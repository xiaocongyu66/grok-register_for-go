// minirelay: 最小化多协议代理中继。
// 替代 sing-box 二进制，只做"本地 mixed inbound + 远端协议 outbound"。
//
// Usage:
//   minirelay --listen 127.0.0.1:19210 --upstream socks5://user:pass@host:port
//   minirelay --listen 127.0.0.1:19210 --upstream http://host:port
//   minirelay --listen 127.0.0.1:19210 --upstream vless://...（TODO）
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"grok-free-register/grok/pkg/minirelay"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:19210", "local mixed listen address")
	upstream := flag.String("upstream", "", "upstream proxy URL (socks5:// | http:// | vless:// ...)")
	flag.Parse()

	if *upstream == "" {
		fmt.Fprintln(os.Stderr, "usage: minirelay --listen ADDR --upstream URL")
		os.Exit(1)
	}

	r, err := minirelay.New(*listen, *upstream)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create: %v\n", err)
		os.Exit(1)
	}
	if err := r.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "start: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[minirelay] %s → %s\n", r.ListenAddr(), *upstream)

	// 等待信号
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	r.Close()
	fmt.Println("[minirelay] stopped")
}
