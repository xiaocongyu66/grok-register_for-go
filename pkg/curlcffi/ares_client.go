package curlcffi

// ares_client.go — AresClient 整合进 curl_cffi_Ares
// 两阶段：1) chromedp 浏览器突破 CF  2) tls-client Session 高性能请求维持
// AresClient 内部使用 Session（curl_cffi 的 Session）作为 HTTP 引擎

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// AresResponse 是 AresClient 返回的响应对象，兼容 requests.Response 接口。
type AresResponse struct {
	StatusCode int
	Headers    map[string]string
	Cookies    map[string]string
	Content    []byte
	URL        string
}

func (r *AresResponse) Text() string {
	return string(r.Content)
}

func (r *AresResponse) JSON(v interface{}) error {
	return json.Unmarshal(r.Content, v)
}

func (r *AresResponse) String() string {
	return fmt.Sprintf("<AresResponse [%d]>", r.StatusCode)
}

// AresClient 是 CF-Ares 核心客户端。
// 处理 Cloudflare 挑战并提供 requests 兼容接口。
// 内部使用 Session（tls-client Chrome TLS 指纹）作为高性能 HTTP 引擎。
type AresClient struct {
	Headless     bool
	Fingerprint  string
	Proxy        string
	BrowserProxy string // 浏览器专用代理（带认证的 socks5 需先转成本地无认证中继）
	Timeout      int
	MaxRetries   int
	Debug        bool
	ChromePath   string
	Impersonate  BrowserType

	session      *Session
	browserEng   *BrowserEngine
	sessionMgr   *SessionManager
	fpMgr        *FingerprintManager
	initialized  bool
}

