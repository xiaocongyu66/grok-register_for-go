// ts-xai: 真实 x.ai Turnstile 求解测试(生产链路直调)。
//
// 直接调 engine.SolveTurnstileWithUA —— 与注册管线完全相同的代码路径:
//   Servo 内核 serve → 导航 accounts.x.ai/sign-up(真实 origin,真实 sitekey)
//   → 注入 api.js + render → 轮询 token → HumanClick(真实鼠标)×3
//
// 通过标准:拿到真实 token(非 DUMMY)。拿不到时打印 obscura 日志尾部供诊断。
//
// Usage:
//   go run ./cmd/ts-xai [-proxy http://127.0.0.1:PORT] [-timeout 120]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"time"

	"grok-free-register/grok/pkg/engine"
)

func main() {
	proxy := flag.String("proxy", "", "http(s) proxy for the kernel")
	timeout := flag.Int("timeout", 150, "overall timeout seconds")
	flag.Parse()

	// 测试工具:真实建箱(与生产同路径),保证浏览器填的地址可收验证码。
	handle, mhErr := engine.CreateMailbox()
	if mhErr != nil {
		fmt.Printf("[xai] FAIL create mailbox: %v\n", mhErr)
		os.Exit(1)
	}
	fmt.Printf("[xai] mailbox: %s\n", handle.Email)

	ua := engine.RandomUAProfile()
	fmt.Printf("[xai] UA profile: %s\n", ua.UserAgent)
	if *proxy != "" {
		fmt.Printf("[xai] proxy: %s\n", *proxy)
	}

	done := make(chan error, 1)
	var token string
	go func() {
		tok, err := engine.SolveTurnstileWithUA("0x4AAAAAAAhr9JGVDZbrZOo0", *proxy, handle.Email, ua)
		token = tok
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			fmt.Printf("[xai] FAIL: %v\n", err)
			fmt.Println("[xai] ── /tmp/obscura-cf.log tail ──")
			tailLog()
			os.Exit(1)
		}
		fmt.Printf("[xai] TOKEN: %s...\n", token[:min(60, len(token))])
		fmt.Println("[xai] PASS — real x.ai turnstile token obtained")
	case <-time.After(time.Duration(*timeout) * time.Second):
		fmt.Printf("[xai] TIMEOUT after %ds — killing pipeline\n", *timeout)
		tailLog()
		os.Exit(2)
	}
	_ = context.Background()
}

func tailLog() {
	data, err := os.ReadFile("/tmp/obscura-cf.log")
	if err != nil {
		fmt.Println("(no log)")
		return
	}
	lines := 0
	for i := len(data) - 1; i >= 0; i-- {
		if data[i] == '\n' {
			lines++
			if lines > 25 {
				data = data[i+1:]
				break
			}
		}
	}
	os.Stdout.Write(data)
	_ = exec.Command("true").Run()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
