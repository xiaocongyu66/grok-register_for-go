package engine

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	"grok-free-register/grok/pkg/curlcffi"
)

const (
	SiteURL          = "https://accounts.x.ai"
	SignupURLGrok    = SiteURL + "/sign-up?redirect=grok-com"
	ConnectCreate    = SiteURL + "/auth_mgmt.AuthManagement/CreateEmailValidationCode"
	ConnectVerify    = SiteURL + "/auth_mgmt.AuthManagement/VerifyEmailValidationCode"
	DefaultUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	TurnstileSiteKey = "0x4AAAAAAAhr9JGVDZbrZOo0"

	DefaultNextAction      = "7f7f6cee188bd9cc17a3fb9dbde4abe224f21af0e3"
	DefaultRouterStateTree = `["",{"children":["(app)",{"children":["(auth)",{"children":["sign-up",{"children":["__PAGE__",{},null,null,0]},null,null,0]},null,null,0]},null,null,0]},null,null,16]`
)

var (
	jwtRe         = regexp.MustCompile(`eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}`)
	siteKeyRe    = regexp.MustCompile(`0x4AAAAAAA[a-zA-Z0-9_-]+`)
	hex40Re      = regexp.MustCompile(`[a-fA-F0-9]{40,50}`)
	jsSrcRe      = regexp.MustCompile(`src="(/_next/static/[^"]+\.js[^"]*)"`)
	setCookieURLRe = regexp.MustCompile(`https?://[^\s"'<>\\]+set-cookie/?\?q=eyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+`)
	setCookieRelRe  = regexp.MustCompile(`(/[A-Za-z0-9_./-]*set-cookie/?\?q=eyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+)`)
	ssoNamedRe  = regexp.MustCompile(`(?i)(?:^|[;,\s'"\\])sso=(eyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+)`)
	ssoNearRe   = regexp.MustCompile(`(?i)(?:sso|session)[^e]{0,40}(eyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+)`)
)

type SignupConfig struct {
	SiteKey   string
	ActionID  string
	StateTree string
	Source    string
}

type XaiClient struct {
	sess  *curlcffi.Session
	ares  *curlcffi.AresClient
	proxy string
	ua    string
}

func NewXaiClient(proxy string, timeout time.Duration) (*XaiClient, error) {
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	s, err := curlcffi.NewSession(
		curlcffi.WithImpersonate(curlcffi.Chrome131),
		curlcffi.WithProxy(proxy),
		curlcffi.WithTimeout(timeout),
	)
	if err != nil {
		return nil, err
	}
	// AresClient: curlcffi (Chrome131 TLS) by default, launches browser only
	// when Cloudflare challenge (403/503 + markers) is detected. Cookies from
	// the browser solve are fed back into the session for reuse.
	headless := envBool("CF_ARES_HEADLESS", true)
	chromePath := envFirst("SOLVER_CHROME_PATH", "CHROME_BIN", "CF_ARES_CHROME_PATH")
	// Chrome 不支持带认证的 socks5，浏览器专用代理需先转成无认证本地中继
	browserProxy := maybeRelayProxy(proxy)
	ares := curlcffi.NewAresClient(
		curlcffi.AresWithProxy(proxy),
		curlcffi.AresWithBrowserProxy(browserProxy),
		curlcffi.AresWithHeadless(headless),
		curlcffi.AresWithTimeout(int(timeout.Seconds())),
		curlcffi.AresWithChromePath(chromePath),
		curlcffi.AresWithImpersonate(curlcffi.Chrome131),
	)
	return &XaiClient{sess: s, ares: ares, proxy: proxy, ua: DefaultUserAgent}, nil
}

func (c *XaiClient) Close() {
	if c.ares != nil {
		c.ares.Close()
	}
	c.sess.Close()
}

// syncAresCookies copies cookies from the AresClient session to the raw session,
// so subsequent sess.Post/Get calls (gRPC, SSO hop) carry cf_clearance etc.
func (c *XaiClient) syncAresCookies() {
	if c.ares == nil {
		return
	}
	if cookies := c.ares.Cookies(); len(cookies) > 0 {
		c.sess.SetCookies(cookies)
	}
}

