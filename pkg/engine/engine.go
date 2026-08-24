package engine

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"grok-free-register/grok/pkg/curlcffi"
)

// SharedConfig holds the signup config with thread-safe access and auto-refresh.
type SharedConfig struct {
	mu   sync.RWMutex
	cfg  *SignupConfig
	proxy *ProxyNode
}

func (s *SharedConfig) Get() *SignupConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *SharedConfig) Refresh(node *ProxyNode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	proxy := ""
	if node != nil {
		proxy = node.URL
	}
	client, err := NewXaiClient(proxy, 60*time.Second)
	if err != nil {
		return
	}
	cfg, err := client.FetchConfig()
	client.Close()
	if err == nil && cfg != nil {
		s.cfg = cfg
		fmt.Printf("[config] refreshed action=%s... source=%s\n", truncate(cfg.ActionID, 12), cfg.Source)
	}
}

// ProxyNode tracks a proxy's health score.
type ProxyNode struct {
	URL       string
	Score     int
	FailCount int
	LastTest  time.Time
	Protocol  string // "tcp" (socks5/http) 或 "udp" (hy2/tuic 等中继节点)
	Healthy   bool   // 启动时连通性验证结果
}

type ProxyPool struct {
	mu          sync.RWMutex
	nodes       []*ProxyNode
	canRegister bool
}

type RegisterEngine struct {
	pool    *ProxyPool
	target  int
	workers int
	stats   *Stats
	stop    chan struct{}
}

func NewRegisterEngine(target, workers int) *RegisterEngine {
	if workers < 1 {
		workers = 1
	}
	return &RegisterEngine{
		pool:    &ProxyPool{},
		target:  target,
		workers: workers,
		stats:   &Stats{},
		stop:    make(chan struct{}),
	}
}

func (e *RegisterEngine) Run() int {
	fmt.Println("========================================")
	fmt.Printf("  Grok 注册引擎  目标=%d  并发=%d\n", e.target, e.workers)
	fmt.Println("========================================")

	// 设置本批注册的开始时间（用于分批文件名 YYYYMMDDHHMM-sso.txt）
	batchStartTime = time.Now()
	fmt.Printf("[batch] 本批时间戳: %s\n", batchStartTime.Format("200601021504"))

	e.pool.loadFromFile()
	e.pool.canRegister = len(e.pool.nodes) > 0
	fmt.Printf("[pool] 代理: %d\n", len(e.pool.nodes))

	// 启动健康检查后台 goroutine:定期重测不健康节点(阶段性死的可能恢复)
	go e.pool.healthCheckLoop()

	// Fetch signup config (use first available proxy)
	var cfg *SignupConfig
	for _, node := range e.pool.nodes {
		cfgClient, err := NewXaiClient(node.URL, 60*time.Second)
		if err != nil {
			continue
		}
		cfg, err = cfgClient.FetchConfig()
		cfgClient.Close()
		if err == nil && cfg != nil {
			fmt.Printf("[config] site_key=%s action=%s... source=%s (via %s)\n", cfg.SiteKey, truncate(cfg.ActionID, 12), cfg.Source, node.URL)
			break
		}
		fmt.Printf("[config] try %s: %v\n", node.URL, err)
	}
	if cfg == nil {
		fmt.Println("[config] all proxies failed for config fetch")
		return 0
	}

	// Shared config with auto-refresh on stale action
	sharedCfg := &SharedConfig{cfg: cfg, proxy: e.pool.getBest()}
	var wg sync.WaitGroup
	n := e.workers
	if e.pool.count() < n {
		n = e.pool.count()
		if n == 0 {
			n = 1
		}
	}
	for i := 0; i < n; i++ {
		wg.Add(1)
		go e.worker(i, sharedCfg, &wg)
	}

	go e.supervisor(sharedCfg, &wg)
	wg.Wait()

	ssoCount := countSSO()
	fmt.Printf("\n[done] SSO: %d\n", ssoCount)
	return ssoCount
}

