package curlcffi

// utls_session.go — 基于 utls 的自定义 TLS 指纹会话
// 支持 ja3/akamai/perk 自定义指纹（curl_cffi 的核心功能）
// 不经过 tls-client，直接用 utls 底层库

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	utls "github.com/bogdanfinn/utls"
)

// UTlsSession 基于 utls 的会话，支持自定义 JA3 指纹
type UTlsSession struct {
	ja3       string
	akamai    string
	perk      string
	helloID   utls.ClientHelloID
	helloSpec *utls.ClientHelloSpec
	proxy     string
	timeout   time.Duration
	headers   map[string]string
	cookies   map[string]string
	closed    bool
	transport *http.Transport
	client    *http.Client
}

// NewUTlsSession 创建自定义 TLS 指纹会话
func NewUTlsSession(opts ...func(*UTlsSession)) (*UTlsSession, error) {
	s := &UTlsSession{
		timeout: 30 * time.Second,
		headers: make(map[string]string),
		cookies: make(map[string]string),
		helloID: utls.HelloChrome_Auto,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s.build()
}

// WithJA3 设置自定义 JA3 指纹
func WithJA3(ja3 string) func(*UTlsSession) {
	return func(s *UTlsSession) { s.ja3 = ja3 }
}

// WithAkamai 设置自定义 Akamai (HTTP/2) 指纹
func WithAkamai(akamai string) func(*UTlsSession) {
	return func(s *UTlsSession) { s.akamai = akamai }
}

// WithPerk 设置自定义 Perk 指纹
func WithPerk(perk string) func(*UTlsSession) {
	return func(s *UTlsSession) { s.perk = perk }
}

// WithHelloID 设置 utls 预设指纹
func WithHelloID(id utls.ClientHelloID) func(*UTlsSession) {
	return func(s *UTlsSession) { s.helloID = id }
}

// WithUTlsProxy 设置代理
func WithUTlsProxy(proxy string) func(*UTlsSession) {
	return func(s *UTlsSession) { s.proxy = proxy }
}

// WithUTlsTimeout 设置超时
func WithUTlsTimeout(d time.Duration) func(*UTlsSession) {
	return func(s *UTlsSession) { s.timeout = d }
}

// WithUTlsHeaders 设置默认 headers
func WithUTlsHeaders(headers map[string]string) func(*UTlsSession) {
	return func(s *UTlsSession) {
		for k, v := range headers {
			s.headers[k] = v
		}
	}
}

// WithUTlsCookies 设置 cookies
func WithUTlsCookies(cookies map[string]string) func(*UTlsSession) {
	return func(s *UTlsSession) {
		for k, v := range cookies {
			s.cookies[k] = v
		}
	}
}

// build 构建 utls 会话
func (s *UTlsSession) build() (*UTlsSession, error) {
	// 解析 JA3（如果提供）
	if s.ja3 != "" {
		spec, err := ParseJA3(s.ja3)
		if err != nil {
			return nil, fmt.Errorf("parse JA3: %w", err)
		}
		s.helloSpec = spec
	}

	// 创建 utls 拨号器
	dialer := &net.Dialer{Timeout: s.timeout}

	dialTLS := func(network, addr string) (net.Conn, error) {
		// 如果有代理，先连代理
		var rawConn net.Conn
		var err error
		if s.proxy != "" {
			rawConn, err = dialViaProxy(dialer, addr, s.proxy)
			if err != nil {
				return nil, fmt.Errorf("proxy dial: %w", err)
			}
		} else {
			rawConn, err = dialer.Dial(network, addr)
			if err != nil {
				return nil, fmt.Errorf("dial: %w", err)
			}
		}

		// 提取 hostname
		host, _, _ := net.SplitHostPort(addr)

		// 创建 utls 连接
		config := &utls.Config{
			ServerName:         host,
			InsecureSkipVerify: true,
			NextProtos:         []string{"http/1.1"}, // 只协商 HTTP/1.1（避免 h2 兼容问题）
		}

		uconn := utls.UClient(rawConn, config, s.helloID, false, false, false)

		// 如果有自定义 JA3 spec，覆盖预设
		if s.helloSpec != nil {
			if err := uconn.ApplyPreset(s.helloSpec); err != nil {
				rawConn.Close()
				return nil, fmt.Errorf("apply JA3 preset: %w", err)
			}
		}

		// TLS 握手
		ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
		defer cancel()
		if err := uconn.HandshakeContext(ctx); err != nil {
			rawConn.Close()
			return nil, fmt.Errorf("TLS handshake: %w", err)
		}

		return uconn, nil
	}

	// 创建 HTTP Transport（HTTP/1.1 only，utls 不兼容标准库 HTTP/2）
	transport := &http.Transport{
		DialTLS:         dialTLS,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		MaxIdleConns:    10,
		IdleConnTimeout: s.timeout,
	}

	s.transport = transport
	s.client = &http.Client{
		Transport: transport,
		Timeout:   s.timeout,
		Jar:       newCookieJar(),
	}

	// 设置默认 headers
	if _, ok := s.headers["User-Agent"]; !ok {
		s.headers["User-Agent"] = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	}
	if _, ok := s.headers["Accept"]; !ok {
		s.headers["Accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
	}
	if _, ok := s.headers["Accept-Language"]; !ok {
		s.headers["Accept-Language"] = "en-US,en;q=0.9"
	}

	return s, nil
}

// dialViaProxy 通过代理拨号
func dialViaProxy(dialer *net.Dialer, targetAddr, proxyURL string) (net.Conn, error) {
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, err
	}

	// 连接代理服务器
	conn, err := dialer.Dial("tcp", u.Host)
	if err != nil {
		return nil, err
	}

	// HTTP CONNECT 隧道
	host, port, _ := net.SplitHostPort(targetAddr)
	connectReq := fmt.Sprintf("CONNECT %s:%s HTTP/1.1\r\nHost: %s:%s\r\n", host, port, host, port)

	// 代理认证
	if u.User != nil {
		user := u.User.Username()
		pass, _ := u.User.Password()
		auth := BasicAuth(user, pass)
		connectReq += fmt.Sprintf("Proxy-Authorization: %s\r\n", auth)
	}
	connectReq += "\r\n"

	if _, err := conn.Write([]byte(connectReq)); err != nil {
		conn.Close()
		return nil, err
	}

	// 读取 CONNECT 响应
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		conn.Close()
		return nil, err
	}

	// 检查是否 200
	if n < 12 || string(buf[:12]) != "HTTP/1.1 200" {
		conn.Close()
		return nil, fmt.Errorf("proxy CONNECT failed: %s", string(buf[:n]))
	}

	return conn, nil
}

