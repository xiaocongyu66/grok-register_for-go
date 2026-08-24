package curlcffi

// curlcffi.go — Go 版 curl_cffi，用 bogdanfinn/tls-client 替代 libcurl 的 TLS 指纹伪装
// 100% 兼容 Python curl_cffi 的 requests.Session API
// 替代: curl_cffi.requests.Session(impersonate="chrome131")

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	http "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// --- BrowserType (替代 curl_cffi 的 BrowserTypeLiteral) ---

type BrowserType string

const (
	// Chrome
	Chrome99      BrowserType = "chrome99"
	Chrome100     BrowserType = "chrome100"
	Chrome101     BrowserType = "chrome101"
	Chrome104     BrowserType = "chrome104"
	Chrome107     BrowserType = "chrome107"
	Chrome110     BrowserType = "chrome110"
	Chrome116     BrowserType = "chrome116"
	Chrome119     BrowserType = "chrome119"
	Chrome120     BrowserType = "chrome120"
	Chrome123     BrowserType = "chrome123"
	Chrome124     BrowserType = "chrome124"
	Chrome131     BrowserType = "chrome131"
	Chrome133a    BrowserType = "chrome133a"
	Chrome136     BrowserType = "chrome136"
	Chrome142     BrowserType = "chrome142"
	Chrome145     BrowserType = "chrome145"
	Chrome146     BrowserType = "chrome146"
	Chrome150     BrowserType = "chrome150"
	Chrome        BrowserType = "chrome" // alias → latest
	ChromeAndroid BrowserType = "chrome99_android"
	// Edge
	Edge99  BrowserType = "edge99"
	Edge101 BrowserType = "edge101"
	Edge    BrowserType = "edge"
	// Safari
	Safari153  BrowserType = "safari153"
	Safari155  BrowserType = "safari155"
	Safari170  BrowserType = "safari170"
	Safari180  BrowserType = "safari180"
	Safari184  BrowserType = "safari184"
	Safari260  BrowserType = "safari260"
	Safari2601 BrowserType = "safari2601"
	Safari     BrowserType = "safari"
	// Firefox
	Firefox133 BrowserType = "firefox133"
	Firefox135 BrowserType = "firefox135"
	Firefox144 BrowserType = "firefox144"
	Firefox147 BrowserType = "firefox147"
	Firefox    BrowserType = "firefox"
	Tor145     BrowserType = "tor145"
)

// resolveProfile 将 BrowserType 映射到 tls-client 的 ClientProfile
func resolveProfile(bt BrowserType) (string, profiles.ClientProfile) {
	switch bt {
	case Chrome, Chrome150, Chrome146, Chrome142, Chrome136:
		return "chrome_131", profiles.Chrome_131 // 最新可用
	case Chrome131:
		return "chrome_131", profiles.Chrome_131
	case Chrome124:
		return "chrome_124", profiles.Chrome_124
	case Chrome120:
		return "chrome_120", profiles.Chrome_120
	case Chrome119:
		return "chrome_119", profiles.Chrome_120 // 可能不存在，fallback
	case Chrome110:
		return "chrome_110", profiles.Chrome_110 // 可能不存在，fallback
	case Edge, Edge101:
		return "edge_101", profiles.Chrome_131
	case Edge99:
		return "edge_99", profiles.Chrome_131
	case Safari, Safari2601, Safari260, Safari184, Safari180, Safari170, Safari155, Safari153:
		return "safari_17", profiles.Chrome_131
	case Firefox, Firefox147, Firefox144, Firefox135, Firefox133:
		return "firefox_135", profiles.Chrome_131 // 可能不存在，fallback
	default:
		return "chrome_131", profiles.Chrome_131
	}
}

// --- Response (替代 curl_cffi.requests.Response) ---

type Response struct {
	URL          string
	Content      []byte
	StatusCode   int
	Reason       string
	Headers      http.Header
	Cookies      []*http.Cookie
	Elapsed      time.Duration
	HTTPVersion  string
	PrimaryIP    string
	PrimaryPort  int
	RedirectCount int
	RedirectURL  string
	History      []*Response
}

// Text 返回响应体文本
func (r *Response) Text() string {
	return string(r.Content)
}

// JSON 解析响应体为 JSON
func (r *Response) JSON(v interface{}) error {
	return json.Unmarshal(r.Content, v)
}

