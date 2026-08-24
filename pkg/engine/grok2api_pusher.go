package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Grok2APIPusher 把注册成功的 SSO token 推送到 grok2api 服务器。
// 通过环境变量配置:
//
//	GROK2API_PUSH_ENABLED=1          启用推送(默认关闭)
//	GROK2API_BASE_URL=http://host:port grok2api 服务地址
//	GROK2API_ADMIN_TOKEN=...         管理员 accessToken(用于登录后调用 admin API)
//	GROK2API_ADMIN_USER=admin        (可选)用户名,优先用 token
//	GROK2API_ADMIN_PASS=...          (可选)密码,token 为空时自动登录获取
//	GROK2API_PROVIDER=grok_web       推送目标:grok_web | grok_build | grok_console
//	GROK2API_PROXY_SCOPE=grok_web   (可选)同时导入代理的 scope,留空则不导入代理
//	GROK2API_PROXY_POOL_FILE=...    (可选)代理文件路径,推送时一并导入
//	GROK2API_PROXY_ASSIGN=1          (可选)推送账号后自动分配到代理节点(mode=auto)
//	GROK2API_TIMEOUT=30              (可选)单次请求超时秒数
type Grok2APIPusher struct {
	baseURL   string
	token     string
	provider  string
	assign    bool
	timeout   time.Duration

	// 运行时状态
	mu       sync.Mutex
	client   *http.Client
	nodeIDs  []uint64 // 已导入的代理节点 ID,用于自动分配
	resolved bool     // token 是否已解析
}

var (
	grok2apiPusherOnce sync.Once
	grok2apiPusherInst *Grok2APIPusher
)

// Grok2APIPushEnabled 返回是否启用了 grok2api 推送。
func Grok2APIPushEnabled() bool {
	return envBool("GROK2API_PUSH_ENABLED", false)
}

// GetGrok2APIPusher 返回单例 pusher(首次调用时解析配置并可能登录)。
func GetGrok2APIPusher() *Grok2APIPusher {
	grok2apiPusherOnce.Do(func() {
		grok2apiPusherInst = &Grok2APIPusher{
			baseURL:  strings.TrimRight(strings.TrimSpace(os.Getenv("GROK2API_BASE_URL")), "/"),
			token:   strings.TrimSpace(os.Getenv("GROK2API_ADMIN_TOKEN")),
			provider: envFirst("GROK2API_PROVIDER", "GROK2API_PUSH_PROVIDER"),
			assign:   envBool("GROK2API_PROXY_ASSIGN", false),
			timeout:  time.Duration(envInt("GROK2API_TIMEOUT", 30)) * time.Second,
		}
		if grok2apiPusherInst.provider == "" {
			grok2apiPusherInst.provider = "grok_web"
		}
		if grok2apiPusherInst.timeout <= 0 {
			grok2apiPusherInst.timeout = 30 * time.Second
		}
		grok2apiPusherInst.client = &http.Client{Timeout: grok2apiPusherInst.timeout}
	})
	return grok2apiPusherInst
}

