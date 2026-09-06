// regress: full-flow regression harness with per-step JSON timing.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"grok-free-register/grok/pkg/engine"
	"grok-free-register/grok/pkg/obscura"
)

func main() {
	proxy := flag.String("proxy", os.Getenv("PROXY"), "http proxy for the browser exit")
	target := flag.String("target", os.Getenv("TARGET"), "start URL")
	sitekey := flag.String("sitekey", "0x4AAAAAAAhr9JGVDZbrZOo0", "turnstile sitekey")
	out := flag.String("out", "/tmp/regress-report.json", "report output path")
	warmup := flag.Int("warmup", 8, "trajectory warmup moves")
	flag.Parse()
	if *target == "" {
		*target = "https://accounts.x.ai/sign-up?redirect=grok-com"
	}

	ua := engine.RandomUAProfile()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, port, err := obscura.StartObscuraCmdWithOpts(ctx, *proxy, ua.UserAgent, 9222, false)
	if err != nil {
		fmt.Println("start FAIL:", err)
		os.Exit(1)
	}
	defer cmd.Process.Kill()
	client, err := obscura.NewClient(fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		fmt.Println("cdp FAIL:", err)
		os.Exit(1)
	}
	defer client.Close()

	report := client.RunRegression(*target, *sitekey, *warmup)
	report.PrintReport(*out)
}
