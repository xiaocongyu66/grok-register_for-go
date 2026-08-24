package curlcffi

import (
	"errors"
	"fmt"
)

// AresError 是所有 Ares 相关错误的基础类型，对应 Python 的 AresError。
type AresError struct {
	SubKind string // 子类型名称，如 "CloudflareError"、"BrowserError"
	Msg     string // 错误描述
}

func (e *AresError) Error() string {
	if e.SubKind == "" {
		if e.Msg == "" {
			return "ares error"
		}
		return e.Msg
	}
	if e.Msg == "" {
		return fmt.Sprintf("%s", e.SubKind)
	}
	return fmt.Sprintf("%s: %s", e.SubKind, e.Msg)
}

// NewAresError 创建一个通用的 AresError。
func NewAresError(msg string) *AresError {
	return &AresError{SubKind: "AresError", Msg: msg}
}

// CloudflareError 对应 Python 的 CloudflareError。
type CloudflareError struct {
	Msg string
}

func (e *CloudflareError) Error() string {
	if e.Msg == "" {
		return "cloudflare error"
	}
	return fmt.Sprintf("CloudflareError: %s", e.Msg)
}

// NewCloudflareError 创建 CloudflareError。
func NewCloudflareError(msg string) *CloudflareError {
	return &CloudflareError{Msg: msg}
}

// BrowserError 对应 Python 的 BrowserError。
type BrowserError struct {
	Msg string
}

func (e *BrowserError) Error() string {
	if e.Msg == "" {
		return "browser error"
	}
	return fmt.Sprintf("BrowserError: %s", e.Msg)
}

// NewBrowserError 创建 BrowserError。
func NewBrowserError(msg string) *BrowserError {
	return &BrowserError{Msg: msg}
}

// SessionError 对应 Python 的 SessionError。
type SessionError struct {
	Msg string
}

func (e *SessionError) Error() string {
	if e.Msg == "" {
		return "session error"
	}
	return fmt.Sprintf("SessionError: %s", e.Msg)
}

// NewSessionError 创建 SessionError。
func NewSessionError(msg string) *SessionError {
	return &SessionError{Msg: msg}
}

// RequestError 对应 Python 的 RequestError。
type RequestError struct {
	Msg string
}

func (e *RequestError) Error() string {
	if e.Msg == "" {
		return "request error"
	}
	return fmt.Sprintf("RequestError: %s", e.Msg)
}

// NewRequestError 创建 RequestError。
func NewRequestError(msg string) *RequestError {
	return &RequestError{Msg: msg}
}

// ProxyError 对应 Python 的 ProxyError。
type ProxyError struct {
	Msg string
}

func (e *ProxyError) Error() string {
	if e.Msg == "" {
		return "proxy error"
	}
	return fmt.Sprintf("ProxyError: %s", e.Msg)
}

// NewProxyError 创建 ProxyError。
func NewProxyError(msg string) *ProxyError {
	return &ProxyError{Msg: msg}
}

// CloudflareChallengeFailed 对应 Python 的 CloudflareChallengeFailed。
// 作为独立的 sentinel error，同时提供带消息的构造函数。
var ErrCloudflareChallengeFailed = errors.New("cloudflare challenge failed")

// CloudflareChallengeFailedError 是可携带自定义消息的版本。
type CloudflareChallengeFailedError struct {
	Msg string
}

func (e *CloudflareChallengeFailedError) Error() string {
	if e.Msg == "" {
		return "cloudflare challenge failed"
	}
	return fmt.Sprintf("CloudflareChallengeFailed: %s", e.Msg)
}

// NewCloudflareChallengeFailed 创建 CloudflareChallengeFailedError。
func NewCloudflareChallengeFailed(msg string) *CloudflareChallengeFailedError {
	return &CloudflareChallengeFailedError{Msg: msg}
}

// CloudflareSessionExpired 对应 Python 的 CloudflareSessionExpired。
var ErrCloudflareSessionExpired = errors.New("cloudflare session expired")

// CloudflareSessionExpiredError 是可携带自定义消息的版本。
type CloudflareSessionExpiredError struct {
	Msg string
}

func (e *CloudflareSessionExpiredError) Error() string {
	if e.Msg == "" {
		return "cloudflare session expired"
	}
	return fmt.Sprintf("CloudflareSessionExpired: %s", e.Msg)
}

// NewCloudflareSessionExpired 创建 CloudflareSessionExpiredError。
func NewCloudflareSessionExpired(msg string) *CloudflareSessionExpiredError {
	return &CloudflareSessionExpiredError{Msg: msg}
}

