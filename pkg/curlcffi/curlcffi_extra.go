package curlcffi

// curlcffi_extra.go — 补齐剩余缺失功能

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	http "github.com/bogdanfinn/fhttp"
)

// ============================================================
// Session.__init__ 缺失参数
// ============================================================

// WithSessionParams 设置 session 默认 params（每次请求自动附加）
func WithSessionParams(params map[string]string) func(*Session) {
	return func(s *Session) {
		if s.defaultParams == nil {
			s.defaultParams = make(map[string]string)
		}
		for k, v := range params {
			s.defaultParams[k] = v
		}
	}
}

// WithTrustEnv 设置是否信任环境变量代理
func WithTrustEnv(trust bool) func(*Session) {
	return func(s *Session) { s.trustEnv = trust }
}

// WithAllowRedirects 设置是否允许重定向
func WithAllowRedirectsSession(allow bool) func(*Session) {
	return func(s *Session) { s.allowRedirects = allow }
}

// WithDefaultHeaders 设置是否使用默认 headers
func WithDefaultHeaders(use bool) func(*Session) {
	return func(s *Session) { s.useDefaultHeaders = use }
}

// WithDiscardCookies 设置是否丢弃 cookies
func WithDiscardCookies(discard bool) func(*Session) {
	return func(s *Session) { s.discardCookies = discard }
}

// WithRaiseForStatus 设置是否自动 RaiseForStatus
func WithRaiseForStatus(raise bool) func(*Session) {
	return func(s *Session) { s.raiseForStatus = raise }
}

// WithHTTPVersion 设置 HTTP 版本
func WithHTTPVersion(version string) func(*Session) {
	return func(s *Session) { s.httpVersion = version }
}

// WithCert 设置客户端证书
func WithCert(certPath, keyPath string) func(*Session) {
	return func(s *Session) { s.certPath = certPath; s.certKeyPath = keyPath }
}

// WithInterface 设置网络接口
func WithInterface(iface string) func(*Session) {
	return func(s *Session) { s.iface = iface }
}

// ============================================================
// Request 缺失参数
// ============================================================

// WithContent 设置原始请求体（区别于 WithData）
func WithContent(content []byte) func(*Request) {
	return func(r *Request) { r.Data = content }
}

// WithAcceptEncoding 设置 Accept-Encoding 头
func WithAcceptEncoding(encoding string) func(*Request) {
	return func(r *Request) {
		if r.Headers == nil {
			r.Headers = make(map[string]string)
		}
		r.Headers["Accept-Encoding"] = encoding
	}
}

// WithProxyReq 设置请求级代理
func WithProxyReq(proxy string) func(*Request) {
	return func(r *Request) { r.Proxy = proxy }
}

// WithImpersonateReq 设置请求级浏览器指纹
func WithImpersonateReq(bt BrowserType) func(*Request) {
	return func(r *Request) { r.Impersonate = bt }
}

// WithVerifyReq 设置请求级 TLS 验证
func WithVerifyReq(verify bool) func(*Request) {
	return func(r *Request) { r.Verify = &verify }
}

// WithMaxRedirectsReq 设置请求级最大重定向
func WithMaxRedirectsReq(n int) func(*Request) {
	return func(r *Request) { r.MaxRedirects = n }
}

// WithProxyAuth 设置代理认证
func WithProxyAuth(user, password string) func(*Request) {
	return func(r *Request) {
		if r.Headers == nil {
			r.Headers = make(map[string]string)
		}
		r.Headers["Proxy-Authorization"] = "Basic " + basicAuth(user, password)
	}
}

// ============================================================
// Response 缺失方法
// ============================================================