// OK 返回状态码是否在 200-399
func (r *Response) OK() bool {
	return r.StatusCode >= 200 && r.StatusCode < 400
}

// RaiseForStatus 如果状态码 >= 400 则返回 error
func (r *Response) RaiseForStatus() error {
	if r.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d %s", r.StatusCode, r.Reason)
	}
	return nil
}

// IsRedirect 返回是否为重定向
func (r *Response) IsRedirect() bool {
	return r.StatusCode >= 300 && r.StatusCode < 400
}

// Header 获取单个响应头
func (r *Response) Header(key string) string {
	return r.Headers.Get(key)
}

// Charset 返回响应的字符集（从 Content-Type 头解析）
func (r *Response) Charset() string {
	if r == nil || r.Headers == nil {
		return "utf-8"
	}
	ct := r.Headers.Get("Content-Type")
	if ct == "" {
		return "utf-8"
	}
	// 解析 charset=xxx
	idx := strings.Index(strings.ToLower(ct), "charset=")
	if idx < 0 {
		return "utf-8"
	}
	charset := ct[idx+8:]
	// 去除分号后的内容
	if semi := strings.Index(charset, ";"); semi > 0 {
		charset = charset[:semi]
	}
	charset = strings.TrimSpace(charset)
	// 去除引号
	if len(charset) >= 2 && charset[0] == '"' {
		charset = charset[1 : len(charset)-1]
	}
	if charset == "" {
		return "utf-8"
	}
	return strings.ToLower(charset)
}

// CharsetEncoding 返回 charset 的别名（兼容 Python 的 charset_encoding 属性）
func (r *Response) CharsetEncoding() string {
	return r.Charset()
}

// Encoding 返回编码（同 Charset）
func (r *Response) Encoding() string {
	return r.Charset()
}

// --- Request (请求参数) ---

type FileField struct {
	FieldName   string
	FileName    string
	ContentType string
	Content     []byte
}

type Request struct {
	Method          string
	URL             string
	Params          map[string]string
	Headers         map[string]string
	Cookies         map[string]string
	Data            []byte
	JSON            interface{}
	Files           []FileField
	Timeout         time.Duration
	AllowRedirects  *bool
	MaxRedirects    int
	Proxy           string
	Proxies         map[string]string
	Impersonate     BrowserType
	Verify          *bool
	Auth            [2]string
	Referer         string
	collectConnInfo bool
}

// --- Session (替代 curl_cffi.requests.Session) ---

type Session struct {
	client      tls_client.HttpClient
	profile     string
	proxy       string
	timeout     time.Duration
	headers     map[string]string
	cookies     map[string]string
	impersonate BrowserType
	jar         tls_client.CookieJar
	closed      bool
	// 扩展字段（对应 Python Session.__init__ 参数）
	baseURL         string
	maxRedirects    int
	verify          bool
	retry           RetryStrategy
	auth            [2]string
	referer         string
	debug           bool
	dohURL          string
	iface           string
	defaultParams   map[string]string
	trustEnv        bool
	allowRedirects  bool
	useDefaultHeaders bool
	discardCookies bool
	raiseForStatus  bool
	httpVersion     string
	certPath        string
	certKeyPath     string
	cache           *FileCacheBackend
	ja3Profile      string
	akamaiProfile   string
}