func (e *RegisterEngine) worker(id int, scfg *SharedConfig, wg *sync.WaitGroup) {
	defer wg.Done()

	for {
		select {
		case <-e.stop:
			return
		default:
		}

		if e.target > 0 && atomic.LoadInt64(&e.stats.Success) >= int64(e.target) {
			return
		}

		// 多代理并行:每个 worker 用不同代理(不同 browser),真正并行求解 Turnstile
		node := e.pool.getByIndex(id)
		if node == nil {
			node = e.pool.getBest() // fallback
		}
		if node == nil {
			time.Sleep(5 * time.Second)
			continue
		}

		e.stats.BumpStart()

		// Get current config (auto-refreshes on stale action)
		cfg := scfg.Get()

		// Use this proxy
		p := NewPipeline(1, 1, node.URL)
		p.stats = e.stats

		if err := p.registerOnce(id, cfg); err != nil {
			e.stats.BumpFail(err.Error())
			fmt.Printf("[X] W%d %v\n", id, err)
			// 失败立即换节点：代理类错误（超时/连接失败）直接移除，其他错误扣分
			errMsg := err.Error()
			if isProxyError(errMsg) {
				e.pool.remove(node.URL)
				fmt.Printf("[pool] removed dead proxy: %s\n", truncateProxyURL(node.URL))
			} else {
				e.pool.recordFail(node.URL)
				// Turnstile 超时也快速换节点（score 清零，下次跳过）
				if strings.Contains(errMsg, "timeout") || strings.Contains(errMsg, "navigate") {
					e.pool.markZero(node.URL)
				}
			}
			// Auto-refresh config on stale action
			if strings.Contains(errMsg, "Server action not found") {
				scfg.Refresh(e.pool.getBest())
			}
		} else {
			n := e.stats.BumpOK()
			fmt.Printf("[OK] W%d (#%d)\n", id, n)
			e.pool.recordSuccess(node.URL)
		}

		fmt.Printf("[*] ok=%d fail=%d pool=%d\n",
			atomic.LoadInt64(&e.stats.Success),
			atomic.LoadInt64(&e.stats.Failed),
			e.pool.count())
	}
}

func (e *RegisterEngine) supervisor(scfg *SharedConfig, wg *sync.WaitGroup) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-e.stop:
			return
		case <-t.C:
			if e.target > 0 && atomic.LoadInt64(&e.stats.Success) >= int64(e.target) {
				return
			}
		}
	}
}

// --- ProxyPool ---

func (p *ProxyPool) loadFromFile() {
	// 1. 加载 socks5/http 直连代理
	data, _ := os.ReadFile(ProxyFile())
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			p.mu.Lock()
			p.nodes = append(p.nodes, &ProxyNode{URL: line, Score: 50})
			p.mu.Unlock()
		}
	}

	// 2. 加载需要中继的节点（ss/vmess/trojan/vless/hysteria2），用 minirelay 转成本地 HTTP 代理。
	// 限制最多 8 个中继（避免端口/内存占用过多），优先 hysteria2 > vless > trojan > ss > vmess
	relayFile := filepath.Join(filepath.Dir(ProxyFile()), "代理-relay.txt")
	relayData, _ := os.ReadFile(relayFile)
	var hy2, vless, trojan, ss, vmess []string
	for _, line := range strings.Split(string(relayData), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "hysteria2://") || strings.HasPrefix(line, "hy2://"):
			hy2 = append(hy2, line)
		case strings.HasPrefix(line, "vless://"):
			vless = append(vless, line)
		case strings.HasPrefix(line, "trojan://"):
			trojan = append(trojan, line)
		case strings.HasPrefix(line, "ss://"):
			ss = append(ss, line)
		case strings.HasPrefix(line, "vmess://"):
			vmess = append(vmess, line)
		}
	}
	// 按优先级选取，最多 8 个（hysteria2 最优先，因为这些节点通常更稳定）
	maxRelays := 8
	var relayNodes []string
	relayNodes = append(relayNodes, hy2...)
	relayNodes = append(relayNodes, vless...)
	relayNodes = append(relayNodes, trojan...)
	relayNodes = append(relayNodes, ss...)
	relayNodes = append(relayNodes, vmess...)
	if len(relayNodes) > maxRelays {
		relayNodes = relayNodes[:maxRelays]
	}
	if len(relayNodes) > 0 {
		fmt.Printf("[pool] starting minirelay for %d relay nodes (hy2=%d vless=%d trojan=%d ss=%d vmess=%d)\n",
			len(relayNodes), len(hy2), len(vless), len(trojan), len(ss), len(vmess))
		localProxies := startMinirelays(relayNodes)
		// 中继节点放在 nodes 列表前面，且 score 更高，config fetch 和 getBest 优先用它们
		p.mu.Lock()
		relayNodesList := []*ProxyNode{}
		for _, lp := range localProxies {
			relayNodesList = append(relayNodesList, &ProxyNode{URL: lp, Score: 80, Protocol: "tcp", Healthy: true})
		}
		// 中继节点 + socks5 直连节点
		p.nodes = append(relayNodesList, p.nodes...)
		p.mu.Unlock()
		fmt.Printf("[pool] %d relay proxies added to pool (score=80, prioritized)\n", len(localProxies))
	}

	// 3. 启动时验证代理池连通性,去掉不通的节点
	p.verifyAll()
}