func (c *XaiClient) browserHeaders() map[string]string {
	return map[string]string{
		"User-Agent":         c.ua,
		"Accept-Language":   "en-US,en;q=0.9",
		"sec-ch-ua":          `"Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"`,
		"sec-ch-ua-mobile":   "?0",
		"sec-ch-ua-platform": `"Linux"`,
		"sec-fetch-dest":     "empty",
		"sec-fetch-mode":     "cors",
		"sec-fetch-site":     "same-origin",
	}
}

func (c *XaiClient) grpcHeaders() map[string]string {
	h := c.browserHeaders()
	h["Content-Type"] = "application/grpc-web+proto"
	h["X-Grpc-Web"] = "1"
	h["X-User-Agent"] = "connect-es/2.1.1"
	h["Origin"] = SiteURL
	h["Referer"] = SignupURLGrok
	h["Accept"] = "*/*"
	return h
}

func (c *XaiClient) Warm() error {
	h := c.browserHeaders()
	h["Accept"] = "text/html,application/xhtml+xml,*/*"
	resp, err := c.ares.Get(SignupURLGrok, nil, h)
	if err != nil {
		// Fallback to raw session if AresClient fails (e.g. browser unavailable)
		_, ferr := c.sess.Get(SignupURLGrok, func(r *curlcffi.Request) {
			r.Headers = h
		})
		return ferr
	}
	c.syncAresCookies()
	if resp.StatusCode == 403 || resp.StatusCode == 503 {
		return fmt.Errorf("warm: cf block http=%d body=%s", resp.StatusCode, truncStr(string(resp.Content), 150))
	}
	return nil
}

// --- gRPC: CreateEmailValidationCode ---

func (c *XaiClient) CreateEmailCode(email string) error {
	frame := grpcWebFrame(pbStr(1, email))
	headers := c.grpcHeaders()

	// 首次尝试：直接用 tls-client 请求
	resp, err := c.ares.Post(ConnectCreate, frame, headers)
	if err != nil {
		// 超时/网络错误：直接返回，让 engine 轮换代理重试（不在此处做 CF solve，
		// 因为 CF solve 要启动浏览器，开销大，且代理本身不通时 CF solve 也会失败）
		return fmt.Errorf("create email: %w", err)
	}
	c.syncAresCookies()
	st := aresGRPCStatus(resp)

	// Bare 403 (empty body, no CF markers) = Cloudflare WAF block without
	// JS challenge. Force a browser solve to obtain cf_clearance, then retry.
	if resp.StatusCode == 403 && len(resp.Content) == 0 {
		fmt.Println("[cfares] bare 403 on CreateEmailCode, forcing browser solve...")
		if solveErr := c.forceCFSolve(); solveErr != nil {
			return fmt.Errorf("create email: 403 + cf solve failed: %w", solveErr)
		}
		resp, err = c.ares.Post(ConnectCreate, frame, headers)
		if err != nil {
			return fmt.Errorf("create email retry: %w", err)
		}
		c.syncAresCookies()
		st = aresGRPCStatus(resp)
	}

	if resp.StatusCode != 200 || (st != "" && st != "0") {
		preview := truncStr(string(resp.Content), 200)
		return fmt.Errorf("create email http=%d grpc=%s body=%s", resp.StatusCode, st, preview)
	}
	return nil
}

// forceCFSolve launches the browser to visit the signup page, waits for
// Cloudflare to clear, and stores cf_clearance into the session.
func (c *XaiClient) forceCFSolve() error {
	_, err := c.ares.SolveChallenge(SignupURLGrok, 1)
	if err != nil {
		return err
	}
	c.syncAresCookies()
	return nil
}

// --- gRPC: VerifyEmailValidationCode ---

func (c *XaiClient) VerifyEmailCode(email, code string) error {
	code = strings.NewReplacer("-", "", " ", "").Replace(code)
	inner := append(pbStr(1, email), pbStr(2, code)...)
	frame := grpcWebFrame(inner)
	headers := c.grpcHeaders()
	resp, err := c.ares.Post(ConnectVerify, frame, headers)
	if err != nil {
		return fmt.Errorf("verify email: %w", err)
	}
	c.syncAresCookies()
	st := aresGRPCStatus(resp)
	if resp.StatusCode != 200 || (st != "" && st != "0") {
		preview := truncStr(string(resp.Content), 200)
		return fmt.Errorf("verify email http=%d grpc=%s body=%s", resp.StatusCode, st, preview)
	}
	return nil
}