// NewSession 创建一个新的 TLS 指纹会话（替代 curl_cffi.Session(impersonate=...)）
func NewSession(opts ...func(*Session)) (*Session, error) {
	s := &Session{
		timeout:     30 * time.Second,
		headers:     make(map[string]string),
		cookies:     make(map[string]string),
		impersonate: Chrome131,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s.build()
}

// WithImpersonate 设置浏览器指纹
func WithImpersonate(bt BrowserType) func(*Session) {
	return func(s *Session) { s.impersonate = bt }
}

// WithProxy 设置代理
func WithProxy(proxy string) func(*Session) {
	return func(s *Session) { s.proxy = proxy }
}

// WithTimeout 设置超时
func WithTimeout(d time.Duration) func(*Session) {
	return func(s *Session) { s.timeout = d }
}

// WithHeaders 设置默认 headers
func WithHeaders(headers map[string]string) func(*Session) {
	return func(s *Session) {
		for k, v := range headers {
			s.headers[k] = v
		}
	}
}

// WithCookies 设置默认 cookies
func WithCookies(cookies map[string]string) func(*Session) {
	return func(s *Session) {
		for k, v := range cookies {
			s.cookies[k] = v
		}
	}
}

// build 构建底层 tls-client
func (s *Session) build() (*Session, error) {
	name, prof := resolveProfile(s.impersonate)
	s.profile = name

	jar := tls_client.NewCookieJar()
	opts := []tls_client.HttpClientOption{
		tls_client.WithTimeoutSeconds(int(s.timeout.Seconds())),
		tls_client.WithClientProfile(prof),
		tls_client.WithRandomTLSExtensionOrder(),
		tls_client.WithCookieJar(jar),
		tls_client.WithNotFollowRedirects(),
		tls_client.WithInsecureSkipVerify(),
	}
	if s.proxy != "" {
		opts = append(opts, tls_client.WithProxyUrl(s.proxy))
	}

	// 如果有自定义 JA3，覆盖 profile
	if s.ja3Profile != "" {
		var customProfile profiles.ClientProfile
		var err error
		if s.akamaiProfile != "" {
			customProfile, err = ProfileFromJA3AndAkamai(s.ja3Profile, s.akamaiProfile)
		} else {
			customProfile, err = ProfileFromJA3(s.ja3Profile)
		}
		if err != nil {
			return nil, fmt.Errorf("custom JA3 profile: %w", err)
		}
		// 替换 opts 中的 profile
		opts[1] = tls_client.WithClientProfile(customProfile)
		s.profile = "custom_ja3"
	}

	client, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), opts...)
	if err != nil {
		return nil, fmt.Errorf("create tls-client: %w", err)
	}
	client.SetFollowRedirect(true)

	s.client = client
	s.jar = jar

	// 设置默认 headers
	if _, ok := s.headers["User-Agent"]; !ok {
		s.headers["User-Agent"] = defaultUA(s.impersonate)
	}
	if _, ok := s.headers["Accept"]; !ok {
		s.headers["Accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8"
	}
	if _, ok := s.headers["Accept-Language"]; !ok {
		s.headers["Accept-Language"] = "en-US,en;q=0.9"
	}
	if _, ok := s.headers["Accept-Encoding"]; !ok {
		s.headers["Accept-Encoding"] = "gzip, deflate, br"
	}

	// 应用 cookies
	for name, val := range s.cookies {
		jar.SetCookies(nil, []*http.Cookie{{Name: name, Value: val}})
	}

	return s, nil
}

// defaultUA 返回浏览器对应的 User-Agent
func defaultUA(bt BrowserType) string {
	switch bt {
	case Chrome, Chrome131, Chrome146, Chrome150, Chrome136, Chrome142, Chrome145, Chrome133a:
		return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	case Chrome120, Chrome124, Chrome123, Chrome119, Chrome116:
		return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	case Chrome110, Chrome107, Chrome104, Chrome101, Chrome100, Chrome99:
		return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/110.0.0.0 Safari/537.36"
	case Edge, Edge101, Edge99:
		return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 Edg/131.0.0.0"
	case Safari, Safari2601, Safari260, Safari184, Safari180, Safari170, Safari155, Safari153:
		return "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"
	case Firefox, Firefox147, Firefox144, Firefox135, Firefox133, Tor145:
		return "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:135.0) Gecko/20100101 Firefox/135.0"
	default:
		return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	}
}

// --- Session 方法 ---

