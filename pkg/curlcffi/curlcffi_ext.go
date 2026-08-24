package curlcffi

// curlcffi_ext.go — 补齐 curl_cffi Python API 的全部缺失功能
// 不重复 curlcffi.go 中已有的类型/函数

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// --- Response 扩展 ---

// ContentLength 返回 Content-Length
func (r *Response) ContentLength() int64 {
	if cl := r.Headers.Get("Content-Length"); cl != "" {
		var n int64
		fmt.Sscanf(cl, "%d", &n)
		return n
	}
	return int64(len(r.Content))
}

// ContentType 返回 Content-Type
func (r *Response) ContentType() string {
	return r.Headers.Get("Content-Type")
}

// CookiesMap 返回 cookies 为 map
func (r *Response) CookiesMap() map[string]string {
	m := make(map[string]string)
	for _, c := range r.Cookies {
		m[c.Name] = c.Value
	}
	return m
}

// --- RetryStrategy ---

type RetryStrategy struct {
	Count      int
	Backoff    time.Duration
	MaxBackoff time.Duration
	RetryOn    []int
}

func DefaultRetry() RetryStrategy {
	return RetryStrategy{Count: 0, Backoff: 500 * time.Millisecond, MaxBackoff: 10 * time.Second, RetryOn: []int{429, 502, 503}}
}

// --- Session 选项补齐 ---

func WithBaseURL(baseURL string) func(*Session) {
	return func(s *Session) { s.baseURL = baseURL }
}

func WithMaxRedirects(n int) func(*Session) {
	return func(s *Session) { s.maxRedirects = n }
}

func WithVerify(verify bool) func(*Session) {
	return func(s *Session) { s.verify = verify }
}

func WithRetry(strategy RetryStrategy) func(*Session) {
	return func(s *Session) { s.retry = strategy }
}

func WithAuth(user, password string) func(*Session) {
	return func(s *Session) { s.auth = [2]string{user, password} }
}

func WithReferer(referer string) func(*Session) {
	return func(s *Session) { s.referer = referer }
}

func WithDebug(debug bool) func(*Session) {
	return func(s *Session) { s.debug = debug }
}

func WithDohURL(dohURL string) func(*Session) {
	return func(s *Session) { s.dohURL = dohURL }
}

// --- Request 选项补齐 ---

func WithFiles(files []FileField) func(*Request) {
	return func(r *Request) { r.Files = files }
}

func WithRefererReq(referer string) func(*Request) {
	return func(r *Request) { r.Referer = referer }
}

func WithAuthReq(user, password string) func(*Request) {
	return func(r *Request) { r.Auth = [2]string{user, password} }
}

func WithAllowRedirects(allow bool) func(*Request) {
	return func(r *Request) { r.AllowRedirects = &allow }
}

func WithForm(form map[string]string) func(*Request) {
	return func(r *Request) {
		vals := url.Values{}
		for k, v := range form {
			vals.Set(k, v)
		}
		r.Data = []byte(vals.Encode())
		if r.Headers == nil {
			r.Headers = make(map[string]string)
		}
		r.Headers["Content-Type"] = "application/x-www-form-urlencoded"
	}
}

// --- Session 方法补齐 ---

// Trace 发送 TRACE 请求
func (s *Session) Trace(targetURL string, opts ...func(*Request)) (*Response, error) {
	req := &Request{Method: "TRACE", URL: targetURL}
	for _, opt := range opts {
		opt(req)
	}
	return s.Request(req)
}

// RequestWithRetry 带重试的请求
func (s *Session) RequestWithRetry(req *Request) (*Response, error) {
	if s.closed {
		return nil, fmt.Errorf("session is closed")
	}

	strategy := s.retry
	if strategy.Count <= 0 {
		return s.Request(req)
	}

	var lastResp *Response
	var lastErr error

	for attempt := 0; attempt <= strategy.Count; attempt++ {
		if attempt > 0 {
			backoff := strategy.Backoff * time.Duration(attempt)
			if backoff > strategy.MaxBackoff {
				backoff = strategy.MaxBackoff
			}
			time.Sleep(backoff)
			if s.debug {
				fmt.Printf("[curlcffi] retry %d/%d after %v\n", attempt, strategy.Count, backoff)
			}
		}

		resp, err := s.Request(req)
		if err != nil {
			lastErr = err
			continue
		}

		shouldRetry := false
		for _, code := range strategy.RetryOn {
			if resp.StatusCode == code {
				shouldRetry = true
				break
			}
		}

		if !shouldRetry {
			return resp, nil
		}

		lastResp = resp
		lastErr = nil
		if s.debug {
			fmt.Printf("[curlcffi] retry %d: status %d\n", attempt+1, resp.StatusCode)
		}
	}

	if lastResp != nil {
		return lastResp, nil
	}
	return nil, lastErr
}

// resolveURL 处理 base_url 拼接
func (s *Session) resolveURL(targetURL string) string {
	if s.baseURL == "" {
		return targetURL
	}
	if strings.HasPrefix(targetURL, "http://") || strings.HasPrefix(targetURL, "https://") {
		return targetURL
	}
	return strings.TrimRight(s.baseURL, "/") + "/" + strings.TrimLeft(targetURL, "/")
}

// --- AresClient 补齐 ---

// Patch 发送 PATCH 请求
func (c *AresClient) Patch(targetURL string, data []byte, headers map[string]string) (*AresResponse, error) {
	return c.request("PATCH", targetURL, nil, data, headers)
}

// Head 发送 HEAD 请求
func (c *AresClient) Head(targetURL string, headers map[string]string) (*AresResponse, error) {
	return c.request("HEAD", targetURL, nil, nil, headers)
}

// Options 发送 OPTIONS 请求
func (c *AresClient) Options(targetURL string, headers map[string]string) (*AresResponse, error) {
	return c.request("OPTIONS", targetURL, nil, nil, headers)
}

// --- 模块级补齐 ---

func PutQuick(targetURL string, data []byte, impersonate BrowserType, proxy string) (*Response, error) {
	s, err := NewSession(WithImpersonate(impersonate), WithProxy(proxy))
	if err != nil {
		return nil, err
	}
	defer s.Close()
	return s.Put(targetURL, WithData(data))
}

func DeleteQuick(targetURL string, impersonate BrowserType, proxy string) (*Response, error) {
	s, err := NewSession(WithImpersonate(impersonate), WithProxy(proxy))
	if err != nil {
		return nil, err
	}
	defer s.Close()
	return s.Delete(targetURL)
}

func HeadQuick(targetURL string, impersonate BrowserType, proxy string) (*Response, error) {
	s, err := NewSession(WithImpersonate(impersonate), WithProxy(proxy))
	if err != nil {
		return nil, err
	}
	defer s.Close()
	return s.Head(targetURL)
}

func PatchQuick(targetURL string, data []byte, impersonate BrowserType, proxy string) (*Response, error) {
	s, err := NewSession(WithImpersonate(impersonate), WithProxy(proxy))
	if err != nil {
		return nil, err
	}
	defer s.Close()
	return s.Patch(targetURL, WithData(data))
}

func OptionsQuick(targetURL string, impersonate BrowserType, proxy string) (*Response, error) {
	s, err := NewSession(WithImpersonate(impersonate), WithProxy(proxy))
	if err != nil {
		return nil, err
	}
	defer s.Close()
	return s.Options(targetURL)
}

// --- 辅助函数 ---

func BasicAuth(user, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password))
}

func ParseProxy(proxyStr string) (*url.URL, error) {
	return url.Parse(proxyStr)
}

func ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