// --- Server Action: Signup ---

func (c *XaiClient) SignupServerAction(body []byte, actionID, stateTree string) (sso string, err error) {
	h := c.browserHeaders()
	h["Accept"] = "text/x-component"
	h["Content-Type"] = "text/plain;charset=UTF-8"
	h["Next-Action"] = actionID
	h["Next-Router-State-Tree"] = stateTree
	h["Origin"] = SiteURL
	h["Referer"] = SignupURLGrok

	resp, err := c.sess.Post(SignupURLGrok, func(r *curlcffi.Request) {
		r.Headers = h
		r.Data = body
	})
	if err != nil {
		return "", fmt.Errorf("signup: %w", err)
	}

	text := string(resp.Content)

	// SSO extraction: Set-Cookie → jar → body → hop chain
	sso = ssoFromCookies(resp.Cookies)
	if sso == "" {
		sso = c.jarSSO()
	}
	if sso == "" {
		sso = ExtractSSOFromText(text)
	}
	// Follow SSO hop chain from response body
	if sso == "" {
		hops := extractAllSetCookieURLs(text)
		if DebugMode() {
			fmt.Printf("[debug] SSO extraction: resp.Cookies=%d, body_len=%d, hops=%d\n", len(resp.Cookies), len(text), len(hops))
			for i, hop := range hops {
				if len(hop) > 80 {
					hop = hop[:80]
				}
				fmt.Printf("[debug] hop[%d]: %s\n", i, hop)
			}
			// 打印 body 里所有 JWT(脱敏)
			for i, m := range jwtRe.FindAllString(text, -1) {
				preview := m
				if len(preview) > 40 {
					preview = preview[:40] + "..."
				}
				fmt.Printf("[debug] jwt[%d] in body: %s (len=%d, isSession=%v)\n", i, preview, len(m), isSessionSSO(m))
			}
			// 打印 session cookie jar 里所有 cookie(从各域名)
			debugDomains := []string{"https://grok.com", "https://accounts.x.ai", "https://auth.grok.com"}
			for _, d := range debugDomains {
				jarCookies := c.sess.GetCookiesForURL(d)
				if len(jarCookies) > 0 {
					fmt.Printf("[debug] jar cookies for %s: %d\n", d, len(jarCookies))
					for name, val := range jarCookies {
						preview := val
						if len(preview) > 30 {
							preview = preview[:30] + "..."
						}
						fmt.Printf("[debug]   %s = %s\n", name, preview)
					}
				}
			}
		}
		for _, hop := range hops {
			v := c.followSSOHop(hop)
			if DebugMode() && v != "" {
				preview := v
				if len(preview) > 40 {
					preview = preview[:40] + "..."
				}
				fmt.Printf("[debug] followSSOHop returned: %s (isSession=%v)\n", preview, isSessionSSO(v))
			}
			if isSessionSSO(v) {
				sso = v
				break
			}
		}
	}
	if sso == "" {
		sso = c.jarSSO()
	}

	if resp.StatusCode >= 400 {
		preview := text
		if len(preview) > 200 {
			preview = preview[:200]
		}
		return "", fmt.Errorf("signup http=%d body=%s", resp.StatusCode, preview)
	}
	if sso == "" {
		return "", fmt.Errorf("signup ok but no SSO")
	}
	return sso, nil
}

