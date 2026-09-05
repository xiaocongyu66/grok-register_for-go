// Package obscura 提供 obscura headless 浏览器的 Go CDP 客户端。
// obscura 是 Rust 写的 headless 浏览器(https://github.com/h4ckf0r0day/obscura),
// 支持 CDP(Chrome DevTools Protocol),可以用标准 CDP 客户端连接。
//
// 本包不依赖 Node.js/playwright driver,直接用 Go WebSocket 连接 obscura 的 CDP 端口。
// 支持所有 CDP 命令(navigate/evaluate/click/screenshot/cookie 等)。
package obscura

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/websocket"
)

// Client obscura CDP 客户端
type Client struct {
	ws        *websocket.Conn
	mu        sync.Mutex
	nextID    int64
	pending   map[int64]chan json.RawMessage
	closed    bool
	targetID  string // 当前页面 ID
	eventCallback EventCallback // CDP 事件回调
	sessionID string // 当前 session ID(flatten CDP 用)
}

// NewClient 连接 obscura 的 CDP 端口(browser-level)
// addr: obscura serve 的地址,如 "127.0.0.1:9222"
// 自动创建一个新页面并 attach,后续 Evaluate/Navigate 在该页面上执行
func NewClient(addr string) (*Client, error) {
	// 连接 browser-level CDP
	wsURL := fmt.Sprintf("ws://%s/devtools/browser", addr)
	ws, err := websocket.Dial(wsURL, "", fmt.Sprintf("http://%s", addr))
	if err != nil {
		return nil, fmt.Errorf("ws dial %s: %w", wsURL, err)
	}

	c := &Client{
		ws:      ws,
		pending: make(map[int64]chan json.RawMessage),
	}

	// 启动消息读取循环
	go c.readLoop()

	// 创建新页面
	result, err := c.Send("Target.createTarget", map[string]interface{}{"url": "about:blank"})
	if err != nil {
		return nil, fmt.Errorf("createTarget: %w", err)
	}

	var targetResp struct {
		TargetID string `json:"targetId"`
	}
	if err := json.Unmarshal(result, &targetResp); err != nil {
		return nil, fmt.Errorf("parse targetId: %w", err)
	}
	c.targetID = targetResp.TargetID

	// Attach 到新页面
	result, err = c.Send("Target.attachToTarget", map[string]interface{}{
		"targetId": c.targetID,
		"flatten":  true,
	})
	if err != nil {
		return nil, fmt.Errorf("attachToTarget: %w", err)
	}

	var attachResp struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(result, &attachResp); err != nil {
		return nil, fmt.Errorf("parse sessionId: %w", err)
	}
	c.sessionID = attachResp.SessionID

	// 启用 Runtime + Page
	c.Send("Runtime.enable", nil)
	c.Send("Page.enable", nil)

	return c, nil
}

// readLoop 读取 CDP 消息,分发到 pending 请求
func (c *Client) readLoop() {
	for {
		var msg string
		if err := websocket.Message.Receive(c.ws, &msg); err != nil {
			if !c.isClosed() {
				fmt.Printf("[obscura] ws read error: %v\n", err)
			}
			return
		}

		// 解析消息
		var envelope struct {
			ID     json.Number       `json:"id"`
			Method string            `json:"method"`
			Result json.RawMessage   `json:"result"`
			Error  json.RawMessage   `json:"error"`
		}
		if err := json.Unmarshal([]byte(msg), &envelope); err != nil {
			continue
		}

		// 只有有 id 的消息才是同步响应(事件没有 id)
		if envelope.ID != "" {
			// 解析 id 为 int64
			id, err := envelope.ID.Int64()
			if err != nil || id == 0 {
				continue
			}
			c.mu.Lock()
			ch, ok := c.pending[id]
			if ok {
				delete(c.pending, id)
			}
			c.mu.Unlock()
			if ok {
				if len(envelope.Error) > 0 {
					ch <- envelope.Error
				} else {
					ch <- envelope.Result
				}
			}
		}
		// 事件(envelope.Method != "" 且没有 id)转发给 eventCallback
		if envelope.Method != "" && c.eventCallback != nil {
			c.eventCallback(envelope.Method, envelope.Result)
		}
	}
}

