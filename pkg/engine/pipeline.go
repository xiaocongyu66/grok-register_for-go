package engine

import (
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// Stats tracks registration pipeline counters.
type Stats struct {
	Started  int64
	Success  int64
	Failed   int64
	LastErr  string
	mu       sync.Mutex
}

func (s *Stats) BumpStart() { atomic.AddInt64(&s.Started, 1) }
func (s *Stats) BumpOK() int64 { return atomic.AddInt64(&s.Success, 1) }
func (s *Stats) BumpFail(msg string) {
	atomic.AddInt64(&s.Failed, 1)
	s.mu.Lock()
	s.LastErr = msg
	s.mu.Unlock()
}

// Pipeline runs the full S/P/C registration pipeline.
// S = Solve turnstile, P = Poll email, C = Create user.
type Pipeline struct {
	Proxy   string
	Target  int
	Workers int
	stats   *Stats
}

func NewPipeline(target, workers int, proxy string) *Pipeline {
	if workers <= 0 {
		workers = 4
	}
	if workers > 20 {
		workers = 20
	}
	return &Pipeline{
		Proxy:   proxy,
		Target:  target,
		Workers: workers,
		stats:   &Stats{},
	}
}

// Run starts the pipeline and blocks until target reached or error.
func (p *Pipeline) Run() int {
	fmt.Printf("[pipeline] target=%d workers=%d proxy=%s\n", p.Target, p.Workers, p.Proxy)

	// Fetch signup config
	client, err := NewXaiClient(p.Proxy, 45*time.Second)
	if err != nil {
		fmt.Printf("[pipeline] failed to create client: %v\n", err)
		return 1
	}
	cfg, err := client.FetchConfig()
	client.Close()
	if err != nil {
		fmt.Printf("[pipeline] config fetch failed: %v\n", err)
		return 1
	}
	fmt.Printf("[pipeline] site_key=%s action_id=%s...\n", cfg.SiteKey, truncate(cfg.ActionID, 12))

	var wg sync.WaitGroup
	for i := 0; i < p.Workers; i++ {
		wg.Add(1)
		go p.worker(i, cfg, &wg)
	}

	// Status ticker
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for range t.C {
			fmt.Printf("[*] started=%d success=%d fail=%d last=%s\n",
				atomic.LoadInt64(&p.stats.Started),
				atomic.LoadInt64(&p.stats.Success),
				atomic.LoadInt64(&p.stats.Failed),
				truncate(p.stats.LastErr, 80))
		}
	}()

	wg.Wait()
	fmt.Printf("[done] success=%d fail=%d\n", p.stats.Success, p.stats.Failed)
	return int(p.stats.Success)
}

func (p *Pipeline) worker(id int, cfg *SignupConfig, wg *sync.WaitGroup) {
	defer wg.Done()

	for {
		if p.Target > 0 && atomic.LoadInt64(&p.stats.Success) >= int64(p.Target) {
			return
		}
		p.stats.BumpStart()

		if err := p.registerOnce(id, cfg); err != nil {
			p.stats.BumpFail(err.Error())
			fmt.Printf("[W%d] fail: %v\n", id, err)
			time.Sleep(time.Second)
			continue
		}
		n := p.stats.BumpOK()
		fmt.Printf("[W%d] success (#%d)\n", id, n)
	}
}

func (p *Pipeline) registerOnce(wid int, cfg *SignupConfig) error {
	// 1. Create mailbox
	handle, err := CreateMailbox()
	if err != nil {
		return fmt.Errorf("mailbox: %w", err)
	}

	// 2. Create xAI client + warm
	client, err := NewXaiClient(p.Proxy, 30*time.Second)
	if err != nil {
		return fmt.Errorf("client: %w", err)
	}
	defer client.Close()
	client.Warm()

	// 3. 并行启动：send code + solve turnstile + poll email
	// turnstile 不依赖 send code，可以完全并行，节省 30 秒 send code 等待时间
	var code, token string
	var codeErr, tokenErr, sendErr error
	var w sync.WaitGroup
	w.Add(3)

	// 3a. Send verification code
	go func() {
		defer w.Done()
		sendErr = client.CreateEmailCode(handle.Email)
	}()

	// 3b. Solve turnstile（与 send code 并行，不互相依赖）
	go func() {
		defer w.Done()
		token, tokenErr = SolveTurnstile(cfg.SiteKey, p.Proxy)
	}()

	// 3c. Poll email code（send code 发出后开始轮询）
	go func() {
		defer w.Done()
		// 等 send code 先发出（最多等 30 秒）
		code, codeErr = PollEmailCode(handle.ID, 90*time.Second)
	}()

	w.Wait()

	// send code 失败：直接返回，不浪费 turnstile token（turnstile 结果丢弃）
	if sendErr != nil {
		return fmt.Errorf("send code: %w", sendErr)
	}
	if codeErr != nil {
		return fmt.Errorf("poll code: %w", codeErr)
	}
	if tokenErr != nil {
		return fmt.Errorf("turnstile: %w", tokenErr)
	}

	clean := sanitizeCode(code)

	// 4. Verify code
	if err := client.VerifyEmailCode(handle.Email, clean); err != nil {
		fmt.Printf("[W%d] verify soft-fail: %v\n", wid, err)
	}

	// 5. Signup via Server Action
	body := BuildSignupBody(handle.Email, handle.Password, clean, token)
	sso, err := client.SignupServerAction(body, cfg.ActionID, cfg.StateTree)
	if err != nil {
		return fmt.Errorf("signup: %w", err)
	}

	// 6. Save account
	appendAccount(handle.Email, handle.Password, sso)
	return nil
}

// batchStartTime 是本批注册的开始时间（用于分批文件名），引擎启动时设置。
var batchStartTime = time.Now()

func appendAccount(email, password, sso string) {
	line := email + ":" + sso + "\n"
	// 1. 写入主 sso.txt（兼容，所有批次合并）
	ssoPath := SSOFile()
	if f, err := os.OpenFile(ssoPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
		f.WriteString(line)
		f.Close()
	}
	// 2. 写入分批文件 YYYYMMDDHHMM-sso.txt
	batchPath := filepath.Join(KeysDir(), batchStartTime.Format("200601021504")+"-sso.txt")
	if f, err := os.OpenFile(batchPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
		f.WriteString(line)
		f.Close()
	}
	// 3. 写入分批 grok2api 文件（纯 SSO token，一行一个）
	batchGrokPath := filepath.Join(KeysDir(), batchStartTime.Format("200601021504")+"-grok2api.txt")
	if f, err := os.OpenFile(batchGrokPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
		f.WriteString(sso + "\n")
		f.Close()
	}
	// 4. 推送到 grok2api（如果启用）
	PushAccountSync(email, sso)
}

func sanitizeCode(code string) string {
	out := ""
	for _, c := range code {
		if (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			out += string(c)
		}
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// referenced to keep math/big import
var _ = big.NewInt