// followSSOHop follows set-cookie redirect chain to get SSO JWT
// 优先用 ares(浏览器,能过 Cloudflare),因为 set-cookie URL 用 curl 访问会 400
func (c *XaiClient) followSSOHop(start string) string {
	hops := expandSSOHopURLs([]string{start})
	seen := map[string]bool{}

	// 小改方案:用同一 ares 浏览器会话访问 signup 页(建立 grok.com 上下文 + 过 CF),
	// 再访问 set-cookie URL,保持会话连续,服务器才会 set sso cookie。
	if c.ares != nil {
		// 1. 先访问 signup 页,建立 grok.com 上下文 + 过 Cloudflare
		if err := c.ares.Navigate(SignupURLGrok); err != nil {
			if DebugMode() {
				fmt.Printf("[debug] followSSOHop: 建立 grok.com 上下文失败: %v\n", err)
			}
		} else if DebugMode() {
			bc := c.ares.BrowserCookies()
			fmt.Printf("[debug] followSSOHop: 访问 signup 后浏览器 cookie: %v\n", mapKeys(bc))
		}

		// 2. 依次访问 set-cookie URL(同一浏览器会话)
		for _, hop := range hops {
			if hop == "" || seen[hop] {
				continue
			}
			seen[hop] = true

			if err := c.ares.Navigate(hop); err != nil {
				if DebugMode() {
					hopPreview := hop
					if len(hopPreview) > 60 {
						hopPreview = hopPreview[:60]
					}
					fmt.Printf("[debug] followSSOHop(ares) %s -> error: %v\n", hopPreview, err)
				}
				continue
			}

			// 从浏览器 cookie 拿 sso(包括 HttpOnly)
			bc := c.ares.BrowserCookies()
			if DebugMode() {
				hopPreview := hop
				if len(hopPreview) > 60 {
					hopPreview = hopPreview[:60]
				}
				fmt.Printf("[debug] followSSOHop(ares) %s -> 浏览器 cookie: %v\n", hopPreview, mapKeys(bc))
			}
			if sso, ok := bc["sso"]; ok && isSessionSSO(sso) {
				return sso
			}
		}
	}

	// 回退:用 curl session,手动逐跳跟随重定向,每跳检查 Set-Cookie。
	// 不依赖 curl_cffi 的 jar(跨域 cookie 存储有 bug),直接从每跳响应提取 sso。
	seen = map[string]bool{}
	for _, hop := range hops {
		if hop == "" || seen[hop] {
			continue
		}
		seen[hop] = true

		sso := c.followRedirectsForSSO(hop)
		if sso != "" {
			return sso
		}
	}
	return ""
}

