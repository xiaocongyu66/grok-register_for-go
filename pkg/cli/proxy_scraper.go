package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/proxy"
	"grok-free-register/grok/pkg/curlcffi"
)

// --- 代理源 ---

// clashSourceURLs 是 proxypool clash.yml 抓取源（主 + 备）。
// 主源走 cdnjsd 反代，备用直连 raw.githubusercontent.com。
var clashSourceURLs = []string{
	"https://cdnjsd.congyu.dpdns.org/gh/xiaocongyu66/proxypool@main/output/clash.yml",
	"https://raw.githubusercontent.com/xiaocongyu66/proxypool/main/output/clash.yml",
}

// extraProxies 是手动添加的高质量代理（t.me/socks 链接格式或 socks5:// URI）。
// 这些代理会直接加入抓取结果，跳过测活直接可用（用户已确认纯净度高）。
// 通过 EXTRA_PROXIES 环境变量配置（逗号或换行分隔）。
var extraProxies = []string{}

// subSourceURLs 是 base64 订阅源列表（解码后为 share-link URI 列表）。
// 这些节点通常是 vless/vmess/trojan/ss，需要 sing-box 中继转成本地 HTTP 代理。
// 通过 SUB_SOURCE_URLS 环境变量配置（逗号或换行分隔）。
var subSourceURLs = []string{}

// githubMirrors 用于 raw.githubusercontent.com 失败时回退。
var githubMirrors = []string{
	"https://github.090227.xyz/raw.githubusercontent.com",
	"https://github.cmliussss.com/raw.githubusercontent.com",
	"https://github.cmliussss.net/raw.githubusercontent.com",
}

// bootstrapProxy 在直连被墙时用作抓取回退代理（取自上一次 代理.txt）。
var bootstrapProxy = ""

const scrapeUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// socksLinkRe 兜底正则：万一 clash 源返回纯文本代理列表时也能抓到 socks5 链接。
var socksLinkRe = regexp.MustCompile(`(?i)(socks5?|socks5h)://[^\s"<>]+`)

func init() {
	// 从环境变量读取额外代理和订阅源
	if v := os.Getenv("EXTRA_PROXIES"); v != "" {
		for _, line := range strings.Split(v, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				extraProxies = append(extraProxies, line)
			}
		}
	}
	if v := os.Getenv("SUB_SOURCE_URLS"); v != "" {
		for _, line := range strings.Split(v, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				subSourceURLs = append(subSourceURLs, line)
			}
		}
	}

	root := projectRoot()
	data, err := os.ReadFile(filepath.Join(root, "代理.txt"))
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "socks5://") || strings.HasPrefix(line, "http://") {
			bootstrapProxy = line
			break
		}
	}
}

// makeProxiedClient 构造一个 HTTP 客户端：无 bootstrapProxy 时直连，否则走 bootstrapProxy。
func makeProxiedClient(timeout time.Duration) *http.Client {
	if bootstrapProxy == "" {
		return &http.Client{Timeout: timeout}
	}
	u, err := url.Parse(bootstrapProxy)
	if err != nil {
		return &http.Client{Timeout: timeout}
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "1080"
	}
	var auth *proxy.Auth
	if u.User != nil {
		user := u.User.Username()
		pass, _ := u.User.Password()
		auth = &proxy.Auth{User: user, Password: pass}
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme == "socks5" || scheme == "socks5h" {
		dialer, err := proxy.SOCKS5("tcp", net.JoinHostPort(host, port), auth, &net.Dialer{Timeout: 10 * time.Second})
		if err != nil {
			return &http.Client{Timeout: timeout}
		}
		return &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					return dialer.Dial(network, addr)
				},
			},
		}
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(u),
		},
	}
}

// fetchURL 拉取 URL 内容，带 UA 和超时。先直连，失败再走 bootstrapProxy。
func fetchURL(url string, timeout time.Duration) ([]byte, error) {
	data, err := fetchURLWith(url, timeout, false)
	if err == nil {
		return data, nil
	}
	if bootstrapProxy == "" {
		return nil, err
	}
	// 直连失败且有 bootstrap 代理时回退到代理
	data2, err2 := fetchURLWith(url, timeout, true)
	if err2 == nil {
		return data2, nil
	}
	return nil, err
}