// ensureResolved 懒加载:如果没 token,尝试用用户名密码登录获取。
func (p *Grok2APIPusher) ensureResolved() error {
	if p.resolved {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.resolved {
		return nil
	}
	if p.baseURL == "" {
		return fmt.Errorf("GROK2API_BASE_URL 未配置")
	}
	if p.token == "" {
		user := strings.TrimSpace(os.Getenv("GROK2API_ADMIN_USER"))
		pass := strings.TrimSpace(os.Getenv("GROK2API_ADMIN_PASS"))
		if user == "" || pass == "" {
			return fmt.Errorf("GROK2API_ADMIN_TOKEN 未配置,且未提供 GROK2API_ADMIN_USER/PASS 用于登录")
		}
		tok, err := p.login(user, pass)
		if err != nil {
			return fmt.Errorf("登录 grok2api 失败: %w", err)
		}
		p.token = tok
	}
	// 如果配置了代理池文件,导入代理并记录节点 ID
	if pf := strings.TrimSpace(os.Getenv("GROK2API_PROXY_POOL_FILE")); pf != "" {
		if _, err := os.Stat(pf); err == nil {
			if err := p.importProxyFile(pf); err != nil {
				fmt.Printf("[grok2api] 代理导入失败: %v\n", err)
			}
		}
	}
	p.resolved = true
	return nil
}

func (p *Grok2APIPusher) login(user, pass string) (string, error) {
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	req, err := http.NewRequest("POST", p.baseURL+"/api/admin/v1/auth/login", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("http %d: %s", resp.StatusCode, string(data))
	}
	var parsed struct {
		Data struct {
			Tokens struct {
				AccessToken string `json:"accessToken"`
			} `json:"tokens"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", err
	}
	if parsed.Data.Tokens.AccessToken == "" {
		return "", fmt.Errorf("响应无 accessToken: %s", string(data))
	}
	return parsed.Data.Tokens.AccessToken, nil
}

// importProxyFile 导入代理文件到 grok2api(scope 由 GROK2API_PROXY_SCOPE 决定),
// 并把新建的节点 ID 记录到 pusher,用于后续账号自动分配。
func (p *Grok2APIPusher) importProxyFile(path string) error {
	scope := strings.TrimSpace(os.Getenv("GROK2API_PROXY_SCOPE"))
	if scope == "" {
		scope = p.provider
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	name := "grok-free-register-" + nowUTC()
	payload, _ := json.Marshal(map[string]any{
		"name":    name,
		"scope":   scope,
		"content": string(content),
	})
	req, _ := http.NewRequest("POST", p.baseURL+"/api/admin/v1/egress-imports", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 201 {
		return fmt.Errorf("http %d: %s", resp.StatusCode, string(data))
	}
	// 查询刚导入的节点(按 name 前缀匹配),记录 ID 供后续分配
	if err := p.fetchNodeIDs(name, scope); err != nil {
		fmt.Printf("[grok2api] 代理已导入,但查询节点 ID 失败: %v\n", err)
	}
	fmt.Printf("[grok2api] 代理导入完成 scope=%s\n", scope)
	return nil
}

func (p *Grok2APIPusher) fetchNodeIDs(namePrefix, scope string) error {
	// 分页拉取所有节点,过滤 name 前缀
	p.mu.Lock()
	p.nodeIDs = p.nodeIDs[:0]
	p.mu.Unlock()
	page := 1
	for {
		url := fmt.Sprintf("%s/api/admin/v1/egress-nodes?pageSize=500&page=%d", p.baseURL, page)
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("Authorization", "Bearer "+p.token)
		resp, err := p.client.Do(req)
		if err != nil {
			return err
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("http %d: %s", resp.StatusCode, string(data))
		}
		var parsed struct {
			Data struct {
				Items []struct {
					ID    uint64 `json:"id,string"`
					Name  string `json:"name"`
					Scope string `json:"scope"`
				} `json:"items"`
				Total int `json:"total"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &parsed); err != nil {
			return err
		}
		for _, n := range parsed.Data.Items {
			if (scope == "" || n.Scope == scope) && strings.HasPrefix(n.Name, namePrefix) {
				p.mu.Lock()
				p.nodeIDs = append(p.nodeIDs, n.ID)
				p.mu.Unlock()
			}
		}
		collected := 0
		p.mu.Lock()
		collected = len(p.nodeIDs)
		p.mu.Unlock()
		if collected >= parsed.Data.Total || len(parsed.Data.Items) == 0 {
			break
		}
		page++
		if page > 50 {
			break
		}
	}
	return nil
}

// PushAccount 推送单个 SSO token 到 grok2api。
// 返回 (created, error)。如果 token 无效(grok2api 同步失败),返回错误。
func (p *Grok2APIPusher) PushAccount(sso string) (int, error) {
	if !isSessionSSO(sso) {
		return 0, fmt.Errorf("token 不是有效的 session SSO(可能是未兑换的登录票据)")
	}
	if err := p.ensureResolved(); err != nil {
		return 0, err
	}
	// 最多重试 2 次:第一次 401 时清 token 缓存重新登录
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		created, retry, err := p.doPush(sso, attempt > 0)
		if err == nil {
			return created, nil
		}
		lastErr = err
		if !retry {
			// 非 401 错误,不重试
			return created, err
		}
		// 401:清 token 缓存,ensureResolved 会重新登录
		p.mu.Lock()
		p.token = ""
		p.resolved = false
		p.mu.Unlock()
		if err := p.ensureResolved(); err != nil {
			return 0, err
		}
	}
	return 0, lastErr
}