// Send 发送 CDP 命令,等待响应
// sessionId 在请求顶层(flatten CDP),不在 params 里
func (c *Client) Send(method string, params map[string]interface{}) (json.RawMessage, error) {
	if c.isClosed() {
		return nil, fmt.Errorf("client closed")
	}

	id := atomic.AddInt64(&c.nextID, 1)
	ch := make(chan json.RawMessage, 1)

	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	// 构造请求: sessionId 在顶层(flatten CDP)
	req := map[string]interface{}{"id": id, "method": method, "params": params}
	if c.sessionID != "" {
		req["sessionId"] = c.sessionID
	}
	data, _ := json.Marshal(req)
	if err := websocket.Message.Send(c.ws, string(data)); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("ws send: %w", err)
	}

	select {
	case result := <-ch:
		// 检查是否是 error
		var errCheck struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(result, &errCheck); err == nil && errCheck.Code != 0 {
			return nil, fmt.Errorf("CDP error: %s", errCheck.Message)
		}
		return result, nil
	case <-time.After(120 * time.Second):
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("CDP timeout: %s", method)
	}
}

// Navigate 导航到 URL
func (c *Client) Navigate(url string) error {
	_, err := c.Send("Page.navigate", map[string]interface{}{"url": url})
	return err
}

// Evaluate 执行 JS,返回结果字符串
func (c *Client) Evaluate(expression string) (string, error) {
	result, err := c.Send("Runtime.evaluate", map[string]interface{}{
		"expression":    expression,
		"returnByValue": true,
		"awaitPromise":  true,
	})
	if err != nil {
		return "", err
	}

	var evalResult struct {
		Result struct {
			Type  string      `json:"type"`
			Value interface{} `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(result, &evalResult); err != nil {
		return string(result), nil
	}
	if evalResult.Result.Value != nil {
		return fmt.Sprintf("%v", evalResult.Result.Value), nil
	}
	return "", nil
}

// WaitForSelector 等待 CSS 选择器出现
func (c *Client) WaitForSelector(selector string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		result, err := c.Evaluate(fmt.Sprintf(`document.querySelector('%s') ? 'found' : 'not found'`, selector))
		if err == nil && result == "found" {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", selector)
}

// Screenshot 截图(返回 base64 PNG)
func (c *Client) Screenshot() (string, error) {
	result, err := c.Send("Page.captureScreenshot", map[string]interface{}{
		"format": "png",
	})
	if err != nil {
		return "", err
	}
	var ss struct {
		Data string `json:"data"`
	}
	json.Unmarshal(result, &ss)
	return ss.Data, nil
}

// GetCookies 获取所有 cookies
func (c *Client) GetCookies() (string, error) {
	result, err := c.Send("Network.getCookies", nil)
	if err != nil {
		return "", err
	}
	return string(result), nil
}

// Close 关闭客户端
func (c *Client) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return c.ws.Close()
}

func (c *Client) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// StartObscura 启动 obscura serve 进程(默认端口 9222)。
// 兼容旧调用方:返回 *exec.Cmd,调用者用 cmd.Process.Kill() 关闭。
// 新代码应使用 StartObscuraCmd 拿到端口。
func StartObscura(ctx context.Context, proxy, ua string) (*exec.Cmd, error) {
	cmd, _, err := StartObscuraCmd(ctx, proxy, ua, 9222)
	return cmd, err
}

// StartObscuraCmd 启动 obscura serve 并返回 *exec.Cmd 和实际端口。
// 调用者用 cmd.Process.Kill() 关闭进程。
//
// 内部会:
//   - 用 --stealth 开启指纹注入 + CDP hardening
//   - 用 --proxy 把 obscura 的协议级代理设到给定的 proxy URL
//   - 用 --user-agent 覆盖 UA(和 curlcffi 的 UA 对齐)
//   - 用 --allow-private-network 允许访问 RFC1918
//   - 轮询 /json/version 直到 CDP 就绪(最多 15s)
//   - 若进程提前退出,Wait4 拿到退出码,立即返回错误
// StartObscuraCmdWithOpts starts obscura serve with full options.
// Pass webglRust=true to enable the Rust (PortableGL) WebGL backend
// for real software rendering.
// StartObscuraCmdWithOpts starts obscura serve with full options.
// The Rust WebGL backend (real software rendering) is enabled by default.
// Pass useJsStub=true to use the JS stub instead (faster, no real rendering).
func StartObscuraCmdWithOpts(ctx context.Context, proxy, ua string, port int, useJsStub bool) (*exec.Cmd, int, error) {
	args := []string{"serve", "--port", fmt.Sprintf("%d", port), "--allow-private-network"}
	if proxy != "" {
		args = append(args, "--proxy", proxy)
	}
	args = append(args, "--stealth")
	if ua != "" {
		args = append(args, "--user-agent", ua)
	}
	if useJsStub {
		args = append(args, "--no-webgl-rust")
	}

		// 时区跟随代理出口 IP 的真实地区(Camoufox geoip 流程):通过代理查
	// ip-api.com 拿 IANA timezone,Date/Intl 与 IP 地理位置一致(CF 必查)。
	env := os.Environ()
	if os.Getenv("OBSCURA_TIMEZONE") == "" && os.Getenv("TZ") == "" {
		geo := ResolveProxyGeo(proxy, "Asia/Shanghai")
		env = append(env, "OBSCURA_TIMEZONE="+geo.Timezone, "TZ="+geo.Timezone,
			"OBSCURA_LOCALE="+geo.Locale)
	} else {
		env = append(env)
	}
cmd := exec.CommandContext(ctx, "obscura", args...)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Redirect obscura stdout/stderr to a log file so we can inspect what
	// the browser is doing (frame loads, script execution, errors).
	logFile, _ := os.Create("/tmp/obscura-cf.log")
	if logFile != nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}
	if err := cmd.Start(); err != nil {
		return nil, 0, fmt.Errorf("start obscura: %w", err)
	}

	// 等待 CDP 端口就绪
	for i := 0; i < 30; i++ {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version", port))
		if err == nil {
			resp.Body.Close()
			return cmd, port, nil
		}
		// 进程提前退出就不要继续等
		if cmd.Process != nil {
			var status syscall.WaitStatus
			pid, err := syscall.Wait4(cmd.Process.Pid, &status, syscall.WNOHANG, nil)
			if err == nil && pid == cmd.Process.Pid && status.Exited() {
				return nil, 0, fmt.Errorf("obscura exited early (status=%d)", status.ExitStatus())
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	cmd.Process.Kill()
	return nil, 0, fmt.Errorf("obscura CDP not ready after 15s")
}

// StartObscuraCmd starts obscura serve and returns (cmd, port, err).
// Rust WebGL backend is enabled by default.
func StartObscuraCmd(ctx context.Context, proxy, ua string, port int) (*exec.Cmd, int, error) {
	return StartObscuraCmdWithOpts(ctx, proxy, ua, port, false)
}

// ─── Multi-Instance Pool ─────────────────────────────────────────────────────

// InstancePool manages multiple obscura instances (one per worker).
// Each instance has independent V8 runtime, fingerprint, proxy, and storage.
type InstancePool struct {
	mu        sync.Mutex
	instances map[string]*Instance
}

// Instance represents a single obscura process with its own fingerprint.
type Instance struct {
	Client   *Client
	Cmd      *exec.Cmd
	Port     int
	Finger   *FingerprintConfig
}

// FingerprintConfig for obscura instance
type FingerprintConfig struct {
	Seed        int64  // Deterministic seed for fingerprint
	UserAgent   string
	Proxy       string
	StorageDir  string
}

// NewInstancePool creates a new pool
func NewInstancePool() *InstancePool {
	return &InstancePool{
		instances: make(map[string]*Instance),
	}
}

// StartInstance starts a new obscura instance with the given config
func (p *InstancePool) StartInstance(id string, cfg FingerprintConfig) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if existing, ok := p.instances[id]; ok && existing.Client != nil {
		return nil // already running
	}

	// Allocate a port
	port := 9300 + len(p.instances)

	ctx, cancel := context.WithCancel(context.Background())
	_ = cancel

	args := []string{"serve", "--port", fmt.Sprintf("%d", port), "--allow-private-network"}
	if cfg.Proxy != "" {
		args = append(args, "--proxy", cfg.Proxy)
	}
	args = append(args, "--stealth")
	if cfg.UserAgent != "" {
		args = append(args, "--user-agent", cfg.UserAgent)
	}
	if cfg.StorageDir != "" {
		args = append(args, "--storage-dir", cfg.StorageDir)
	}

	cmd := exec.CommandContext(ctx, "obscura", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start obscura: %w", err)
	}

	// Wait for CDP port
	for i := 0; i < 30; i++ {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version", port))
		if err == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Connect to CDP
	client, err := NewClient(fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		cmd.Process.Kill()
		return fmt.Errorf("connect CDP: %w", err)
	}

	p.instances[id] = &Instance{
		Client: client,
		Cmd:    cmd,
		Port:   port,
		Finger: &cfg,
	}
	return nil
}

// GetInstance returns a running instance by ID
func (p *InstancePool) GetInstance(id string) (*Instance, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	inst, ok := p.instances[id]
	if !ok {
		return nil, fmt.Errorf("instance %s not found", id)
	}
	return inst, nil
}

// StopInstance stops a specific instance
func (p *InstancePool) StopInstance(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if inst, ok := p.instances[id]; ok {
		inst.Client.Close()
		if inst.Cmd != nil && inst.Cmd.Process != nil {
			inst.Cmd.Process.Kill()
		}
		delete(p.instances, id)
	}
}

// StopAll stops all instances
func (p *InstancePool) StopAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, inst := range p.instances {
		inst.Client.Close()
		if inst.Cmd != nil && inst.Cmd.Process != nil {
			inst.Cmd.Process.Kill()
		}
		delete(p.instances, id)
	}
}

// ─── Network Capture ─────────────────────────────────────────────────────────

// NetworkRequest represents a captured HTTP request
type NetworkRequest struct {
	URL      string
	Method   string
	Headers   map[string]string
	PostData string
}

// NetworkResponse represents a captured HTTP response
type NetworkResponse struct {
	URL        string
	Status     int
	Headers    map[string]string
	MimeType   string
	BodySize   int64
}

// EnableNetworkCapture enables CDP Network domain to capture all requests/responses
func (c *Client) EnableNetworkCapture() error {
	_, err := c.Send("Network.enable", nil)
	return err
}

// GetCapturedRequests returns all captured requests (via CDP events)
// Note: requires EnableNetworkCapture first, then navigate
func (c *Client) GetCapturedRequests() ([]NetworkRequest, error) {
	// CDP events are received in readLoop but not stored yet.
	// This is a placeholder for the event accumulation pattern.
	return nil, nil
}

// ─── Fingerprint Injection ───────────────────────────────────────────────────

// InjectFingerprint injects a fingerprint script via Page.addScriptToEvaluateOnNewDocument
// This runs before any page JavaScript, ensuring consistent identity.
func (c *Client) InjectFingerprint(script string) error {
	_, err := c.Send("Page.addScriptToEvaluateOnNewDocument", map[string]interface{}{
		"source": script,
	})
	return err
}

// InjectUAProfile injects a UA + sec-ch-ua + platform override that runs
// before any page script. Use this to pin the session-level UA so the
// browser and curlcffi report the same identity. The script overrides:
//   - navigator.userAgent
//   - navigator.userAgentData (brands, platform, mobile)
//   - navigator.platform
//   - sec-ch-ua / sec-ch-ua-platform / sec-ch-ua-mobile request headers
//     (the request-header side is set via Network.setExtraHTTPHeaders)
func (c *Client) InjectUAProfile(userAgent, chua, chuaPlatform string) error {
	// 1. JS-side override via addScriptToEvaluateOnNewDocument.
	script := fmt.Sprintf(`(function(){
  var ua = %s;
  var chua = %s;
  var plat = %s;
  try { Object.defineProperty(navigator, 'userAgent', {get: function(){return ua;}, configurable: false, enumerable: true}); } catch(_){}
  try { Object.defineProperty(navigator, 'platform', {get: function(){return plat === 'Linux' ? 'Linux x86_64' : plat === 'macOS' ? 'MacIntel' : 'Win32';}, configurable: false, enumerable: true}); } catch(_){}
  // Parse chua like '"Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"'
  var brands = [];
  try {
    chua.split(',').forEach(function(part){
      var m = part.match(/"([^"]+)";v="([^"]+)"/);
      if (m) brands.push({brand: m[1], version: m[2]});
    });
  } catch(_){}
  if (brands.length && navigator.userAgentData) {
    try { Object.defineProperty(navigator.userAgentData, 'brands', {get: function(){return brands;}, configurable: false}); } catch(_){}
    try { Object.defineProperty(navigator.userAgentData, 'platform', {get: function(){return plat;}, configurable: false}); } catch(_){}
  }
  // Sec-CH-UA headers are set on the request side via Network.setExtraHTTPHeaders.
})();
`, quoteJS(userAgent), quoteJS(chua), quoteJS(chuaPlatform))
	if err := c.InjectFingerprint(script); err != nil {
		return err
	}
	// 2. Request-side headers.
	mobile := "false"
	if chuaPlatform == "Android" {
		mobile = "true"
	}
	headers := map[string]interface{}{
		"User-Agent":         userAgent,
		"sec-ch-ua":           chua,
		"sec-ch-ua-mobile":   mobile,
		"sec-ch-ua-platform": fmt.Sprintf("%q", chuaPlatform),
	}
	_, err := c.Send("Network.setExtraHTTPHeaders", map[string]interface{}{
		"headers": headers,
	})
	return err
}

// quoteJS wraps a string in double quotes with JS-safe escapes.
func quoteJS(s string) string {
	r := strings.ReplaceAll(s, `\`, `\\`)
	r = strings.ReplaceAll(r, `"`, `\"`)
	r = strings.ReplaceAll(r, "\n", `\n`)
	r = strings.ReplaceAll(r, "\r", `\r`)
	r = strings.ReplaceAll(r, "\t", `\t`)
	return `"` + r + `"`
}

// InjectCDPHardening injects CDP leak protection script
func (c *Client) InjectCDPHardening() error {
	hardeningScript := `(function(){
		var props = Object.getOwnPropertyNames(window);
		for (var i = 0; i < props.length; i++) {
			var p = props[i];
			if (/^cdc_|^_cdc_|^__webdriver|^__selenium|^__driver|^\$chrome_|^__playwright|^__puppeteer/.test(p)) {
				try { delete window[p]; } catch(e) {}
			}
		}
		Object.defineProperty(Navigator.prototype, 'webdriver', {get: function() { return false; }, configurable: true});
		try { delete window.domAutomation; } catch(e) {}
		try { delete window.domAutomationController; } catch(e) {}
	})();`
	return c.InjectFingerprint(hardeningScript)
}

// ─── CDP Event Capture ───────────────────────────────────────────────────────

// EventCallback is called when a CDP event is received
type EventCallback func(method string, params json.RawMessage)

// SetEventHandler sets a callback for CDP events
func (c *Client) SetEventHandler(cb EventCallback) {
	c.eventCallback = cb
}

// CaptureNetworkRequests starts capturing all network requests/responses
// Returns a channel that receives captured requests
func (c *Client) CaptureNetworkRequests() chan NetworkRequest {
	ch := make(chan NetworkRequest, 100)
	c.SetEventHandler(func(method string, params json.RawMessage) {
		if method == "Network.requestWillBeSent" {
			var req struct {
				Request struct {
					URL      string            `json:"url"`
					Method   string            `json:"method"`
					Headers  map[string]string `json:"headers"`
					PostData string            `json:"postData"`
				} `json:"request"`
			}
			if json.Unmarshal(params, &req) == nil {
				select {
				case ch <- NetworkRequest{
					URL:      req.Request.URL,
					Method:   req.Request.Method,
					Headers:  req.Request.Headers,
					PostData: req.Request.PostData,
				}:
				default:
				}
			}
		}
	})
	c.Send("Network.enable", nil)
	return ch
}

// CaptureConsoleMessages starts capturing console.log messages
// Returns a channel that receives console messages
func (c *Client) CaptureConsoleMessages() chan string {
	ch := make(chan string, 50)
	c.SetEventHandler(func(method string, params json.RawMessage) {
		if method == "Runtime.consoleAPICalled" {
			var msg struct {
				Args []struct {
					Value string `json:"value"`
					Type  string `json:"type"`
				} `json:"args"`
			}
			if json.Unmarshal(params, &msg) == nil {
				var parts []string
				for _, arg := range msg.Args {
					if arg.Value != "" {
						parts = append(parts, arg.Value)
					}
				}
				if len(parts) > 0 {
					select {
					case ch <- strings.Join(parts, " "):
					default:
					}
				}
			}
		}
	})
	c.Send("Runtime.enable", nil)
	return ch
}

// CaptureErrors starts capturing JS errors
// Returns a channel that receives error messages
func (c *Client) CaptureErrors() chan string {
	ch := make(chan string, 50)
	c.SetEventHandler(func(method string, params json.RawMessage) {
		if method == "Runtime.exceptionThrown" {
			var exc struct {
				ExceptionDetails struct {
					Text      string `json:"text"`
					Exception  struct {
						Description string `json:"description"`
					} `json:"exception"`
				} `json:"exceptionDetails"`
			}
			if json.Unmarshal(params, &exc) == nil {
				msg := exc.ExceptionDetails.Text
				if exc.ExceptionDetails.Exception.Description != "" {
					msg = exc.ExceptionDetails.Exception.Description
				}
				if msg != "" {
					select {
					case ch <- msg:
					default:
					}
				}
			}
		}
	})
	c.Send("Runtime.enable", nil)
	return ch
}

// ExportHAR exports captured network data as HAR (HTTP Archive) JSON
func (c *Client) ExportHAR() (string, error) {
	// Placeholder - would accumulate from CaptureNetworkRequests
	return `{"log":{"version":"1.2","entries":[]}}`, nil
}

// ─── Screenshot ──────────────────────────────────────────────────────────────

// ScreenshotFull takes a full-page screenshot
func (c *Client) ScreenshotFull() (string, error) {
	result, err := c.Send("Page.captureScreenshot", map[string]interface{}{
		"format":  "png",
		"quality":  100,
		"fromSurface": true,
		"captureBeyondViewport": true,
	})
	if err != nil {
		return "", err
	}
	var ss struct {
		Data string `json:"data"`
	}
	json.Unmarshal(result, &ss)
	return ss.Data, nil
}

// ─── Cookies ─────────────────────────────────────────────────────────────────

// GetAllCookies returns all cookies
func (c *Client) GetAllCookies() (string, error) {
	result, err := c.Send("Network.getCookies", nil)
	if err != nil {
		return "", err
	}
	return string(result), nil
}

// SetCookie sets a cookie
func (c *Client) SetCookie(name, value, domain string) error {
	_, err := c.Send("Network.setCookie", map[string]interface{}{
		"name":   name,
		"value":  value,
		"domain": domain,
	})
	return err
}

// ─── DOM ─────────────────────────────────────────────────────────────────────

// GetHTML returns the page HTML
func (c *Client) GetHTML() (string, error) {
	r, err := c.Evaluate("document.documentElement.outerHTML")
	if err != nil {
		return "", err
	}
	return r, nil
}

// GetText returns the page text content
func (c *Client) GetText() (string, error) {
	r, err := c.Evaluate("document.body.innerText")
	if err != nil {
		return "", err
	}
	return r, nil
}

// Click clicks an element by CSS selector
func (c *Client) Click(selector string) error {
	_, err := c.Evaluate(fmt.Sprintf(`document.querySelector('%s').click()`, selector))
	return err
}

// Type types text into an element
func (c *Client) Type(selector, text string) error {
	escaped := strings.ReplaceAll(text, "'", "\\'")
	_, err := c.Evaluate(fmt.Sprintf(`document.querySelector('%s').value = '%s'`, selector, escaped))
	return err
}

// Scroll scrolls the page
func (c *Client) Scroll(x, y int) error {
	_, err := c.Evaluate(fmt.Sprintf(`window.scrollTo(%d, %d)`, x, y))
	return err
}

// ─── CF Turnstile Debugging API ──────────────────────────────────────────────
//
// These methods let you debug Cloudflare Turnstile (and similar widget-based
// challenges) from Go. They install hooks that record every DOM operation
// the widget does (createElement, attachShadow, appendChild, querySelector
// null results, iframe.src/srcdoc, input.setAttribute, postMessage traffic),
// then return the captured hooks as JSON.
//
// Usage:
//   client.InstallWidgetHooks()
//   client.Navigate("http://...")
//   // ... wait for widget to run ...
//   hooks := client.GetWidgetHooks()
//   shadowInfo := client.GetShadowRootInfo()

// InstallWidgetHooks installs DOM hooks that record widget behavior.
// The hooks are injected via Page.addScriptToEvaluateOnNewDocument so they
// run before any page script. Call before Navigate.
func (c *Client) InstallWidgetHooks() error {
	hookScript := `(function(){
  if (window.__hooks) return;
  window.__hooks = [];
  window.__shadowRoots = [];
  // hook createElement
  var origCreateElement = document.createElement.bind(document);
  document.createElement = function(tag) {
    var el = origCreateElement(tag);
    var tl = tag.toLowerCase();
    if (tl === 'iframe' || tl === 'input') {
      window.__hooks.push({type:'createElement', tag: tl, time: Date.now()});
    }
    if (tl === 'input') {
      var origSA = el.setAttribute.bind(el);
      el.setAttribute = function(name, val) {
        window.__hooks.push({type:'input.setAttribute', name: name, value: String(val).substring(0,80)});
        return origSA(name, val);
      };
    }
    if (tl === 'iframe') {
      var origSA2 = el.setAttribute.bind(el);
      el.setAttribute = function(name, val) {
        if (name === 'src' || name === 'srcdoc') {
          window.__hooks.push({type:'iframe.setAttribute', name: name, value: String(val).substring(0,120)});
        }
        return origSA2(name, val);
      };
    }
    return el;
  };
  // hook querySelector / querySelectorAll (record null/empty results)
  var origQS = document.querySelector.bind(document);
  document.querySelector = function(sel) {
    var r = origQS(sel);
    if (!r) window.__hooks.push({type:'querySelector null', sel: sel, time: Date.now()});
    return r;
  };
  var origQSA = document.querySelectorAll.bind(document);
  document.querySelectorAll = function(sel) {
    var r = origQSA(sel);
    if (r.length === 0) window.__hooks.push({type:'querySelectorAll empty', sel: sel, time: Date.now()});
    return r;
  };
  // hook attachShadow
  var origAttachShadow = Element.prototype.attachShadow;
  Element.prototype.attachShadow = function(opts) {
    var tag = (this.tagName || '').toLowerCase();
    try {
      var r = origAttachShadow.call(this, opts);
      window.__shadowRoots.push({host: tag, root: r, hostElement: this});
      window.__hooks.push({type:'attachShadow OK', tag: tag, mode: opts.mode, time: Date.now()});
      return r;
    } catch(e) {
      window.__hooks.push({type:'attachShadow ERR', tag: tag, err: e.name, time: Date.now()});
      throw e;
    }
  };
  // hook appendChild (iframe/input/shadowroot)
  var origAppendChild = Node.prototype.appendChild;
  Node.prototype.appendChild = function(child) {
    var parentTag = (this.tagName || this.constructor.name || '').toLowerCase();
    var childTag = child && child.tagName ? child.tagName.toLowerCase() : 'unknown';
    if (childTag === 'iframe' || childTag === 'input' || parentTag === 'shadowroot' || this._host) {
      window.__hooks.push({type:'appendChild', parent: parentTag, child: childTag, time: Date.now()});
    }
    try {
      return origAppendChild.call(this, child);
    } catch(e) {
      window.__hooks.push({type:'appendChild ERR', parent: parentTag, child: childTag, err: e.name, time: Date.now()});
      throw e;
    }
  };
  // hook console
  var origLog = console.log;
  console.log = function() {
    var args = Array.prototype.slice.call(arguments);
    window.__hooks.push({type:'console.log', args: args.map(function(a){return String(a).substring(0,200);}).join(' ')});
    origLog.apply(console, arguments);
  };
  var origErr = console.error;
  console.error = function() {
    var args = Array.prototype.slice.call(arguments);
    window.__hooks.push({type:'console.error', args: args.map(function(a){return String(a).substring(0,200);}).join(' ')});
    origErr.apply(console, arguments);
  };
  // hook fetch
  var origFetch = globalThis.fetch;
  globalThis.fetch = function(input, init) {
    var url = typeof input === 'string' ? input : (input && input.url);
    window.__hooks.push({type:'fetch', url: String(url).substring(0,200)});
    return origFetch.apply(this, arguments);
  };
})();
`
	return c.InjectFingerprint(hookScript)
}

// GetWidgetHooks returns all captured hooks as JSON.
func (c *Client) GetWidgetHooks() (string, error) {
	return c.Evaluate("JSON.stringify(window.__hooks || [])")
}

// GetShadowRootInfo returns the contents of all shadow roots the page
// created (including closed ones, since we intercepted attachShadow).
func (c *Client) GetShadowRootInfo() (string, error) {
	script := `(function(){
  var out = [];
  var roots = window.__shadowRoots || [];
  for (var i=0;i<roots.length;i++) {
    var r = roots[i].root;
    try {
      out.push({host: roots[i].host, childCount: r.childNodes.length, html: r.innerHTML.substring(0, 800)});
    } catch(e) {
      out.push({host: roots[i].host, err: e.message});
    }
  }
  return JSON.stringify(out);
})()`
	return c.Evaluate(script)
}

// GetTurnstileState returns the Turnstile widget state: token, err, rendered.
func (c *Client) GetTurnstileState() (string, error) {
	script := `(function(){
  return JSON.stringify({
    token: window.__ts_token || '',
    err: window.__ts_err || '',
    rendered: window.__ts_rendered || false
  });
})()`
	return c.Evaluate(script)
}

// WaitForTurnstileToken polls for the Turnstile token with a timeout.
// Returns the token if obtained, or an error on timeout.
func (c *Client) WaitForTurnstileToken(timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		token, _ := c.Evaluate("window.__ts_token || ''")
		if len(token) > 20 {
			return token, nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return "", fmt.Errorf("turnstile token timeout after %v", timeout)
}

// RenderTurnstile renders a Turnstile widget on the page. The page must
// already have the Turnstile api.js loaded (or it will be injected).
func (c *Client) RenderTurnstile(siteKey string) error {
	// Ensure api.js is loaded
	_, _ = c.Evaluate(fmt.Sprintf(`(function(){
  if (typeof turnstile === 'undefined') {
    var s = document.createElement('script');
    s.src = 'https://challenges.cloudflare.com/turnstile/v0/api.js';
    s.async = true;
    document.head.appendChild(s);
  }
  window.__ts_token = '';
  window.__ts_err = '';
  window.__ts_rendered = false;
})()`))
	// Wait for turnstile to be ready
	for i := 0; i < 90; i++ {
		r, _ := c.Evaluate("typeof turnstile !== 'undefined' ? 'yes' : 'no'")
		if r == "yes" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	// render('#cf-turnstile') 需要容器存在,页面没有就先建一个
	// (否则 render 抛 "Unable to find a container for #cf-turnstile")
	_, _ = c.Evaluate(`(function(){
  if (!document.querySelector('#cf-turnstile')) {
    var d = document.createElement('div');
    d.id = 'cf-turnstile';
    d.style.cssText = 'position:fixed;bottom:10px;right:10px;width:350px;height:120px;z-index:99999;';
    document.body.appendChild(d);
  }
})()`)
	// Render
	_, err := c.Evaluate(fmt.Sprintf(`(function(){
  try {
    turnstile.render('#cf-turnstile', {
      sitekey: '%s',
      callback: function(token) { window.__ts_token = token; },
      'error-callback': function(err) { window.__ts_err = err; }
    });
    window.__ts_rendered = true;
  } catch(e) { window.__ts_err = 'render-threw:' + e.message; }
})()`, siteKey))
	return err
}

// ProbeAPIs checks which browser APIs are available (for fingerprinting
// compatibility analysis). Returns JSON with the status of each API.
func (c *Client) ProbeAPIs() (string, error) {
	script := `(function(){
  var checks = {
    'crypto.subtle': typeof crypto !== 'undefined' && crypto.subtle ? 'yes' : 'no',
    'crypto.getRandomValues': typeof crypto !== 'undefined' && crypto.getRandomValues ? 'yes' : 'no',
    'canvas 2d': (function(){try{var c=document.createElement('canvas');return c.getContext('2d')?'yes':'no'}catch(e){return 'err'}})(),
    'canvas webgl': (function(){try{var c=document.createElement('canvas');return c.getContext('webgl')?'yes':'no'}catch(e){return 'err'}})(),
    'navigator.userAgent': navigator.userAgent,
    'navigator.platform': navigator.platform,
    'navigator.webdriver': String(navigator.webdriver),
    'window.chrome': typeof window.chrome,
    'navigator.plugins.length': String(navigator.plugins && navigator.plugins.length),
    'MessageChannel': typeof MessageChannel,
    'WebSocket': typeof WebSocket,
    'Worker': typeof Worker,
    'fetch': typeof fetch,
    'indexedDB': typeof indexedDB,
    'MutationObserver': typeof MutationObserver,
    'ResizeObserver': typeof ResizeObserver,
    'requestAnimationFrame': typeof requestAnimationFrame,
    'WebGLRenderingContext': typeof WebGLRenderingContext,
    'WebGL2RenderingContext': typeof WebGL2RenderingContext,
    'Notification': typeof Notification,
    'speechSynthesis': typeof speechSynthesis,
    'localStorage': typeof localStorage,
    'sessionStorage': typeof sessionStorage,
  };
  return JSON.stringify(checks);
})()`
	return c.Evaluate(script)
}
