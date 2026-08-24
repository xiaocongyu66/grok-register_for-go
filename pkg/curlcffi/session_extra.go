package curlcffi

// session_extra.go — Session 补齐: query/upkeep/executor/loop + Response.markdown

import (
	"fmt"
	"strings"
)

// Query 发送 HTTP QUERY 请求（RFC 9110 方法）
func (s *Session) Query(targetURL string, opts ...func(*Request)) (*Response, error) {
	req := &Request{Method: "QUERY", URL: targetURL}
	for _, opt := range opts {
		opt(req)
	}
	return s.Request(req)
}

// Upkeep 维护会话（清理空闲连接、刷新 DNS 缓存等）
func (s *Session) Upkeep() {
	// tls-client 内部自动管理连接池
	// 这里只做 cookie/header 的清理
	if s.discardCookies {
		s.ClearCookies()
	}
}

// Executor 返回同步执行器（Go 没有异步执行器，返回 session 自身）
func (s *Session) Executor() *Session {
	return s
}

// Loop 返回事件循环（Go 没有事件循环，返回 nil）
func (s *Session) Loop() interface{} {
	return nil
}

// --- Response 补齐 ---

// Markdown 将 HTML 响应体转换为 Markdown（简单实现）
func (r *Response) Markdown() string {
	text := r.Text()
	// 简单 HTML→Markdown 转换
	text = strings.ReplaceAll(text, "<br>", "\n")
	text = strings.ReplaceAll(text, "<br/>", "\n")
	text = strings.ReplaceAll(text, "</p>", "\n\n")
	text = strings.ReplaceAll(text, "</h1>", "\n\n")
	text = strings.ReplaceAll(text, "</h2>", "\n\n")
	text = strings.ReplaceAll(text, "</h3>", "\n\n")
	text = strings.ReplaceAll(text, "</li>", "\n")
	text = strings.ReplaceAll(text, "</div>", "\n")
	// 移除所有 HTML 标签
	text = stripHTMLTags(text)
	// 压缩多余空行
	for strings.Contains(text, "\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(text)
}

// stripHTMLTags 移除 HTML 标签
func stripHTMLTags(s string) string {
	var result strings.Builder
	inTag := false
	for _, c := range s {
		if c == '<' {
			inTag = true
			continue
		}
		if c == '>' {
			inTag = false
			continue
		}
		if !inTag {
			result.WriteRune(c)
		}
	}
	return result.String()
}

// Queue 返回响应队列（Go 不需要，返回 nil）
func (r *Response) Queue() interface{} {
	return nil
}

// Suppress unused
var _ = fmt.Sprintf