// fetchURLWith 实际执行抓取：useBootstrap=false 直连，true 走 bootstrapProxy。
func fetchURLWith(url string, timeout time.Duration, useBootstrap bool) ([]byte, error) {
	var client *http.Client
	if useBootstrap {
		client = makeProxiedClient(timeout)
	} else {
		client = &http.Client{Timeout: timeout}
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", scrapeUserAgent)
	req.Header.Set("Accept", "*/*")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// fetchClashSource 依次尝试主备 clash 源，失败时再走 GitHub 镜像。
func fetchClashSource() ([]byte, error) {
	var lastErr error
	for _, src := range clashSourceURLs {
		data, err := fetchURL(src, 20*time.Second)
		if err == nil && len(data) > 0 {
			return data, nil
		}
		if err != nil {
			fmt.Printf("[scrape] %s: %v\n", src, err)
			lastErr = err
		} else {
			lastErr = fmt.Errorf("empty response from %s", src)
		}
		// raw.githubusercontent.com 失败时尝试镜像
		if strings.Contains(src, "raw.githubusercontent.com") {
			prefix := "https://raw.githubusercontent.com"
			path := strings.TrimPrefix(src, prefix)
			for _, m := range githubMirrors {
				if data, err = fetchURL(m+path, 20*time.Second); err == nil && len(data) > 0 {
					return data, nil
				}
			}
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("all clash sources failed")
	}
	return nil, lastErr
}

// fetchAllProxies 抓取 clash.yml + base64 订阅源，解析出 share-link 列表。
func fetchAllProxies() []string {
	var all []string

	// 1. clash.yml 源
	if data, err := fetchClashSource(); err != nil {
		fmt.Printf("[scrape] clash source: FAIL %v\n", err)
	} else {
		nodes := parseClashConfig(string(data))
		if len(nodes) == 0 {
			// 兜底：clash 源返回非 YAML 时用正则抓 socks5 链接
			for _, m := range socksLinkRe.FindAllString(string(data), -1) {
				nodes = append(nodes, strings.TrimSpace(m))
			}
		}
		fmt.Printf("[scrape] clash: %d nodes\n", len(nodes))
		all = append(all, nodes...)
	}

	// 2. base64 订阅源（并发）
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, src := range subSourceURLs {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			nodes := fetchSubscribe(url)
			if len(nodes) > 0 {
				mu.Lock()
				all = append(all, nodes...)
				mu.Unlock()
			}
			fmt.Printf("[scrape] sub %s: %d nodes\n", url, len(nodes))
		}(src)
	}
	wg.Wait()

	// 3. 手动添加的额外代理（t.me/socks 链接 → socks5:// URI）
	for _, link := range extraProxies {
		if p := parseTgSocksLink(link); p != "" {
			all = append(all, p)
		}
	}
	if len(extraProxies) > 0 {
		fmt.Printf("[scrape] extra: %d nodes\n", len(extraProxies))
	}

	return dedup(all)
}

// parseTgSocksLink 把 t.me/socks?server=host&port=port&user=user&pass=pass 转为 socks5:// URI。
// 也接受 socks5:// 或 http:// 开头的直接 URI。
func parseTgSocksLink(link string) string {
	link = strings.TrimSpace(link)
	if strings.HasPrefix(link, "socks5://") || strings.HasPrefix(link, "http://") {
		return link
	}
	// t.me/socks?server=host&port=port&user=user&pass=pass
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	q := u.Query()
	server := q.Get("server")
	port := q.Get("port")
	if server == "" || port == "" {
		return ""
	}
	user := q.Get("user")
	pass := q.Get("pass")
	host := net.JoinHostPort(server, port)
	if user != "" && pass != "" {
		return "socks5://" + url.UserPassword(user, pass).String() + "@" + host
	}
	return "socks5://" + host
}

// fetchSubscribe 拉取 base64 编码的订阅，解码后返回 share-link URI 列表。
func fetchSubscribe(url string) []string {
	data, err := fetchURL(url, 20*time.Second)
	if err != nil {
		return nil
	}
	text := strings.TrimSpace(string(data))
	// 尝试 base64 解码（订阅通常是 base64 编码的）
	if decoded, err := base64.StdEncoding.DecodeString(text); err == nil && len(decoded) > 0 {
		text = string(decoded)
	}
	var proxies []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// 保留所有 share-link scheme（socks5/http/ss/vmess/trojan/vless/hysteria2/hy2）
		if strings.Contains(line, "://") {
			proxies = append(proxies, line)
		}
	}
	return proxies
}

// parseClashConfig 从 Clash YAML 配置中提取代理节点，转换为 share-link URI。
// 支持 JSON-inline 格式： - {"name":"...","server":"...","type":"ss",...}
func parseClashConfig(text string) []string {
	var proxies []string
	inProxies := false
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)

		if !inProxies {
			if trimmed == "proxies:" || strings.HasPrefix(trimmed, "proxies:") {
				inProxies = true
			}
			continue
		}

		// 进入下一个顶层 key（无缩进、以冒号结尾，且不是列表项）
		if len(line) > 0 && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, "-") && strings.HasSuffix(trimmed, ":") {
			break
		}

		node := parseClashProxyLine(trimmed)
		if node == nil {
			continue
		}
		link := clashNodeToShareLink(node)
		if link != "" {
			proxies = append(proxies, link)
		}
	}
	return proxies
}

