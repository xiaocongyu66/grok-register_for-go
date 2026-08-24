package engine

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// EmailHandle holds mailbox info returned by Create().
type EmailHandle struct {
	Email    string
	Password string
	ID       string
}

// moemail 配置用 lazy 读取(因为 init() 在 loadEnvFile 之前执行)。
// 每次调用时从环境变量读,确保 .env 已加载。
func moemailAPI() string {
	return strings.TrimRight(envFirst("MOEMAIL_API"), "/")
}
func moemailKey() string {
	return envFirst("MOEMAIL_API_KEY")
}
func moemailDomain() string {
	return envFirst("MOEMAIL_DOMAIN")
}

var noProxyClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{Proxy: nil},
}

// CreateMailbox creates a temp email via MoeMail API (no proxy).
func CreateMailbox() (*EmailHandle, error) {
	name := "oc" + randomHex(4)
	payload, _ := json.Marshal(map[string]any{
		"name":       name,
		"domain":     moemailDomain(),
		"expiryTime": 3600000,
	})

	req, _ := http.NewRequest("POST", moemailAPI()+"/api/emails/generate", strings.NewReader(string(payload)))
	req.Header.Set("X-API-Key", moemailKey())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := noProxyClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("moemail create: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("moemail http %d: %s", resp.StatusCode, string(body[:min(200, len(body))]))
	}

	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("moemail json: %w", err)
	}

	email, _ := data["email"].(string)
	if email == "" {
		email, _ = data["address"].(string)
	}
	id, _ := data["id"].(string)
	if id == "" {
		id = email
	}
	if email == "" {
		return nil, fmt.Errorf("moemail: no email in response")
	}

	return &EmailHandle{
		Email:    email,
		Password: randomPassword(16),
		ID:       id,
	}, nil
}

// PollEmailCode polls MoeMail for verification code (no proxy).
func PollEmailCode(handle string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest("GET", moemailAPI()+"/api/emails/"+handle, nil)
		req.Header.Set("X-API-Key", moemailKey())
		req.Header.Set("Accept", "application/json")
		resp, err := noProxyClient.Do(req)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			code := findCode(string(body))
			if code != "" {
				return code, nil
			}
		}
		time.Sleep(2500 * time.Millisecond)
	}
	return "", fmt.Errorf("email code timeout")
}

var codeRe1 = regexp.MustCompile(`\b([A-Z0-9]{3})-([A-Z0-9]{3})\b`)
var codeRe2 = regexp.MustCompile(`\b([A-Z0-9]{6})\b`)

func findCode(text string) string {
	upper := strings.ToUpper(text)
	if m := codeRe1.FindStringSubmatch(upper); len(m) >= 3 {
		return m[1] + m[2]
	}
	if m := codeRe2.FindStringSubmatch(upper); len(m) >= 2 {
		return m[1]
	}
	return ""
}

func randomPassword(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%"
	b := make([]byte, n)
	for i := range b {
		b[i] = chars[secureRandInt(len(chars))]
	}
	return string(b)
}

func randomHex(n int) string {
	b := make([]byte, n)
	secureRandRead(b)
	return hex.EncodeToString(b)
}