// verifyAll 并发验证所有代理节点的连通性。
// 中继节点(127.0.0.1:19xxx)测本地 TCP 端口;
// socks5/http 直连节点测到目标服务器的 TCP(或对 hy2 域名测 UDP)。
// 不通的节点 Score 设为 0,运行时跳过。
func (p *ProxyPool) verifyAll() {
	p.mu.RLock()
	total := len(p.nodes)
	nodes := make([]*ProxyNode, total)
	copy(nodes, p.nodes)
	p.mu.RUnlock()

	if total == 0 {
		return
	}
	fmt.Printf("[pool] 验证 %d 个代理节点连通性...\n", total)

	type result struct {
		node    *ProxyNode
		healthy bool
		err     string
	}
	results := make(chan result, total)
	var wg sync.WaitGroup

	for _, node := range nodes {
		wg.Add(1)
		go func(n *ProxyNode) {
			defer wg.Done()
			healthy, errStr := verifyNode(n.URL)
			results <- result{node: n, healthy: healthy, err: errStr}
		}(node)
	}
	wg.Wait()
	close(results)

	healthy := 0
	for r := range results {
		r.node.Healthy = r.healthy
		r.node.LastTest = time.Now()
		if !r.healthy {
			r.node.Score = 0
		}
		if r.healthy {
			healthy++
		}
	}
	fmt.Printf("[pool] 连通性验证完成: %d/%d 可用\n", healthy, total)
}

// verifyNode 验证单个代理 URL 的连通性。
// 返回 (是否可用, 错误描述)。
func verifyNode(proxyURL string) (bool, string) {
	// 解析 URL 拿到 host:port
	u, err := url.Parse(proxyURL)
	if err != nil {
		return false, "parse error: " + err.Error()
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else if u.Scheme == "http" || u.Scheme == "socks5" {
			port = "1080"
		} else {
			port = "443"
		}
	}
	target := net.JoinHostPort(host, port)

	// 中继节点(127.0.0.1)或 TCP 代理(socks5/http):测 TCP
	// hy2/tuic 等如果是直连 URL(不是中继)需测 UDP,但目前 relay 节点都是本地 TCP
	timeout := 5 * time.Second
	conn, err := net.DialTimeout("tcp", target, timeout)
	if err != nil {
		return false, "tcp dial: " + err.Error()
	}
	conn.Close()
	return true, ""
}

// healthCheckLoop 定期重测不健康节点(每 2 分钟)。
// 阶段性死的节点可能几分钟后恢复,重测成功则恢复 Score 并标记 Healthy。
func (p *ProxyPool) healthCheckLoop() {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		p.mu.RLock()
		unhealthy := []*ProxyNode{}
		for _, n := range p.nodes {
			if !n.Healthy || n.Score <= 0 {
				unhealthy = append(unhealthy, n)
			}
		}
		p.mu.RUnlock()
		if len(unhealthy) == 0 {
			continue
		}

		var wg sync.WaitGroup
		for _, n := range unhealthy {
			wg.Add(1)
			go func(node *ProxyNode) {
				defer wg.Done()
				healthy, _ := verifyNode(node.URL)
				if healthy {
					p.mu.Lock()
					node.Healthy = true
					node.Score = 50 // 恢复到默认分数
					node.LastTest = time.Now()
					p.mu.Unlock()
					fmt.Printf("[pool] 节点恢复: %s\n", truncateURL(node.URL))
				} else {
					p.mu.Lock()
					node.LastTest = time.Now()
					p.mu.Unlock()
				}
			}(n)
		}
		wg.Wait()
	}
}