// parseClashProxyLine 解析单行 proxy 节点（JSON-inline 形式）。
func parseClashProxyLine(trimmed string) map[string]interface{} {
	if strings.HasPrefix(trimmed, "- {") || strings.HasPrefix(trimmed, "-{") {
		js := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
		var node map[string]interface{}
		if err := json.Unmarshal([]byte(js), &node); err == nil {
			return node
		}
	} else if strings.HasPrefix(trimmed, "{") {
		var node map[string]interface{}
		if err := json.Unmarshal([]byte(trimmed), &node); err == nil {
			return node
		}
	}
	return nil
}

// clashNodeToShareLink 把 Clash proxy 节点转换为 share-link URI。
//   - socks5/http → socks5://host:port / http://host:port（带认证则加 user:pass@）
//   - ss/vmess/trojan/vless/hysteria2 → 对应 URI（供 sing-box 中继使用）
func clashNodeToShareLink(node map[string]interface{}) string {
	typ, _ := node["type"].(string)
	server, _ := node["server"].(string)
	port := toInt(node["port"])
	if server == "" || port == 0 {
		return ""
	}
	host := net.JoinHostPort(server, strconv.Itoa(port))
	name, _ := node["name"].(string)

	switch strings.ToLower(typ) {
	case "socks5", "socks":
		u := url.URL{Scheme: "socks5", Host: host}
		applyURLAuth(&u, node)
		return appendFragment(u.String(), name)
	case "http":
		u := url.URL{Scheme: "http", Host: host}
		applyURLAuth(&u, node)
		return appendFragment(u.String(), name)
	case "ss":
		method, _ := node["cipher"].(string)
		password, _ := node["password"].(string)
		if method == "" || password == "" {
			return ""
		}
		creds := base64.URLEncoding.EncodeToString([]byte(method + ":" + password))
		u := url.URL{Scheme: "ss", Host: host, User: url.User(creds)}
		return appendFragment(u.String(), name)
	case "vmess":
		return vmessNodeToLink(node, server, port, name)
	case "trojan":
		password, _ := node["password"].(string)
		if password == "" {
			return ""
		}
		u := url.URL{Scheme: "trojan", Host: host, User: url.User(password)}
		return appendFragment(u.String(), name)
	case "vless":
		id, _ := node["uuid"].(string)
		if id == "" {
			return ""
		}
		u := url.URL{Scheme: "vless", Host: host, User: url.User(id)}
		if flow, ok := node["flow"].(string); ok && flow != "" {
			q := u.Query()
			q.Set("flow", flow)
			u.RawQuery = q.Encode()
		}
		return appendFragment(u.String(), name)
	case "hysteria2", "hy2":
		password, _ := node["password"].(string)
		u := url.URL{Scheme: "hy2", Host: host}
		if password != "" {
			u.User = url.User(password)
		}
		return appendFragment(u.String(), name)
	}
	return ""
}