// Request 发送 HTTP 请求
func (s *UTlsSession) Request(method, targetURL string, params map[string]string, body io.Reader, headers map[string]string) (*Response, error) {
	if s.closed {
		return nil, fmt.Errorf("session is closed")
	}

	// 附加 params
	if len(params) > 0 {
		u, err := url.Parse(targetURL)
		if err == nil {
			q := u.Query()
			for k, v := range params {
				q.Set(k, v)
			}
			u.RawQuery = q.Encode()
			targetURL = u.String()
		}
	}

	// 构建请求
	req, err := http.NewRequest(method, targetURL, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	// 设置 headers
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// 设置 cookies
	for name, val := range s.cookies {
		req.AddCookie(&http.Cookie{Name: name, Value: val})
	}

	// 发送请求
	startTime := time.Now()
	resp, err := s.client.Do(req)
	elapsed := time.Since(startTime)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	content, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	// 转换为 curlcffi.Response
	respHeaders := fhttp.Header{}
	for k, vs := range resp.Header {
		for _, v := range vs {
			respHeaders.Add(k, v)
		}
	}

	var respCookies []*fhttp.Cookie
	for _, c := range resp.Cookies() {
		respCookies = append(respCookies, &fhttp.Cookie{Name: c.Name, Value: c.Value})
	}

	return &Response{
		URL:         resp.Request.URL.String(),
		Content:     content,
		StatusCode:  resp.StatusCode,
		Reason:      resp.Status,
		Headers:     respHeaders,
		Cookies:     respCookies,
		Elapsed:     elapsed,
		RedirectURL: resp.Header.Get("Location"),
	}, nil
}

// Get 发送 GET 请求
func (s *UTlsSession) Get(targetURL string) (*Response, error) {
	return s.Request("GET", targetURL, nil, nil, nil)
}

// Post 发送 POST 请求
func (s *UTlsSession) Post(targetURL string, body io.Reader, headers map[string]string) (*Response, error) {
	return s.Request("POST", targetURL, nil, body, headers)
}

// Close 关闭会话
func (s *UTlsSession) Close() {
	s.closed = true
	if s.transport != nil {
		s.transport.CloseIdleConnections()
	}
}

// --- 辅助类型 ---

// 简单 cookie jar（用于 net/http.Client）
type simpleCookieJar struct {
	cookies map[string]string
}

func newCookieJar() *simpleCookieJar {
	return &simpleCookieJar{cookies: make(map[string]string)}
}

func (j *simpleCookieJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	for _, c := range cookies {
		j.cookies[c.Name] = c.Value
	}
}

func (j *simpleCookieJar) Cookies(u *url.URL) []*http.Cookie {
	var result []*http.Cookie
	for name, val := range j.cookies {
		result = append(result, &http.Cookie{Name: name, Value: val})
	}
	return result
}
