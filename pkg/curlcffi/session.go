package curlcffi

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultSessionTTL 是会话默认有效期（秒），对应 Python 的 session_ttl = 3600。
const DefaultSessionTTL = 3600

// SessionEntry 对应 Python 中 sessions[domain] 的 value：
// {cookies, headers, timestamp}
type SessionEntry struct {
	Cookies    map[string]string
	Headers    map[string]string
	Timestamp  time.Time
	expiration time.Time
}

// SessionManager 管理不同域名的 cookies/headers，带 TTL。
type SessionManager struct {
	mu         sync.Mutex
	sessions   map[string]*SessionEntry
	sessionTTL int
}

// NewSessionManager 创建一个 SessionManager，使用默认 TTL。
func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions:   make(map[string]*SessionEntry),
		sessionTTL: DefaultSessionTTL,
	}
}

// NewSessionManagerWithTTL 创建一个 SessionManager，使用自定义 TTL（秒）。
func NewSessionManagerWithTTL(ttl int) *SessionManager {
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	return &SessionManager{
		sessions:   make(map[string]*SessionEntry),
		sessionTTL: ttl,
	}
}

// SetSessionTTL 设置会话 TTL（秒）。影响后续更新与校验。
func (sm *SessionManager) SetSessionTTL(ttl int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if ttl > 0 {
		sm.sessionTTL = ttl
	}
}

// GetSessionTTL 返回当前会话 TTL（秒）。
func (sm *SessionManager) GetSessionTTL() int {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.sessionTTL
}

// getDomain 从 URL 中提取域名（host），对应 Python 的 _get_domain(url)。
// 传入的 url 可以带或不含 scheme；不含 scheme 时补上 "//" 以便解析。
// 失败时返回空字符串。
func getDomain(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := u.Host
	if host == "" {
		// 尝试补 scheme
		u2, err2 := url.Parse("//" + rawURL)
		if err2 != nil {
			return ""
		}
		host = u2.Host
	}
	// 去掉端口
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	return strings.ToLower(strings.TrimSpace(host))
}

// GetDomain 是对外暴露的域名提取方法。
func (sm *SessionManager) GetDomain(rawURL string) string {
	return getDomain(rawURL)
}

// Update 更新某域名的会话（cookies 与 headers），对应 Python 的 update(url, cookies, headers)。
// 传入的 cookies/headers 可以为 nil，表示不更新对应部分。
func (sm *SessionManager) Update(rawURL string, cookies map[string]string, headers map[string]string) {
	domain := getDomain(rawURL)
	if domain == "" {
		return
	}
	now := time.Now()
	ttl := sm.GetSessionTTL()

	sm.mu.Lock()
	defer sm.mu.Unlock()

	entry, ok := sm.sessions[domain]
	if !ok {
		entry = &SessionEntry{
			Cookies: make(map[string]string),
			Headers: make(map[string]string),
		}
		sm.sessions[domain] = entry
	}

	if cookies != nil {
		if entry.Cookies == nil {
			entry.Cookies = make(map[string]string)
		}
		for k, v := range cookies {
			entry.Cookies[k] = v
		}
	}
	if headers != nil {
		if entry.Headers == nil {
			entry.Headers = make(map[string]string)
		}
		for k, v := range headers {
			entry.Headers[k] = v
		}
	}

	entry.Timestamp = now
	entry.expiration = now.Add(time.Duration(ttl) * time.Second)
}

// GetCookies 获取某域名的 cookies，对应 Python 的 get_cookies(url)。
// 若会话不存在或已过期，返回 nil。
func (sm *SessionManager) GetCookies(rawURL string) map[string]string {
	domain := getDomain(rawURL)
	if domain == "" {
		return nil
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	entry, ok := sm.sessions[domain]
	if !ok || !sm.isValidLocked(entry) {
		return nil
	}
	// 返回拷贝，避免外部修改内部状态
	out := make(map[string]string, len(entry.Cookies))
	for k, v := range entry.Cookies {
		out[k] = v
	}
	return out
}

// GetHeaders 获取某域名的 headers，对应 Python 的 get_headers(url)。
// 若会话不存在或已过期，返回 nil。
func (sm *SessionManager) GetHeaders(rawURL string) map[string]string {
	domain := getDomain(rawURL)
	if domain == "" {
		return nil
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	entry, ok := sm.sessions[domain]
	if !ok || !sm.isValidLocked(entry) {
		return nil
	}
	out := make(map[string]string, len(entry.Headers))
	for k, v := range entry.Headers {
		out[k] = v
	}
	return out
}

// HasValidSession 检查某域名是否有有效会话（未过期），
// 对应 Python 的 has_valid_session(url)。
func (sm *SessionManager) HasValidSession(rawURL string) bool {
	domain := getDomain(rawURL)
	if domain == "" {
		return false
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	entry, ok := sm.sessions[domain]
	if !ok {
		return false
	}
	return sm.isValidLocked(entry)
}

// isValidLocked 判断会话是否有效（未过期），调用者必须持有锁。
func (sm *SessionManager) isValidLocked(entry *SessionEntry) bool {
	if entry == nil {
		return false
	}
	if entry.expiration.IsZero() {
		// 兼容旧数据：用 Timestamp + TTL 判断
		return time.Since(entry.Timestamp) < time.Duration(sm.sessionTTL)*time.Second
	}
	return time.Now().Before(entry.expiration)
}

// Clear 清除会话，对应 Python 的 clear(url=None)。
// 当 url 为空字符串时，清除全部会话；否则仅清除该域名对应的会话。
func (sm *SessionManager) Clear(rawURL string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if rawURL == "" {
		sm.sessions = make(map[string]*SessionEntry)
		return
	}
	domain := getDomain(rawURL)
	if domain == "" {
		return
	}
	delete(sm.sessions, domain)
}

// ClearAll 清除所有会话。
func (sm *SessionManager) ClearAll() {
	sm.Clear("")
}

// Domains 返回当前持有的所有域名（不论是否过期）。
func (sm *SessionManager) Domains() []string {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	out := make([]string, 0, len(sm.sessions))
	for d := range sm.sessions {
		out = append(out, d)
	}
	return out
}

// String 返回 SessionManager 的简要描述。
func (sm *SessionManager) String() string {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return fmt.Sprintf("SessionManager(sessions=%d, ttl=%ds)", len(sm.sessions), sm.sessionTTL)
}
