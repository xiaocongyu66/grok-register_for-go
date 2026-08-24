package engine

import (
	"os"
	"strings"
	"testing"
)

// 读取一个已知是登录票据的 token(从 keys/ 目录),验证 isSessionSSO 返回 false。
func TestIsSessionSSORejectsLoginTicket(t *testing.T) {
	data, err := os.ReadFile("../../../keys/202608220027-grok2api.txt")
	if err != nil {
		t.Skipf("跳过:测试文件不存在 (%v)", err)
	}
	tok := strings.TrimSpace(strings.Split(string(data), "\n")[0])
	if tok == "" {
		t.Skip("跳过:token 为空")
	}
	if isSessionSSO(tok) {
		t.Errorf("isSessionSSO 对登录票据(含 config)应返回 false,但返回了 true")
	}
}

// 真正的 SSO cookie payload 应含 session_id,isSessionSSO 应返回 true。
func TestIsSessionSSOAcceptsSessionToken(t *testing.T) {
	// payload: {"session_id":"test-session-123"}
	header := `{"alg":"none"}`
	payload := `{"session_id":"abc-123"}`
	tok := b64url(header) + "." + b64url(payload) + "."
	if !isSessionSSO(tok) {
		t.Errorf("isSessionSSO 对 session SSO 应返回 true,但返回 false。token=%s", tok)
	}
}

// SSO cookie payload 可能是其他结构(不一定含 session_id),只要不含 config 就接受。
func TestIsSessionSSOAcceptsOtherPayload(t *testing.T) {
	// payload: {"sub":"user-123","iat":1700000000} —— 无 session_id 但也不是登录票据
	header := `{"alg":"none"}`
	payload := `{"sub":"user-123","iat":1700000000}`
	tok := b64url(header) + "." + b64url(payload) + "."
	if !isSessionSSO(tok) {
		t.Errorf("isSessionSSO 对非登录票据 JWT 应返回 true,但返回 false。token=%s", tok)
	}
}

func b64url(s string) string {
	const tbl = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	out := make([]byte, 0, len(s)*4/3+4)
	val := 0
	bits := 0
	for i := 0; i < len(s); i++ {
		val = (val << 8) | int(s[i])
		bits += 8
		for bits >= 6 {
			bits -= 6
			out = append(out, tbl[(val>>bits)&0x3F])
		}
	}
	if bits > 0 {
		out = append(out, tbl[(val<<(6-bits))&0x3F])
	}
	return string(out)
}