func applyURLAuth(u *url.URL, node map[string]interface{}) {
	user, _ := node["username"].(string)
	pass, _ := node["password"].(string)
	if user != "" {
		if pass != "" {
			u.User = url.UserPassword(user, pass)
		} else {
			u.User = url.User(user)
		}
	}
}

func appendFragment(s, name string) string {
	if name == "" {
		return s
	}
	return s + "#" + url.QueryEscape(name)
}

// vmessNodeToLink 把 vmess 节点转为 vmess://base64(json) 链接。
func vmessNodeToLink(node map[string]interface{}, server string, port int, name string) string {
	uuid, _ := node["uuid"].(string)
	if uuid == "" {
		return ""
	}
	cipher, _ := node["cipher"].(string)
	if cipher == "" {
		cipher = "auto"
	}
	network, _ := node["network"].(string)
	if network == "" {
		network = "tcp"
	}
	tls := ""
	if b, ok := node["tls"].(bool); ok {
		if b {
			tls = "tls"
		}
	} else if s, ok := node["tls"].(string); ok {
		tls = s
	}
	sni, _ := node["servername"].(string)
	data := map[string]interface{}{
		"v":    "2",
		"ps":   name,
		"add":  server,
		"port": port,
		"id":   uuid,
		"aid":  toInt(node["alterId"]),
		"scy":  cipher,
		"net":  network,
		"tls":  tls,
		"sni":  sni,
	}
	if wsOpts, ok := node["ws-opts"].(map[string]interface{}); ok {
		if path, ok := wsOpts["path"].(string); ok {
			data["path"] = path
		}
		if headers, ok := wsOpts["headers"].(map[string]interface{}); ok {
			for k, v := range headers {
				if vs, ok := v.(string); ok {
					data[strings.ToLower(k)] = vs
				}
			}
		}
	}
	b, _ := json.Marshal(data)
	return "vmess://" + base64.StdEncoding.EncodeToString(b)
}

func toInt(v interface{}) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		v, _ := strconv.Atoi(n)
		return v
	}
	return 0
}