// BrowserInitializationError 对应 Python 的 BrowserInitializationError。
var ErrBrowserInitializationError = errors.New("browser initialization error")

// BrowserInitializationErrorType 是可携带自定义消息的版本。
type BrowserInitializationErrorType struct {
	Msg string
}

func (e *BrowserInitializationErrorType) Error() string {
	if e.Msg == "" {
		return "browser initialization error"
	}
	return fmt.Sprintf("BrowserInitializationError: %s", e.Msg)
}

// NewBrowserInitializationError 创建 BrowserInitializationErrorType。
func NewBrowserInitializationError(msg string) *BrowserInitializationErrorType {
	return &BrowserInitializationErrorType{Msg: msg}
}

// --- curl_cffi.requests.exceptions 补齐 ---

// TooManyRedirectsError 重定向次数过多
type TooManyRedirectsError struct{ Msg string }

func (e *TooManyRedirectsError) Error() string { return "too many redirects: " + e.Msg }

// IncompleteReadError 响应体未完整读取
type IncompleteReadError struct{ Msg string }

func (e *IncompleteReadError) Error() string { return "incomplete read: " + e.Msg }

// MissingSchemaError URL 缺少协议
type MissingSchemaError struct{ Msg string }

func (e *MissingSchemaError) Error() string { return "missing schema: " + e.Msg }

// FileModeWarning 文件模式警告
type FileModeWarning struct{ Msg string }

func (e *FileModeWarning) Error() string { return "file mode warning: " + e.Msg }

// --- Sentinel errors ---

var (
	ErrTooManyRedirects  = errors.New("too many redirects")
	ErrIncompleteRead    = errors.New("incomplete read")
	ErrMissingSchema     = errors.New("missing schema")
	ErrSessionClosed     = errors.New("session is closed")
	ErrStreamConsumed    = errors.New("stream already consumed")
	ErrRetryError        = errors.New("retry exhausted")
	ErrInvalidURL        = errors.New("invalid URL")
	ErrInvalidProxyURL   = errors.New("invalid proxy URL")
	ErrInvalidJSON       = errors.New("invalid JSON response")
	ErrChunkedEncoding   = errors.New("chunked encoding error")
	ErrContentDecoding   = errors.New("content decoding error")
	ErrConnectTimeout    = errors.New("connect timeout")
	ErrReadTimeout       = errors.New("read timeout")
	ErrConnectionError   = errors.New("connection error")
	ErrSSLError          = errors.New("SSL error")
	ErrCertificateVerify = errors.New("certificate verification error")
	ErrImpersonateError  = errors.New("impersonate error")
	ErrInterfaceError    = errors.New("interface error")
	ErrDNSError          = errors.New("DNS resolution error")
	ErrInvalidHeader     = errors.New("invalid header")
	ErrUnrewindableBody  = errors.New("unrewindable body")
	ErrURLRequired       = errors.New("URL required")
	ErrInvalidSchema     = errors.New("invalid schema")
)

// AsAresError 尝试把任意 error 转换为 *AresError，成功返回 true。
// 用于在调用方判断错误是否属于 Ares 体系。
func AsAresError(err error) (*AresError, bool) {
	if err == nil {
		return nil, false
	}
	var ae *AresError
	if errors.As(err, &ae) {
		return ae, true
	}
	return nil, false
}

// IsAresError 判断 error 是否属于 Ares 体系（AresError 或其派生类型，
// 或是独立的 CloudflareChallengeFailed/CloudflareSessionExpired/BrowserInitializationError）。
func IsAresError(err error) bool {
	if err == nil {
		return false
	}
	var (
		ae  *AresError
		ce  *CloudflareError
		be  *BrowserError
		se  *SessionError
		re  *RequestError
		pe  *ProxyError
		ccf *CloudflareChallengeFailedError
		cse *CloudflareSessionExpiredError
		bie *BrowserInitializationErrorType
	)
	if errors.As(err, &ae) || errors.As(err, &ce) || errors.As(err, &be) ||
		errors.As(err, &se) || errors.As(err, &re) || errors.As(err, &pe) ||
		errors.As(err, &ccf) || errors.As(err, &cse) || errors.As(err, &bie) {
		return true
	}
	return errors.Is(err, ErrCloudflareChallengeFailed) ||
		errors.Is(err, ErrCloudflareSessionExpired) ||
		errors.Is(err, ErrBrowserInitializationError)
}
