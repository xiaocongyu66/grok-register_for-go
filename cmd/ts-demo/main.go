// ts-demo: 端到端验证注册机的 Servo 内核求解管线。
//
// 导航 CF 官方 demo(demo.turnstile.workers.dev,测试 sitekey,永远可解),
// 走与 solveTurnstileObscura 相同的启动 + 人形输入链路:
//   StartObscuraCmdWithOpts(Servo 内核 CLI) → NewClient(flatten CDP)
//   → InjectUAProfile + GetUserAgent(内核真实 UA)
//   → implicit render → 轮询 token → HumanClick(真实 compositor 命中)
//
// 通过标准:拿到 XXXX.DUMMY.TOKEN.XXXX(测试 sitekey 的官方固定 token)。
//
// Usage:
//   go run ./cmd/ts-demo [-proxy http://127.0.0.1:PORT]
package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"grok-free-register/grok/pkg/engine"
	"grok-free-register/grok/pkg/obscura"
)

const demoURL = "https://demo.turnstile.workers.dev/"

func main() {
	proxy := flag.String("proxy", "", "http(s) proxy for the kernel (e.g. http://127.0.0.1:21000)")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fmt.Println("[demo] starting obscura (Servo kernel) ...")
	cmd, port, err := obscura.StartObscuraCmdWithOpts(ctx, *proxy, "", 9222, false)
	if err != nil {
		fmt.Printf("[demo] FAIL start obscura: %v\n", err)
		return
	}
	defer cmd.Process.Kill()
	fmt.Printf("[demo] obscura serve up on port %d\n", port)

	client, err := obscura.NewClient(fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		fmt.Printf("[demo] FAIL cdp: %v\n", err)
		return
	}
	defer client.Close()

	ua := engine.RandomUAProfile()
	if err := client.InjectUAProfile(ua.UserAgent, ua.CHUA, ua.CHUAPlatform); err != nil {
		fmt.Printf("[demo] inject UA: %v\n", err)
	}
	if kernelUA, err := client.GetUserAgent(); err == nil && len(kernelUA) > 20 {
		ua.UserAgent = kernelUA
		fmt.Printf("[demo] kernel UA: %s\n", kernelUA)
	}

	fmt.Printf("[demo] navigating %s\n", demoURL)
	if err := client.Navigate(demoURL); err != nil {
		fmt.Printf("[demo] FAIL navigate: %v\n", err)
		return
	}

	ready := false
	for i := 0; i < 90; i++ {
		r, _ := client.Evaluate("typeof turnstile !== 'undefined' ? 'yes' : 'no'")
		if r == "yes" {
			ready = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !ready {
		fmt.Println("[demo] FAIL: turnstile api.js did not load in 45s")
		return
	}
	fmt.Println("[demo] turnstile API ready (implicit render on demo page)")

	clicked := 0
	for i := 0; i < 100; i++ {
		tok, _ := client.Evaluate("(function(){ try { return window.turnstile.getResponse() || ''; } catch(e) { return ''; } })()")
		if len(tok) > 20 {
			fmt.Printf("[demo] TOKEN: %s\n", tok)
			if tok == "XXXX.DUMMY.TOKEN.XXXX" {
				fmt.Println("[demo] PASS — servo kernel pipeline live (real mouse path ready)")
			} else {
				fmt.Println("[demo] PASS (unexpected token shape, but pipeline live)")
			}
			return
		}
		if i >= 10 && clicked < 3 && (i-10)%10 == 0 {
			if x, y, err := client.ElementCenter(`[class*=cf-turnstile], [id^=cf-chl-widget]`, 0); err == nil {
				fmt.Printf("[demo] human click (%.0f, %.0f) #%d\n", x, y, clicked+1)
				if clickErr := client.HumanClick(x, y); clickErr != nil {
					fmt.Printf("[demo] human click error: %v\n", clickErr)
				}
			}
			clicked++
		}
		time.Sleep(500 * time.Millisecond)
	}
	fmt.Println("[demo] FAIL: no token in 50s window")
}