// doPush 执行一次推送。返回 (created, retry, err):
//   - retry=true 表示遇到 401,调用方应清 token 缓存重试
//   - created 为 grok2api 返回的创建数
func (p *Grok2APIPusher) doPush(sso string, isRetry bool) (int, bool, error) {
	// 写临时文件,multipart 上传
	tmp, err := os.CreateTemp("", "grok2api-sso-*.txt")
	if err != nil {
		return 0, false, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(sso + "\n"); err != nil {
		tmp.Close()
		return 0, false, err
	}
	tmp.Close()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("files", filepath.Base(tmp.Name()))
	if err != nil {
		return 0, false, err
	}
	f, err := os.Open(tmp.Name())
	if err != nil {
		return 0, false, err
	}
	if _, err := io.Copy(part, f); err != nil {
		f.Close()
		return 0, false, err
	}
	f.Close()
	writer.Close()

	importPath := "/api/admin/v1/accounts/import"
	switch p.provider {
	case "grok_web":
		importPath = "/api/admin/v1/accounts/web/import"
	case "grok_console":
		importPath = "/api/admin/v1/accounts/console/import"
	}

	p.mu.Lock()
	tok := p.token
	p.mu.Unlock()

	req, _ := http.NewRequest("POST", p.baseURL+importPath, &buf)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Accept", "text/event-stream")
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, false, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	bodyStr := string(data)

	// 401:token 失效,触发重试
	if resp.StatusCode == 401 {
		return 0, true, fmt.Errorf("admin token 失效 (401)")
	}
	if resp.StatusCode >= 400 {
		return 0, false, fmt.Errorf("http %d: %s", resp.StatusCode, truncateForLog(bodyStr, 200))
	}

	// 解析 SSE complete 事件
	result := parseSSEComplete(bodyStr)
	if result == nil {
		return 0, false, fmt.Errorf("grok2api 响应缺少 complete 事件: %s", truncateForLog(bodyStr, 200))
	}
	created := result.Created
	// 检查 syncFailed:说明 token 已失效或被拒
	if result.SyncFailed > 0 && result.Synced == 0 {
		return created, false, fmt.Errorf("SSO 已导入但同步失败 created=%d syncFailed=%d(token 可能无效)", created, result.SyncFailed)
	}
	// 成功
	if p.assign {
		p.mu.Lock()
		nodes := append([]uint64(nil), p.nodeIDs...)
		p.mu.Unlock()
		if len(nodes) > 0 {
			if err := p.assignToNode(nodes[0]); err != nil {
				fmt.Printf("[grok2api] 代理自动分配失败: %v\n", err)
			}
		}
	}
	return created, false, nil
}

// sseCompleteResult 表示 grok2api 导入 SSE 的 complete 事件内容。
type sseCompleteResult struct {
	Created    int `json:"created"`
	Updated    int `json:"updated"`
	Skipped    int `json:"skipped"`
	Failed     int `json:"failed"`
	Synced     int `json:"synced"`
	SyncFailed int `json:"syncFailed"`
}

// parseSSEComplete 解析 grok2api 导入的 SSE 响应,返回 complete 事件的完整数据。
// 如果响应里出现 error 事件,返回 nil。
func parseSSEComplete(body string) *sseCompleteResult {
	currentEvent := ""
	for _, rawLine := range strings.Split(body, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			currentEvent = strings.TrimSpace(line[6:])
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(line[5:])
		if currentEvent == "error" {
			return nil
		}
		if currentEvent != "complete" {
			continue
		}
		var result sseCompleteResult
		if json.Unmarshal([]byte(payload), &result) == nil {
			return &result
		}
	}
	return nil
}

// truncateForLog 截断字符串用于日志输出。
func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// assignToNode 把刚导入的账号分配到指定节点(mode=auto)。
// 因为不知道刚导入的账号 ID,这里用 provider 查最新账号。
func (p *Grok2APIPusher) assignToNode(nodeID uint64) error {
	// 查最新的账号 ID(按 id desc)
	url := fmt.Sprintf("%s/api/admin/v1/accounts?provider=%s&pageSize=1", p.baseURL, p.provider)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+p.token)
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("http %d: %s", resp.StatusCode, string(data))
	}
	var parsed struct {
		Data struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	if len(parsed.Data.Items) == 0 {
		return fmt.Errorf("未找到 %s 账号", p.provider)
	}
	accountID := parsed.Data.Items[0].ID
	// 分配到节点
	payload, _ := json.Marshal(map[string]any{
		"provider": p.provider,
		"ids":      []string{accountID},
		"mode":     "auto",
	})
	req, _ = http.NewRequest("POST", fmt.Sprintf("%s/api/admin/v1/egress-nodes/%d/accounts", p.baseURL, nodeID), bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err = p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// PushAccountSync 是 appendAccount 调用的入口,封装了错误处理。
// 失败时不中断注册流程,只打印警告(已写入本地文件,可后续补救)。
func PushAccountSync(email, sso string) {
	if !Grok2APIPushEnabled() {
		return
	}
	p := GetGrok2APIPusher()
	created, err := p.PushAccount(sso)
	if err != nil {
		fmt.Printf("[grok2api] 推送 %s 失败: %v(本地已保存,可后续补救)\n", email, err)
		return
	}
	fmt.Printf("[grok2api] 推送 %s 成功 created=%d\n", email, created)
}