// IterLines 逐行迭代响应内容
func (r *Response) IterLines() []string {
	lines := strings.Split(string(r.Content), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// IterContent 按块迭代响应内容
func (r *Response) IterContent(chunkSize int) [][]byte {
	if chunkSize <= 0 {
		chunkSize = 1024
	}
	var chunks [][]byte
	for i := 0; i < len(r.Content); i += chunkSize {
		end := i + chunkSize
		if end > len(r.Content) {
			end = len(r.Content)
		}
		chunks = append(chunks, r.Content[i:end])
	}
	return chunks
}

// DownloadSize 返回下载字节数
func (r *Response) DownloadSize() int64 {
	return int64(len(r.Content))
}

// UploadSize 返回上传字节数（从请求头估算）
func (r *Response) UploadSize() int64 {
	return 0 // tls-client 不暴露此信息
}

// HeaderSize 返回响应头大小
func (r *Response) HeaderSize() int {
	total := 0
	for k, vs := range r.Headers {
		for _, v := range vs {
			total += len(k) + len(v) + 4 // ": " + "\r\n"
		}
	}
	return total
}

// ============================================================
// Cookie 管理
// ============================================================

// DeleteCookie 删除指定 cookie
func (s *Session) DeleteCookie(name string) {
	if s.cookies == nil {
		return
	}
	delete(s.cookies, name)
}

// ClearCookies 清除所有 cookies
func (s *Session) ClearCookies() {
	s.cookies = make(map[string]string)
}

// UpdateCookies 批量更新 cookies
func (s *Session) UpdateCookies(cookies map[string]string) {
	if s.cookies == nil {
		s.cookies = make(map[string]string)
	}
	for k, v := range cookies {
		s.cookies[k] = v
	}
}

// GetCookie 获取单个 cookie
func (s *Session) GetCookie(name string) string {
	return s.cookies[name]
}

// ============================================================
// AresClient 缺失参数
// ============================================================

// AresWithMaxRetries 设置最大重试次数
func AresWithMaxRetries(n int) func(*AresClient) {
	return func(c *AresClient) { c.MaxRetries = n }
}

// AresWithUseEdge 设置使用 Edge 浏览器
func AresWithUseEdge(useEdge bool) func(*AresClient) {
	return func(c *AresClient) {
		if useEdge {
			c.Impersonate = Edge101
		}
	}
}

// AresWithBrowserEngine 设置浏览器引擎类型（兼容 Python 的 browser_engine 参数）
func AresWithBrowserEngine(engine string) func(*AresClient) {
	return func(c *AresClient) {
		// Go 只有一种浏览器引擎（chromedp），忽略参数但保留兼容
	}
}

// ============================================================
// 模块级函数补齐
// ============================================================

// RequestQuick 发送一次性请求
func RequestQuick(method, targetURL string, impersonate BrowserType, proxy string) (*Response, error) {
	s, err := NewSession(WithImpersonate(impersonate), WithProxy(proxy))
	if err != nil {
		return nil, err
	}
	defer s.Close()
	return s.Request(&Request{Method: method, URL: targetURL})
}

// TraceQuick 发送一次性 TRACE 请求
func TraceQuick(targetURL string, impersonate BrowserType, proxy string) (*Response, error) {
	s, err := NewSession(WithImpersonate(impersonate), WithProxy(proxy))
	if err != nil {
		return nil, err
	}
	defer s.Close()
	return s.Trace(targetURL)
}

// ============================================================
// 辅助
// ============================================================

func basicAuth(user, password string) string {
	return encodeBase64(user + ":" + password)
}

func encodeBase64(s string) string {
	return stdBase64Encode([]byte(s))
}

// 使用标准库 base64
func stdBase64Encode(data []byte) string {
	const base64Table = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var result strings.Builder
	i := 0
	for i < len(data) {
		b1 := data[i]
		i++
		result.WriteByte(base64Table[b1>>2])
		if i < len(data) {
			b2 := data[i]
			i++
			result.WriteByte(base64Table[((b1&0x3)<<4)|(b2>>4)])
			if i < len(data) {
				b3 := data[i]
				i++
				result.WriteByte(base64Table[((b2&0xF)<<2)|(b3>>6)])
				result.WriteByte(base64Table[b3&0x3F])
			} else {
				result.WriteByte(base64Table[(b2&0xF)<<2])
				result.WriteByte('=')
			}
		} else {
			result.WriteByte(base64Table[(b1&0x3)<<4])
			result.WriteString("==")
		}
	}
	return result.String()
}

// suppress unused
var _ = json.Marshal
var _ = io.Discard
var _ = bytes.NewBuffer
var _ = http.NewRequest
var _ = url.Parse
var _ = time.Now
var _ = fmt.Sprintf