// truncateURL 截断 URL 用于日志(隐藏密码等敏感信息)。
func truncateURL(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		if len(u) > 40 {
			return u[:40] + "..."
		}
		return u
	}
	// 只保留 scheme://host:port
	out := parsed.Scheme + "://" + parsed.Hostname()
	if parsed.Port() != "" {
		out += ":" + parsed.Port()
	}
	return out
}

func (p *ProxyPool) count() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.nodes)
}

// getByIndex 按索引取节点（多代理并行：每个 worker 用不同代理）。
// index 超过节点数时取模轮换，确保每个 worker 拿到不同代理。
func (p *ProxyPool) getByIndex(index int) *ProxyNode {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.nodes) == 0 {
		return nil
	}
	// 跳过 score=0 的节点（失败太多的）
	for i := 0; i < len(p.nodes); i++ {
		idx := (index + i) % len(p.nodes)
		if p.nodes[idx].Score > 0 {
			return p.nodes[idx]
		}
	}
	return nil
}

// markZero 把节点 score 设为 0（下次 getByIndex 跳过，但不移除，保留可能恢复）。
func (p *ProxyPool) markZero(url string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, n := range p.nodes {
		if n.URL == url {
			n.Score = 0
			return
		}
	}
}

// isProxyError 判断是否代理类错误（超时/连接失败/EOF），这类错误应立即换节点。
func isProxyError(msg string) bool {
	keywords := []string{
		"i/o timeout", "connection timed out", "connection refused",
		"connection reset", "EOF", "host unreachable", "no route to host",
		"network is unreachable", "ERR_PROXY", "ERR_SOCKS", "ERR_CONNECTION",
		"socks connect", "dial tcp",
	}
	for _, k := range keywords {
		if strings.Contains(msg, k) {
			return true
		}
	}
	return false
}

// truncateProxyURL 截断代理 URL 用于日志。
func truncateProxyURL(url string) string {
	if len(url) > 50 {
		return url[:50] + "..."
	}
	return url
}

func (p *ProxyPool) getBest() *ProxyNode {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.nodes) == 0 {
		return nil
	}
	best := p.nodes[0]
	for _, n := range p.nodes[1:] {
		if n.Score > best.Score {
			best = n
		}
	}
	return best
}

func (p *ProxyPool) recordSuccess(url string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, n := range p.nodes {
		if n.URL == url {
			n.Score += 5
			if n.Score > 100 {
				n.Score = 100
			}
			n.FailCount = 0
			return
		}
	}
}

func (p *ProxyPool) recordFail(url string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, n := range p.nodes {
		if n.URL == url {
			n.Score -= 10
			if n.Score < 0 {
				n.Score = 0
			}
			n.FailCount++
			return
		}
	}
}

func (p *ProxyPool) remove(url string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, n := range p.nodes {
		if n.URL == url {
			p.nodes = append(p.nodes[:i], p.nodes[i+1:]...)
			return
		}
	}
}

// --- helpers ---

func testProxy(proxy string) bool {
	s, err := curlcffi.NewSession(
		curlcffi.WithImpersonate(curlcffi.Chrome131),
		curlcffi.WithProxy(proxy),
		curlcffi.WithTimeout(5*time.Second),
	)
	if err != nil {
		return false
	}
	defer s.Close()
	r, err := s.Get(SignupURLGrok)
	return err == nil && r != nil && (r.StatusCode == 200 || r.StatusCode == 403)
}

func countSSO() int {
	data, _ := os.ReadFile(SSOFile())
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	count := 0
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			count++
		}
	}
	return count
}