// dedup 去重并去除空白。
func dedup(list []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, s := range list {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}

// testProxy 测试代理能否访问 x.ai。
// 三阶段：TCP 快检 → cloudflare trace 出站探测 → accounts.x.ai 严格判断。
// cfStage 用于分阶段统计；为 nil 时不统计。
func testProxy(proxy string, timeout int) bool {
	return testProxyStaged(proxy, timeout, nil)
}

func testProxyStaged(proxy string, timeout int, cfStage *stageCounter) bool {
	if !strings.HasPrefix(proxy, "socks5://") && !strings.HasPrefix(proxy, "http://") {
		return false
	}
	if cfStage != nil {
		cfStage.tcpAttempt.Add(1)
	}
	// TCP 快检：2 秒（免费代理跨国握手可能 1-3 秒，1 秒太激进）
	if !tcpCheck(proxy, 2) {
		return false
	}
	if cfStage != nil {
		cfStage.tcpOK.Add(1)
	}
	s, err := curlcffi.NewSession(
		curlcffi.WithImpersonate(curlcffi.Chrome131),
		curlcffi.WithProxy(proxy),
		curlcffi.WithTimeout(time.Duration(timeout)*time.Second),
	)
	if err != nil {
		return false
	}
	defer s.Close()
	// 阶段 2: cloudflare trace — 快速过滤能出站但不能到 x.ai 的代理
	r, err := s.Get("https://www.cloudflare.com/cdn-cgi/trace")
	if err != nil || r == nil || r.StatusCode != 200 {
		return false
	}
	if cfStage != nil {
		cfStage.cfOK.Add(1)
	}
	// 阶段 3: accounts.x.ai 严格判断
	r2, err := s.Get("https://accounts.x.ai/")
	ok := err == nil && r2 != nil && (r2.StatusCode == 200 || r2.StatusCode == 403 || r2.StatusCode == 302)
	if ok && cfStage != nil {
		cfStage.xaiOK.Add(1)
	}
	return ok
}

// stageCounter 记录各阶段通过数（线程安全）。
type stageCounter struct {
	tcpAttempt atomic.Int64
	tcpOK      atomic.Int64
	cfOK       atomic.Int64
	xaiOK      atomic.Int64
}

// tcpCheck 快速 TCP 连通测试。
func tcpCheck(proxy string, timeoutSec int) bool {
	host, port := parseProxyHostPort(proxy)
	if host == "" || port == "" {
		return false
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), time.Duration(timeoutSec)*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func parseProxyHostPort(proxy string) (host, port string) {
	if idx := strings.Index(proxy, "@"); idx >= 0 {
		proxy = proxy[idx+1:]
	}
	if idx := strings.Index(proxy, "://"); idx >= 0 {
		proxy = proxy[idx+3:]
	}
	// 去掉 fragment (#name) 和 query (?x=y)
	if idx := strings.IndexAny(proxy, "#?"); idx >= 0 {
		proxy = proxy[:idx]
	}
	if idx := strings.Index(proxy, "/"); idx >= 0 {
		proxy = proxy[:idx]
	}
	if strings.HasPrefix(proxy, "[") {
		end := strings.Index(proxy, "]")
		if end < 0 {
			return "", ""
		}
		host = proxy[1:end]
		rest := proxy[end+1:]
		if strings.HasPrefix(rest, ":") {
			port = rest[1:]
		}
	} else {
		parts := strings.Split(proxy, ":")
		if len(parts) >= 2 {
			host = parts[0]
			port = parts[1]
		}
	}
	return host, port
}

// --- 入口 ---

// ScrapeAndTest 抓取 clash.yml，分流可直接测试的 socks5/http 与需要中继的节点。
func ScrapeAndTest(testWorkers, testTimeout int) []string {
	proxies := fetchAllProxies()

	var testable []string
	var relayNeeded []string
	for _, p := range proxies {
		if strings.HasPrefix(p, "socks5://") || strings.HasPrefix(p, "http://") {
			testable = append(testable, p)
		} else {
			relayNeeded = append(relayNeeded, p)
		}
	}

	fmt.Printf("[scrape] total %d (testable: %d, relay: %d)\n", len(proxies), len(testable), len(relayNeeded))

	var alive []string
	var mu sync.Mutex
	sem := make(chan struct{}, testWorkers)
	var wg sync.WaitGroup
	var stage stageCounter

	for _, p := range testable {
		wg.Add(1)
		go func(proxy string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if testProxyStaged(proxy, testTimeout, &stage) {
				mu.Lock()
				alive = append(alive, proxy)
				mu.Unlock()
			}
		}(p)
	}
	wg.Wait()

	fmt.Printf("[scrape] stages: tcp %d/%d, cf %d, xai %d\n",
		stage.tcpOK.Load(), stage.tcpAttempt.Load(), stage.cfOK.Load(), stage.xaiOK.Load())

	// 测试 relay 节点（vless/trojan/ss/vmess）——通过 minirelay 中继后测 cloudflare
	var relayAlive []string
	if len(relayNeeded) > 0 {
		fmt.Printf("[scrape] testing %d relay nodes via minirelay...\n", len(relayNeeded))
		relayAlive = testRelayNodes(relayNeeded, testWorkers)
	}

	if len(relayAlive) > 0 {
		relayPath := filepath.Join(projectRoot(), "代理-relay.txt")
		os.WriteFile(relayPath, []byte(strings.Join(relayAlive, "\n")+"\n"), 0644)
		fmt.Printf("[scrape] %d alive relay proxies written to 代理-relay.txt\n", len(relayAlive))
	} else {
		// 没有存活的 relay 节点，写空文件避免残留
		relayPath := filepath.Join(projectRoot(), "代理-relay.txt")
		os.WriteFile(relayPath, []byte{}, 0644)
	}

	if len(alive) > 0 {
		path := filepath.Join(projectRoot(), "代理.txt")
		os.WriteFile(path, []byte(strings.Join(alive, "\n")+"\n"), 0644)
		fmt.Printf("[scrape] %d alive proxies written to 代理.txt\n", len(alive))
	} else {
		fmt.Println("[scrape] no alive socks5/http proxies")
	}
	return alive
}

func projectRoot() string {
	root, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(root, "代理.txt")); err == nil {
			return root
		}
		if _, err := os.Stat(filepath.Join(root, "keys")); err == nil {
			return root
		}
		parent := filepath.Dir(root)
		if parent == root {
			break
		}
		root = parent
	}
	return root
}