// followRedirectsForSSO 手动逐跳跟随重定向,每跳检查 Set-Cookie 里的 sso。
// 不依赖 curl_cffi 的 jar(跨域 cookie 存储有 bug),直接从每跳响应提取。
// 最多跟随 10 跳,防止重定向循环。
func (c *XaiClient) followRedirectsForSSO(startURL string) string {
	current := startURL
	visited := map[string]bool{}
	for i := 0; i < 10; i++ {
		if current == "" || visited[current] {
			break
		}
		visited[current] = true

		resp, err := c.sess.Get(current, func(r *curlcffi.Request) {
			h := c.browserHeaders()
			h["Accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
			h["Referer"] = SiteURL + "/"
			h["Origin"] = SiteURL
			h["sec-fetch-site"] = "cross-site"
			h["sec-fetch-mode"] = "navigate"
			h["sec-fetch-dest"] = "document"
			r.Headers = h
			r.AllowRedirects = boolPtr(false) // 手动跟随
		})
		if err != nil {
			if DebugMode() {
				fmt.Printf("[debug] followRedirects hop %d %s -> error: %v\n", i, truncateForLog(current, 60), err)
			}
			break
		}

		if DebugMode() {
			cookieNames := make([]string, 0, len(resp.Cookies))
			for _, ck := range resp.Cookies {
				cookieNames = append(cookieNames, ck.Name)
			}
			location := ""
			if resp.Headers != nil {
				location = resp.Headers.Get("Location")
			}
			fmt.Printf("[debug] followRedirects hop %d %s -> status=%d cookies=%v location=%s\n",
				i, truncateForLog(current, 60), resp.StatusCode, cookieNames, truncateForLog(location, 60))
		}

		// 检查这跳的 Set-Cookie 是否含 sso
		if v := ssoFromCookies(resp.Cookies); isSessionSSO(v) {
			return v
		}

		// 检查 jar(有些 cookie 可能进了 jar)
		if v := c.jarSSO(); isSessionSSO(v) {
			return v
		}

		// 跟随重定向
		if resp.StatusCode >= 300 && resp.StatusCode < 400 && resp.Headers != nil {
			location := resp.Headers.Get("Location")
			if location == "" {
				break
			}
			// 处理相对路径
			if strings.HasPrefix(location, "http://") || strings.HasPrefix(location, "https://") {
				current = location
			} else {
				base, _ := url.Parse(current)
				loc, err := url.Parse(location)
				if err != nil {
					break
				}
				current = base.ResolveReference(loc).String()
			}
			continue
		}

		// 非重定向响应,检查 body 里的 JWT
		if v := ExtractSSOFromText(string(resp.Content)); isSessionSSO(v) {
			return v
		}
		break
	}
	return ""
}



func (c *XaiClient) FetchConfig() (*SignupConfig, error) {
	resp, err := c.sess.Get(SignupURLGrok, func(r *curlcffi.Request) {
		h := c.browserHeaders()
		h["Accept"] = "text/html,application/xhtml+xml,*/*"
		r.Headers = h
	})
	if err != nil {
		return nil, fmt.Errorf("fetch config: %w", err)
	}
	html := string(resp.Content)
	cfg := &SignupConfig{SiteKey: TurnstileSiteKey, Source: "default"}

	if m := siteKeyRe.FindString(html); m != "" {
		cfg.SiteKey = m
	}

	cfg.StateTree = scrapeStateTree(html)
	if cfg.StateTree == "" {
		cfg.StateTree = DefaultRouterStateTree
		cfg.Source += "+default_tree"
	}

	// Scrape action ID from JS files (parallel, first result or 15s deadline wins)
	jsURLs := unique(jsSrcRe.FindAllStringSubmatch(html, -1))
	fmt.Printf("[config] JS files: %d, loading in parallel (15s deadline)...\n", len(jsURLs))

	type jsResult struct {
		path     string
		actionID string
	}
	resultCh := make(chan jsResult, len(jsURLs))
	var wg sync.WaitGroup
	for _, path := range jsURLs {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			js, err := c.fetchJS(p)
			if err != nil || js == "" {
				return
			}
			if !strings.Contains(js, "createUser") && !strings.Contains(js, "registerUser") && !strings.Contains(js, "emailValidation") {
				return
			}
			if hexes := hex40Re.FindAllString(js, -1); len(hexes) > 0 {
				select {
				case resultCh <- jsResult{path: p, actionID: hexes[0]}:
				default:
				}
			}
		}(path)
	}
	allDone := make(chan struct{})
	go func() { wg.Wait(); close(allDone) }()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	gotResult := false
	for !gotResult {
		select {
		case r := <-resultCh:
			if cfg.ActionID == "" {
				cfg.ActionID = r.actionID
				cfg.Source += "+scrape_action"
				fmt.Printf("[config] action ID found: %s (from %s)\n", cfg.ActionID, r.path)
			}
			gotResult = true
		case <-timer.C:
			fmt.Println("[config] JS scrape deadline reached, using default action")
			gotResult = true
		case <-allDone:
			gotResult = true
		}
	}

	if cfg.ActionID == "" {
		cfg.ActionID = DefaultNextAction
		cfg.Source += "+default_action"
	}

	return cfg, nil
}

func (c *XaiClient) fetchJS(path string) (string, error) {
	resp, err := c.sess.Get(SiteURL+path, func(r *curlcffi.Request) {
		h := c.browserHeaders()
		h["Referer"] = SignupURLGrok
		r.Headers = h
	})
	if err != nil {
		return "", err
	}
	return string(resp.Content), nil
}

// --- SSO helpers ---

func (c *XaiClient) jarSSO() string {
	// sso cookie 可能 set 在多个域名上,逐个尝试
	domains := []string{
		"https://grok.com",
		"https://accounts.x.ai",
		"https://auth.grok.com",
		"https://auth.grokusercontent.com",
		"https://x.ai",
	}
	for _, d := range domains {
		for name, val := range c.sess.GetCookiesForURL(d) {
			if name == "sso" && isSessionSSO(val) {
				return val
			}
		}
	}
	return ""
}

func ssoFromCookies(cookies []*fhttp.Cookie) string {
	for _, ck := range cookies {
		if ck.Name == "sso" && isSessionSSO(ck.Value) {
			return ck.Value
		}
	}
	return ""
}

func (c *XaiClient) ssoFromRespCookies(cookies []*fhttp.Cookie) string {
	return ssoFromCookies(cookies)
}