// NewAresClient 创建新的 AresClient。
func NewAresClient(opts ...func(*AresClient)) *AresClient {
	c := &AresClient{
		Headless:    true,
		Timeout:     30,
		MaxRetries:  3,
		Impersonate: Chrome131,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// --- AresClient 选项函数（加 Ares 前缀避免和 Session 选项冲突）---

func AresWithProxy(proxy string) func(*AresClient) {
	return func(c *AresClient) { c.Proxy = proxy }
}

func AresWithHeadless(headless bool) func(*AresClient) {
	return func(c *AresClient) { c.Headless = headless }
}

func AresWithTimeout(timeout int) func(*AresClient) {
	return func(c *AresClient) { c.Timeout = timeout }
}

func AresWithFingerprint(fp string) func(*AresClient) {
	return func(c *AresClient) { c.Fingerprint = fp }
}

func AresWithChromePath(path string) func(*AresClient) {
	return func(c *AresClient) { c.ChromePath = path }
}

func AresWithDebug(debug bool) func(*AresClient) {
	return func(c *AresClient) { c.Debug = debug }
}

func AresWithImpersonate(bt BrowserType) func(*AresClient) {
	return func(c *AresClient) { c.Impersonate = bt }
}

// AresWithBrowserProxy 设置浏览器专用代理（带认证 socks5 需先转成本地无认证中继）。
func AresWithBrowserProxy(p string) func(*AresClient) {
	return func(c *AresClient) { c.BrowserProxy = p }
}

// initialize 初始化 curl session（轻量，不需要浏览器）。
func (c *AresClient) initialize() error {
	if c.initialized {
		return nil
	}
	s, err := NewSession(
		WithImpersonate(c.Impersonate),
		WithProxy(c.Proxy),
		WithTimeout(time.Duration(c.Timeout)*time.Second),
	)
	if err != nil {
		return err
	}
	c.session = s
	c.sessionMgr = NewSessionManagerWithTTL(3600)
	c.fpMgr = NewFingerprintManager()
	c.initialized = true
	return nil
}

// initBrowserEngine 懒加载浏览器引擎——仅在检测到 CF JS 质询时调用。
func (c *AresClient) initBrowserEngine() {
	if c.browserEng != nil {
		return
	}
	// 浏览器优先用 BrowserProxy（已转成无认证本地中继），回退到 Proxy
	bp := c.BrowserProxy
	if bp == "" {
		bp = c.Proxy
	}
	c.browserEng = NewBrowserEngine(c.Headless, bp, c.Timeout, c.Fingerprint, c.ChromePath)
}

// handleCloudflare 用浏览器引擎处理 Cloudflare 质验。
func (c *AresClient) handleCloudflare(targetURL string) error {
	if c.browserEng == nil {
		c.initBrowserEngine()
	}
	if c.browserEng == nil {
		return NewAresError("browser engine not initialized")
	}

	if err := c.browserEng.Get(targetURL); err != nil {
		return NewAresError(fmt.Sprintf("browser navigate: %v", err))
	}

	if !c.browserEng.WaitForCloudflare() {
		return NewAresError("cloudflare challenge failed")
	}

	cookies := c.browserEng.GetCookies()
	headers := c.browserEng.GetHeaders()

	c.sessionMgr.Update(targetURL, cookies, headers)

	if c.session != nil {
		c.session.SetCookies(cookies)
		c.session.SetHeaders(headers)
	}
	return nil
}

// SolveChallenge 显式执行 Cloudflare 质询。
func (c *AresClient) SolveChallenge(targetURL string, maxRetries int) (*AresResponse, error) {
	if err := c.initialize(); err != nil {
		return nil, err
	}
	if maxRetries <= 0 {
		maxRetries = c.MaxRetries
	}

	var lastErr error
	for retry := 0; retry < maxRetries; retry++ {
		err := c.handleCloudflare(targetURL)
		if err != nil {
			lastErr = err
			if c.Debug {
				fmt.Printf("[cfares] challenge retry %d/%d: %v\n", retry+1, maxRetries, err)
			}
			time.Sleep(2 * time.Second)
			continue
		}

		resp, err := c.session.Get(targetURL)
		if err != nil {
			lastErr = err
			continue
		}

		if c.isCloudflareChallenge(resp.StatusCode, resp.Text()) {
			lastErr = NewAresError("response contains challenge page")
			continue
		}

		return toAresResponse(resp), nil
	}
	return nil, fmt.Errorf("%w: %v", ErrCloudflareChallengeFailed, lastErr)
}

// GetSessionInfo 返回当前会话信息。
func (c *AresClient) GetSessionInfo(targetURL string) map[string]interface{} {
	if err := c.initialize(); err != nil {
		return nil
	}
	if targetURL != "" {
		cookies := c.sessionMgr.GetCookies(targetURL)
		headers := c.sessionMgr.GetHeaders(targetURL)
		if cookies == nil && headers == nil {
			return nil
		}
		return map[string]interface{}{
			"cookies":   cookies,
			"headers":   headers,
			"timestamp": time.Now().Unix(),
			"url":       targetURL,
		}
	}
	result := make(map[string]interface{})
	for domain, entry := range c.sessionMgr.sessions {
		result[domain] = map[string]interface{}{
			"cookies":   entry.Cookies,
			"headers":   entry.Headers,
			"timestamp": entry.Timestamp,
		}
	}
	return result
}

// SetSessionInfo 手动设置会话信息。
func (c *AresClient) SetSessionInfo(info map[string]interface{}, targetURL string) error {
	if err := c.initialize(); err != nil {
		return err
	}
	if targetURL == "" {
		if u, ok := info["url"].(string); ok {
			targetURL = u
		}
	}
	if targetURL == "" {
		return fmt.Errorf("must provide url")
	}
	cookies, _ := info["cookies"].(map[string]string)
	headers, _ := info["headers"].(map[string]string)
	c.sessionMgr.Update(targetURL, cookies, headers)
	if c.session != nil {
		c.session.SetCookies(cookies)
		c.session.SetHeaders(headers)
	}
	return nil
}

// SaveSession 保存会话到文件。
func (c *AresClient) SaveSession(filePath string, targetURL string) error {
	info := c.GetSessionInfo(targetURL)
	if dir := filePath[:strings.LastIndex(filePath, "/")]; dir != "" {
		os.MkdirAll(dir, 0755)
	}
	data, _ := json.MarshalIndent(info, "", "  ")
	return os.WriteFile(filePath, data, 0644)
}

// LoadSession 从文件加载会话。
func (c *AresClient) LoadSession(filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	var info map[string]interface{}
	if err := json.Unmarshal(data, &info); err != nil {
		return err
	}
	if _, ok := info["cookies"]; ok {
		return c.SetSessionInfo(info, "")
	}
	for domain, v := range info {
		if entry, ok := v.(map[string]interface{}); ok {
			c.SetSessionInfo(entry, "https://"+domain)
		}
	}
	return nil
}

// isCloudflareChallenge 检测是否为 CF 质询页面。
func (c *AresClient) isCloudflareChallenge(statusCode int, text string) bool {
	if statusCode != 403 && statusCode != 503 {
		return false
	}
	textLower := strings.ToLower(text)
	markers := []string{
		"cf-browser-verification",
		"cf-im-under-attack",
		"challenge platform",
		"just a moment",
		"turnstile",
		"captcha",
	}
	for _, m := range markers {
		if strings.Contains(textLower, m) {
			return true
		}
	}
	return false
}

// request 内部请求方法，自动处理 CF。
func (c *AresClient) request(method, targetURL string, params map[string]string, data []byte, headers map[string]string) (*AresResponse, error) {
	if err := c.initialize(); err != nil {
		return nil, err
	}
	if c.session == nil {
		return nil, NewAresError("session not initialized")
	}

	// 有有效会话直接请求
	if c.sessionMgr.HasValidSession(targetURL) {
		resp, err := c.session.Request(&Request{
			Method: method, URL: targetURL, Params: params, Data: data, Headers: headers,
		})
		if err != nil {
			return nil, err
		}
		return toAresResponse(resp), nil
	}

	// 首次尝试：直接用 tls-client
	resp, err := c.session.Request(&Request{
		Method: method, URL: targetURL, Params: params, Data: data, Headers: headers,
	})
	if err != nil {
		return nil, err
	}

	// 检测 CF 质询
	if c.isCloudflareChallenge(resp.StatusCode, resp.Text()) {
		if c.Debug {
			fmt.Printf("[cfares] CF challenge detected (%d), launching browser...\n", resp.StatusCode)
		}
		if err := c.handleCloudflare(targetURL); err != nil {
			return nil, err
		}
		// 用更新后的 cookies 重试
		resp, err = c.session.Request(&Request{
			Method: method, URL: targetURL, Params: params, Data: data, Headers: headers,
		})
		if err != nil {
			return nil, err
		}
	}

	return toAresResponse(resp), nil
}

// Get 发送 GET 请求。
func (c *AresClient) Get(targetURL string, params map[string]string, headers map[string]string) (*AresResponse, error) {
	return c.request("GET", targetURL, params, nil, headers)
}

// Post 发送 POST 请求。
func (c *AresClient) Post(targetURL string, data []byte, headers map[string]string) (*AresResponse, error) {
	return c.request("POST", targetURL, nil, data, headers)
}

// PostJSON 发送 JSON POST 请求。
func (c *AresClient) PostJSON(targetURL string, body interface{}, headers map[string]string) (*AresResponse, error) {
	jsonData, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	if headers == nil {
		headers = make(map[string]string)
	}
	headers["Content-Type"] = "application/json"
	return c.request("POST", targetURL, nil, jsonData, headers)
}

// Put 发送 PUT 请求。
func (c *AresClient) Put(targetURL string, data []byte, headers map[string]string) (*AresResponse, error) {
	return c.request("PUT", targetURL, nil, data, headers)
}

// Delete 发送 DELETE 请求。
func (c *AresClient) Delete(targetURL string, headers map[string]string) (*AresResponse, error) {
	return c.request("DELETE", targetURL, nil, nil, headers)
}

// Cookies 返回当前 cookies。
func (c *AresClient) Cookies() map[string]string {
	if c.session != nil {
		return c.session.GetCookies()
	}
	return nil
}

// Session 返回内部 session(用于访问 jar cookies 等)。
func (c *AresClient) Session() *Session {
	return c.session
}

// BrowserCookies 返回浏览器当前页面的所有 cookie(包括 HttpOnly)。
// 用于在浏览器访问 set-cookie URL 后提取 sso cookie。
// 注意:浏览器必须先 navigate 到相关页面,cookie 才会在 jar 里。
func (c *AresClient) BrowserCookies() map[string]string {
	if c.browserEng == nil {
		return map[string]string{}
	}
	return c.browserEng.GetCookies()
}

// Navigate 让浏览器访问 URL(不触发 CF 质询重试,只导航一次)。
// 用于在已建立会话后访问 set-cookie URL。
func (c *AresClient) Navigate(url string) error {
	if c.browserEng == nil {
		c.initBrowserEngine()
	}
	if c.browserEng == nil {
		return NewAresError("browser engine not initialized")
	}
	if err := c.browserEng.Get(url); err != nil {
		return NewAresError(fmt.Sprintf("browser navigate: %v", err))
	}
	return nil
}

// Headers 返回当前 headers。
func (c *AresClient) Headers() map[string]string {
	if c.session != nil {
		return c.session.headers
	}
	return nil
}

// Close 关闭所有资源。
func (c *AresClient) Close() {
	if c.browserEng != nil {
		c.browserEng.Close()
	}
	if c.session != nil {
		c.session.Close()
	}
	c.initialized = false
}

// toAresResponse 将 curlcffi.Response 转换为 AresResponse
func toAresResponse(resp *Response) *AresResponse {
	headers := make(map[string]string)
	for k := range resp.Headers {
		for _, v := range resp.Headers.Values(k) {
			headers[k] = v
		}
	}
	cookies := make(map[string]string)
	for _, ck := range resp.Cookies {
		cookies[ck.Name] = ck.Value
	}
	return &AresResponse{
		StatusCode: resp.StatusCode,
		Headers:    headers,
		Cookies:    cookies,
		Content:    resp.Content,
		URL:        resp.URL,
	}
}
