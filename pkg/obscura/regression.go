package obscura

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"time"
)

// Regression harness for the full humanized registration flow. Every step
// records its timing and failure mode into a JSON report; a step failing
// never aborts the run — later steps that can still run, do.

type StepResult struct {
	Step      string    `json:"step"`
	OK        bool      `json:"ok"`
	Err       string    `json:"err,omitempty"`
	Started   time.Time `json:"started"`
	ElapsedMS int64     `json:"elapsed_ms"`
	Detail    string    `json:"detail,omitempty"`
}

type RegressionReport struct {
	Started  time.Time    `json:"started"`
	Proxy    string       `json:"proxy"`
	Target   string       `json:"target"`
	Steps    []StepResult `json:"steps"`
	Token    string       `json:"token,omitempty"`
	FinalErr string       `json:"final_err,omitempty"`
	TotalMS  int64        `json:"total_ms"`
}

func (r *RegressionReport) step(name string, fn func() (string, error)) {
	st := time.Now()
	detail, err := fn()
	r.Steps = append(r.Steps, StepResult{
		Step: name, OK: err == nil, Err: errString(err),
		Started: st, ElapsedMS: time.Since(st).Milliseconds(), Detail: detail,
	})
}

func errString(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}

// RunRegression drives the whole flow against the given client. siteKey is
// the Turnstile key; targetURL the start page. Returns the report.
func (c *Client) RunRegression(targetURL, siteKey string, warmupMoves int) *RegressionReport {
	r := &RegressionReport{Started: time.Now(), Proxy: os.Getenv("PROXY"), Target: targetURL}
	t0 := time.Now()
	defer func() { r.TotalMS = time.Since(t0).Milliseconds() }()

	r.step("navigate", func() (string, error) {
		err := c.Navigate(targetURL)
		if err != nil {
			time.Sleep(3 * time.Second)
			err = c.Navigate(targetURL)
		}
		return "", err
	})
	time.Sleep(4 * time.Second)

	r.step("warmup", func() (string, error) {
		rng := rand.New(rand.NewSource(time.Now().UnixNano()))
		for i := 0; i < warmupMoves; i++ {
			if err := c.HumanMoveTo(100+rng.Float64()*1080, 80+rng.Float64()*560); err != nil {
				return "", err
			}
			time.Sleep(time.Duration(150+rng.Intn(350)) * time.Millisecond)
		}
		return fmt.Sprintf("%d moves", warmupMoves), nil
	})

	var btnIdx int
	var bx, by float64
	r.step("find_button", func() (string, error) {
		raw, err := c.Evaluate(`(function(){
  var els = Array.from(document.querySelectorAll('button, a, [role=button]'));
  var i = els.findIndex(function(e){ return /邮箱注册|Sign up with email|Continue with email/i.test(e.innerText||''); });
  return String(i);
})()`)
		if err != nil {
			return "", err
		}
		if _, e := fmt.Sscanf(raw, "%d", &btnIdx); e != nil || btnIdx < 0 {
			return raw, fmt.Errorf("email button not found")
		}
		bx, by, err = c.ElementCenter("button, a, [role=button]", btnIdx)
		return fmt.Sprintf("idx=%d at %.0f,%.0f", btnIdx, bx, by), err
	})

	r.step("click_button", func() (string, error) {
		if btnIdx < 0 {
			return "", fmt.Errorf("skipped: no button")
		}
		return "", c.HumanClick(bx, by)
	})

	r.step("wait_email_input", func() (string, error) {
		deadline := time.Now().Add(30 * time.Second)
		for {
			has, err := c.Evaluate(`!!document.querySelector('input[type=email], input[name=email]')`)
			if err == nil && has == "true" {
				return "", nil
			}
			if time.Now().After(deadline) {
				has2, _ := c.Evaluate(`document.body ? document.body.innerText.slice(0,200) : 'no-body'`)
				return has2, fmt.Errorf("email input did not appear in 30s")
			}
			time.Sleep(2 * time.Second)
		}
	})

	r.step("type_email", func() (string, error) {
		err := c.HumanTypeInto("reguser.bot@outlook.com", "input[type=email], input[name=email]")
		if err != nil {
			return "", err
		}
		time.Sleep(1 * time.Second)
		val, err := c.Evaluate(`(function(){ var e = document.querySelector('input[type=email], input[name=email]'); return e ? e.value : 'NO-INPUT'; })()`)
		if err != nil {
			return "", err
		}
		if val != "reguser.bot@outlook.com" {
			return val, fmt.Errorf("value not set: %s", val)
		}
		return val, nil
	})

	r.step("render_turnstile", func() (string, error) {
		return "", c.RenderTurnstile(siteKey)
	})

	r.step("wait_token", func() (string, error) {
		tok, err := c.WaitForTurnstileToken(90 * time.Second)
		r.Token = tok
		if err != nil {
			st, _ := c.GetTurnstileState()
			r.FinalErr = st
			return st, err
		}
		return fmt.Sprintf("len=%d", len(tok)), nil
	})

	return r
}

// PrintReport writes a compact JSON report to stdout and a file.
func (r *RegressionReport) PrintReport(path string) {
	b, _ := json.MarshalIndent(r, "", "  ")
	fmt.Println(string(b))
	if path != "" {
		_ = os.WriteFile(path, b, 0o644)
	}
}