func isSessionSSO(tok string) bool {
	if tok == "" || !strings.HasPrefix(tok, "eyJ") || strings.Count(tok, ".") != 2 {
		return false
	}
	if len(tok) <= 40 {
		return false
	}
	// 校验 payload:拒绝登录票据(payload 含 config,需先访问 success_url 兑换)。
	// 真正的 SSO cookie payload 可能含 session_id 或其他字段,只要不是登录票据就接受。
	claims := decodeJWTClaims(tok)
	if claims == nil {
		// payload 解码失败,但格式合法,保守接受(避免误拒真实 SSO)
		return true
	}
	if _, hasConfig := claims["config"]; hasConfig {
		return false
	}
	return true
}

// decodeJWTClaims 解码 JWT 的 payload 部分,失败返回 nil。
// 只做 base64 解码 + JSON 反序列化,不校验签名。
func decodeJWTClaims(tok string) map[string]any {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return nil
	}
	payload := parts[1]
	// base64url padding 补齐
	if pad := 4 - len(payload)%4; pad != 4 {
		payload += strings.Repeat("=", pad)
	}
	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		decoded, err = base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return nil
		}
	}
	var claims map[string]any
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return nil
	}
	return claims
}

// ExtractSSOFromText finds SSO JWT in RSC/HTML body
// mapKeys 返回 map 的所有 key(用于 debug 日志)。
func mapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func ExtractSSOFromText(text string) string {
	body := normalizeRSC(text)
	if m := ssoNamedRe.FindStringSubmatch(body); len(m) > 1 && isSessionSSO(m[1]) {
		return m[1]
	}
	if m := ssoNearRe.FindStringSubmatch(body); len(m) > 1 && isSessionSSO(m[1]) {
		return m[1]
	}
	for _, m := range jwtRe.FindAllString(body, -1) {
		if isSessionSSO(m) {
			return m
		}
	}
	return ""
}

func normalizeRSC(text string) string {
	t := text
	t = strings.ReplaceAll(t, `\u0026`, "&")
	t = strings.ReplaceAll(t, `\u003d`, "=")
	t = strings.ReplaceAll(t, `\u002F`, "/")
	t = strings.ReplaceAll(t, `\/`, "/")
	return t
}

func extractAllSetCookieURLs(text string) []string {
	body := normalizeRSC(text)
	var found []string
	seen := map[string]bool{}
	add := func(u string) {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		found = append(found, u)
	}

	// 优先:解码 body 里所有 JWT,如果 payload 含 config.success_url,用那个 URL。
	// 登录票据的结构是 {config:{success_url:"https://auth.grokusercontent.com/set-cookie?q=<内层JWT>"}},
	// success_url 里的内层 JWT 才是真正用于 set-cookie 的,不能用外层 JWT 拼URL。
	for _, jwt := range jwtRe.FindAllString(body, -1) {
		claims := decodeJWTClaims(jwt)
		if claims == nil {
			continue
		}
		config, ok := claims["config"].(map[string]any)
		if !ok {
			continue
		}
		if successURL, ok := config["success_url"].(string); ok && successURL != "" {
			add(successURL)
		}
	}

	// 回退:正则匹配 set-cookie URL(可能用外层 JWT,通常 400)
	for _, m := range setCookieURLRe.FindAllString(body, -1) {
		add(m)
	}
	for _, m := range setCookieRelRe.FindAllStringSubmatch(body, -1) {
		if len(m) > 1 {
			add(SiteURL + m[1])
		}
	}
	if len(found) == 0 {
		if idx := strings.Index(strings.ToLower(body), "set-cookie"); idx >= 0 {
			window := body[idx:]
			if len(window) > 400 {
				window = window[:400]
			}
			if j := jwtRe.FindString(window); j != "" {
				add("https://auth.grokusercontent.com/set-cookie?q=" + j)
			}
		}
	}
	return found
}

func expandSSOHopURLs(urls []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(u string) {
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		out = append(out, u)
	}
	for _, u := range urls {
		add(u)
		jwt := jwtFromSetCookieURL(u)
		if jwt == "" {
			continue
		}
		add("https://auth.grokusercontent.com/set-cookie?q=" + jwt)
		add("https://auth.grokipedia.com/set-cookie?q=" + jwt)
		add("https://auth.grok.com/set-cookie?q=" + jwt)
		add("https://auth.x.ai/set-cookie?q=" + jwt)
	}
	return out
}

