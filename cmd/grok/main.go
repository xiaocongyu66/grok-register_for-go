package main

import (
	"fmt"
	"os"

	"grok-free-register/grok/pkg/cli"
	"grok-free-register/grok/pkg/engine"
)

func main() {
	loadEnvFile(".env")

	if len(os.Args) < 2 {
		printHelp()
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "register":
		cmdRegister(args)
	case "scrape":
		cmdScrape(args)
	case "init":
		cmdInit(args)
	case "version":
		fmt.Println("grok 1.0.0")
	default:
		fmt.Printf("unknown command: %s\n", cmd)
		printHelp()
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Println(`grok — Grok registration engine (一站式: 抓取 + 中继 + 求解 + 注册)

Commands:
  init [--install]               检测/安装运行环境(Chrome + Xvfb)
  scrape [--timeout T]           抓取代理(clash.yml + 订阅 + 测活)
  register [--target N] [-w W]   注册 Grok 账号(自动中继 relay 节点)
  version                        Print version`)
}

func cmdRegister(args []string) {
	target := envInt("TARGET", 5)
	workers := envInt("GO_REGISTER_WORKERS", 1)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--target", "-t":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &target)
				i++
			}
		case "--workers", "-w":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &workers)
				i++
			}
		}
	}

	if workers < 1 {
		workers = 1
	}

	e := engine.NewRegisterEngine(target, workers)
	os.Exit(e.Run())
}

func cmdScrape(args []string) {
	timeout := 5
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--timeout", "-t":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &timeout)
				i++
			}
		}
	}
	alive := cli.ScrapeAndTest(64, timeout)
	if len(alive) > 0 {
		fmt.Printf("[✓] %d proxies written to 代理.txt\n", len(alive))
	} else {
		fmt.Println("[✗] no proxies")
	}
}