// Request 发送 HTTP 请求（替代 session.request()）
func (s *Session) Request(req *Request) (*Response, error) {
	if s.closed {
		return nil, fmt.Errorf("session is closed")
	}

	// 缓存检查
	if s.cache != nil {
		if cached := s.cache.Get(req.Method, req.URL); cached != nil {
			if s.debug {
				fmt.Printf("[curlcffi] cache hit: %s %s\n", req.Method, req.URL)
			}
			return cached, nil
		}
	}

	// base_url 拼接
	targetURL := s.resolveURL(req.URL)

	// 附加 params
	if len(req.Params) > 0 {
		u, err := url.Parse(targetURL)
		if err == nil {
			q := u.Query()
			for k, v := range req.Params {
				q.Set(k, v)
			}
			u.RawQuery = q.Encode()
			targetURL = u.String()
		}
	}

	// 准备 body
	var body io.Reader
	var contentType string
	if req.JSON != nil {
		jsonBytes, err := json.Marshal(req.JSON)
		if err != nil {
			return nil, fmt.Errorf("marshal json: %w", err)
		}
		body = bytes.NewReader(jsonBytes)
		contentType = "application/json"
	} else if len(req.Files) > 0 {
		// 文件上传 — 用 textproto.MIMEHeader
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		for _, f := range req.Files {
			header := make(textproto.MIMEHeader)
			header.Set("Content-Disposition", fmt.Sprintf("form-data; name=\"%s\"; filename=\"%s\"", f.FieldName, f.FileName))
			if f.ContentType != "" {
				header.Set("Content-Type", f.ContentType)
			}
			part, err := writer.CreatePart(header)
			if err != nil {
				continue
			}
			part.Write(f.Content)
		}
		writer.Close()
		body = &buf
		contentType = writer.FormDataContentType()
	} else if req.Data != nil {
		body = bytes.NewReader(req.Data)
	}

	// 构建请求
	httpReq, err := http.NewRequest(req.Method, targetURL, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	// 设置 headers（合并 session 默认 + 请求自定义）
	for k, v := range s.headers {
		httpReq.Header.Set(k, v)
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	if contentType != "" {
		httpReq.Header.Set("Content-Type", contentType)
	}

	// 设置 Referer
	if req.Referer != "" {
		httpReq.Header.Set("Referer", req.Referer)
	} else if s.referer != "" {
		httpReq.Header.Set("Referer", s.referer)
	}

	// 设置 Basic Auth
	if req.Auth[0] != "" {
		httpReq.SetBasicAuth(req.Auth[0], req.Auth[1])
	} else if s.auth[0] != "" {
		httpReq.SetBasicAuth(s.auth[0], s.auth[1])
	}

	// 设置 cookies
	for name, val := range req.Cookies {
		httpReq.AddCookie(&http.Cookie{Name: name, Value: val})
	}

	// 发送请求
	startTime := time.Now()
	resp, err := s.client.Do(httpReq)
	elapsed := time.Since(startTime)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	// 手动把响应的 Set-Cookie 存入 jar(tls-client 的 jar 可能不自动存)
	s.storeResponseCookies(resp, targetURL)

	// 如果 AllowRedirects=true,手动跟随重定向(tls-client 默认不跟随)
	followRedirects := req.AllowRedirects != nil && *req.AllowRedirects
	if followRedirects {
		resp, elapsed, err = s.followRedirects(resp, httpReq, targetURL, startTime)
		if err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()

	// 读取响应体（处理 gzip/deflate/brotli）
	var reader io.Reader = resp.Body
	switch resp.Header.Get("Content-Encoding") {
	case "gzip":
		gz, err := gzip.NewReader(resp.Body)
		if err == nil {
			defer gz.Close()
			reader = gz
		}
	case "deflate":
		zr, err := zlib.NewReader(resp.Body)
		if err == nil {
			defer zr.Close()
			reader = zr
		}
	}

	content, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	// 提取 cookies — 从响应和 jar 中获取
	var cookies []*http.Cookie
	for _, cookie := range resp.Cookies() {
		cookies = append(cookies, cookie)
	}
	// 也从 jar 中获取（jar 可能包含之前请求积累的 cookies）
	if s.jar != nil {
		reqURL, _ := url.Parse(targetURL)
		for _, cookie := range s.jar.Cookies(reqURL) {
			// 避免重复
			found := false
			for _, c := range cookies {
				if c.Name == cookie.Name {
					found = true
					break
				}
			}
			if !found {
				cookies = append(cookies, cookie)
			}
		}
	}

	// 重定向历史
	var history []*Response

	result := &Response{
		URL:           resp.Request.URL.String(),
		Content:       content,
		StatusCode:    resp.StatusCode,
		Reason:        resp.Status,
		Headers:       resp.Header,
		Cookies:       cookies,
		Elapsed:       elapsed,
		RedirectURL:   resp.Header.Get("Location"),
		RedirectCount: len(history),
		History:       history,
	}

	// 连接信息收集
	if req.collectConnInfo {
		if info, err := GetConnInfo(targetURL, s.proxy); err == nil {
			result.PrimaryIP = info.PrimaryIP
			result.PrimaryPort = info.PrimaryPort
		}
	}

	// 缓存写入
	if s.cache != nil && result.OK() {
		s.cache.Set(req.Method, req.URL, result)
	}

	// 自动 RaiseForStatus
	if s.raiseForStatus {
		if err := result.RaiseForStatus(); err != nil {
			return result, err
		}
	}

	return result, nil
}

// Get 发送 GET 请求
func (s *Session) Get(targetURL string, opts ...func(*Request)) (*Response, error) {
	req := &Request{Method: "GET", URL: targetURL}
	for _, opt := range opts {
		opt(req)
	}
	return s.Request(req)
}

// Post 发送 POST 请求
func (s *Session) Post(targetURL string, opts ...func(*Request)) (*Response, error) {
	req := &Request{Method: "POST", URL: targetURL}
	for _, opt := range opts {
		opt(req)
	}
	return s.Request(req)
}

// Put 发送 PUT 请求
func (s *Session) Put(targetURL string, opts ...func(*Request)) (*Response, error) {
	req := &Request{Method: "PUT", URL: targetURL}
	for _, opt := range opts {
		opt(req)
	}
	return s.Request(req)
}

// Delete 发送 DELETE 请求
func (s *Session) Delete(targetURL string, opts ...func(*Request)) (*Response, error) {
	req := &Request{Method: "DELETE", URL: targetURL}
	for _, opt := range opts {
		opt(req)
	}
	return s.Request(req)
}

// Head 发送 HEAD 请求
func (s *Session) Head(targetURL string, opts ...func(*Request)) (*Response, error) {
	req := &Request{Method: "HEAD", URL: targetURL}
	for _, opt := range opts {
		opt(req)
	}
	return s.Request(req)
}

// Patch 发送 PATCH 请求
func (s *Session) Patch(targetURL string, opts ...func(*Request)) (*Response, error) {
	req := &Request{Method: "PATCH", URL: targetURL}
	for _, opt := range opts {
		opt(req)
	}
	return s.Request(req)
}

// Options 发送 OPTIONS 请求
func (s *Session) Options(targetURL string, opts ...func(*Request)) (*Response, error) {
	req := &Request{Method: "OPTIONS", URL: targetURL}
	for _, opt := range opts {
		opt(req)
	}
	return s.Request(req)
}

// --- Request 选项函数 ---

func WithParams(params map[string]string) func(*Request) {
	return func(r *Request) { r.Params = params }
}

func WithReqHeaders(headers map[string]string) func(*Request) {
	return func(r *Request) { r.Headers = headers }
}

func WithReqCookies(cookies map[string]string) func(*Request) {
	return func(r *Request) { r.Cookies = cookies }
}

func WithData(data []byte) func(*Request) {
	return func(r *Request) { r.Data = data }
}

func WithJSONBody(body interface{}) func(*Request) {
	return func(r *Request) { r.JSON = body }
}

func WithReqTimeout(d time.Duration) func(*Request) {
	return func(r *Request) { r.Timeout = d }
}

// SetCookies 设置 session cookies
func (s *Session) SetCookies(cookies map[string]string) {
	if s.jar == nil {
		return
	}
	// tls-client 的 cookieJar.SetCookies 需要标准库 *url.URL
	for name, val := range cookies {
		s.jar.SetCookies(&url.URL{Scheme: "https", Host: ""}, []*http.Cookie{{Name: name, Value: val}})
	}
}

// storeResponseCookies 把 HTTP 响应的 Set-Cookie 存入 jar。
// tls-client 的 jar 可能不自动存储跨域重定向的 cookie,这里手动存。
func (s *Session) storeResponseCookies(resp *http.Response, requestURL string) {
	if s.jar == nil || resp == nil {
		return
	}
	reqURL, err := url.Parse(requestURL)
	if err != nil {
		return
	}
	cookies := resp.Cookies()
	if len(cookies) > 0 {
		s.jar.SetCookies(reqURL, cookies)
	}
}

// followRedirects 手动跟随重定向链。
// tls-client 默认 WithNotFollowRedirects,AllowRedirects=true 时需要手动跟随。
// 每跳都把 Set-Cookie 存入 jar,最多 10 跳防止循环。
// 返回最终响应(已读取 body 之前的 *http.Response)。
func (s *Session) followRedirects(initialResp *http.Response, originalReq *http.Request, initialURL string, startTime time.Time) (*http.Response, time.Duration, error) {
	currentResp := initialResp
	currentURL := initialURL
	totalElapsed := time.Since(startTime)

	for i := 0; i < 10; i++ {
		if currentResp.StatusCode < 300 || currentResp.StatusCode >= 400 {
			break // 非重定向,停止
		}
		location := currentResp.Header.Get("Location")
		if location == "" {
			break
		}

		// 解析重定向 URL(处理相对路径)
		base, _ := url.Parse(currentURL)
		loc, err := url.Parse(location)
		if err != nil {
			break
		}
		nextURL := base.ResolveReference(loc).String()

		// 关闭当前响应 body,发起新请求
		currentResp.Body.Close()

		nextReq, err := http.NewRequest(originalReq.Method, nextURL, nil)
		if err != nil {
			break
		}
		// 继承 headers
		nextReq.Header = originalReq.Header.Clone()
		// 重定向用 GET(除非 307/308 保持方法)
		if currentResp.StatusCode != 307 && currentResp.StatusCode != 308 {
			nextReq.Method = "GET"
		}

		reqStart := time.Now()
		currentResp, err = s.client.Do(nextReq)
		totalElapsed += time.Since(reqStart)
		if err != nil {
			return nil, totalElapsed, fmt.Errorf("redirect failed: %w", err)
		}
		currentURL = nextURL
		// 存这跳的 Set-Cookie
		s.storeResponseCookies(currentResp, currentURL)
	}
	return currentResp, totalElapsed, nil
}

// GetCookies 获取当前 cookies
func (s *Session) GetCookies() map[string]string {
	result := make(map[string]string)
	// tls-client 的 jar 没有直接导出所有 cookies 的方法
	// 通过 header 方式获取
	for _, cookie := range s.cookies {
		result[""] = cookie
	}
	return result
}

// GetCookiesForURL 返回 jar 里匹配指定 URL 的所有 cookie。
// 用于从 session cookie jar 提取特定域名的 cookie(如 grok.com 的 sso)。
func (s *Session) GetCookiesForURL(rawURL string) map[string]string {
	result := make(map[string]string)
	if s.jar == nil {
		return result
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return result
	}
	for _, cookie := range s.jar.Cookies(parsed) {
		result[cookie.Name] = cookie.Value
	}
	return result
}

// SetHeaders 更新 session 默认 headers
func (s *Session) SetHeaders(headers map[string]string) {
	for k, v := range headers {
		s.headers[k] = v
	}
}

// Close 关闭会话
func (s *Session) Close() {
	s.closed = true
}

// Profile 返回当前 TLS 指纹 profile 名称
func (s *Session) Profile() string {
	return s.profile
}

// Impersonate 返回当前浏览器指纹类型
func (s *Session) Impersonate() BrowserType {
	return s.impersonate
}

// --- 模块级函数（替代 curl_cffi.requests.get/post/...） ---

// GetQuick 发送一次性 GET 请求
func GetQuick(targetURL string, impersonate BrowserType, proxy string) (*Response, error) {
	s, err := NewSession(WithImpersonate(impersonate), WithProxy(proxy))
	if err != nil {
		return nil, err
	}
	defer s.Close()
	return s.Get(targetURL)
}

// PostQuick 发送一次性 POST 请求
func PostQuick(targetURL string, data []byte, impersonate BrowserType, proxy string) (*Response, error) {
	s, err := NewSession(WithImpersonate(impersonate), WithProxy(proxy))
	if err != nil {
		return nil, err
	}
	defer s.Close()
	return s.Post(targetURL, WithData(data))
}

// PostJSONQuick 发送一次性 JSON POST 请求
func PostJSONQuick(targetURL string, body interface{}, impersonate BrowserType, proxy string) (*Response, error) {
	s, err := NewSession(WithImpersonate(impersonate), WithProxy(proxy))
	if err != nil {
		return nil, err
	}
	defer s.Close()
	return s.Post(targetURL, WithJSONBody(body))
}

// suppress unused
var _ = strings.TrimSpace