func jwtFromSetCookieURL(u string) string {
	raw, err := url.QueryUnescape(u)
	if err != nil {
		raw = u
	}
	if i := strings.Index(raw, "q="); i >= 0 {
		rest := raw[i+2:]
		if j := strings.IndexAny(rest, "&\"' "); j >= 0 {
			rest = rest[:j]
		}
		if strings.HasPrefix(rest, "eyJ") {
			return rest
		}
	}
	return ""
}

func boolPtr(v bool) *bool { return &v }

// --- protobuf helpers ---

func pbStr(field int, s string) []byte {
	tag := byte(field<<3 | 2)
	b := []byte(s)
	out := []byte{tag}
	out = append(out, pbVarint(len(b))...)
	return append(out, b...)
}

func pbVarint(n int) []byte {
	var parts []byte
	for n > 0x7f {
		parts = append(parts, byte(n&0x7f)|0x80)
		n >>= 7
	}
	return append(parts, byte(n))
}

func grpcWebFrame(inner []byte) []byte {
	frame := make([]byte, 5+len(inner))
	frame[0] = 0
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(inner)))
	copy(frame[5:], inner)
	return frame
}

func readGRPCStatus(resp *curlcffi.Response) string {
	if resp.Headers != nil {
		if st := resp.Headers.Get("Grpc-Status"); st != "" {
			return st
		}
	}
	body := string(resp.Content)
	if i := strings.LastIndex(body, "grpc-status:"); i >= 0 {
		rest := body[i+12:]
		end := strings.IndexAny(rest, "\r\n")
		if end < 0 {
			end = len(rest)
		}
		return strings.TrimSpace(rest[:end])
	}
	return "0"
}

// aresGRPCStatus extracts gRPC status from an AresResponse (map-based headers).
func aresGRPCStatus(resp *curlcffi.AresResponse) string {
	if resp.Headers != nil {
		if st, ok := resp.Headers["Grpc-Status"]; ok && st != "" {
			return st
		}
		if st, ok := resp.Headers["grpc-status"]; ok && st != "" {
			return st
		}
	}
	body := string(resp.Content)
	if i := strings.LastIndex(body, "grpc-status:"); i >= 0 {
		rest := body[i+12:]
		end := strings.IndexAny(rest, "\r\n")
		if end < 0 {
			end = len(rest)
		}
		return strings.TrimSpace(rest[:end])
	}
	return "0"
}

// --- config scraping helpers ---

func scrapeStateTree(html string) string {
	re := regexp.MustCompile(`self\.__next_f\.push\(\[1,"(.*?)"\]\)`)
	chunks := re.FindAllStringSubmatch(html, -1)
	for _, ch := range chunks {
		if len(ch) < 2 {
			continue
		}
		decoded := strings.ReplaceAll(ch[1], `\"`, `"`)
		if !strings.Contains(decoded, "sign-up") {
			continue
		}
		idx := strings.Index(decoded, `"f":[[[`)
		if idx < 0 {
			continue
		}
		rest := decoded[idx:]
		end := strings.Index(rest, `]]]`)
		if end < 0 {
			continue
		}
		return rest[:end+3]
	}
	return ""
}

func unique(matches [][]string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, m := range matches {
		if len(m) >= 2 && !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// --- body builder ---

func BuildSignupBody(email, password, code, turnstileToken string) []byte {
	payload := []any{
		map[string]any{
			"emailValidationCode": code,
			"createUserAndSessionRequest": map[string]any{
				"email":              email,
				"givenName":          "James",
				"familyName":         "Smith",
				"clearTextPassword":  password,
				"tosAcceptedVersion": 1,
			},
			"turnstileToken":     turnstileToken,
			"conversionId":       randomUUID(),
			"castleRequestToken": "",
		},
		map[string]any{
			"client":      "$T",
			"meta":        "$undefined",
			"mutationKey": "$undefined",
		},
	}
	raw, _ := json.Marshal(payload)
	return raw
}

func randomUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]),
	)
}

func gzipDecompress(data []byte) []byte {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return data
	}
	defer gz.Close()
	out, err := io.ReadAll(gz)
	if err != nil {
		return data
	}
	return out
}

// jwtPayloadMap decodes JWT payload
func jwtPayloadMap(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	payload := parts[1]
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}
	raw, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		raw, err = base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return nil
		}
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

var _ io.Reader = (*bytes.Reader)(nil)
