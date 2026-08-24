package cli

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"grok-free-register/grok/pkg/minirelay"
)

// testRelayNodes 测试 relay 节点（vless/trojan/ss/vmess/hysteria2）：
// 对每个节点启动 minirelay 中继，通过本地 HTTP 代理测 cloudflare trace + x.ai。
// 能通的返回。并发测试，端口从 19400 开始分配。
// hy2 等 QUIC 协议握手较慢,超时设为 20 秒。
func testRelayNodes(nodes []string, workers int) []string {
	if workers <= 0 {
		workers = 8
	}
	if workers > 16 {
		workers = 16 // minirelay 中继占端口，限制并发
	}

	var alive []string
	var mu sync.Mutex
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup

	var portCounter int32 = 19400
	var okCount int32
	var failCount int32

	for _, node := range nodes {
		wg.Add(1)
		go func(upstream string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// 识别协议(用于日志)
			proto := "relay"
			u, err := url.Parse(upstream)
			if err == nil {
				proto = u.Scheme
			}

			port := atomic.AddInt32(&portCounter, 1)
			listen := fmt.Sprintf("127.0.0.1:%d", port)

			// 启动 minirelay 中继
			relay, err := minirelay.New(listen, upstream)
			if err != nil {
				atomic.AddInt32(&failCount, 1)
				fmt.Printf("[scrape] relay FAIL (%s) minirelay create: %s\n", proto, truncateForLog(upstream, 50))
				return
			}
			if err := relay.Start(); err != nil {
				atomic.AddInt32(&failCount, 1)
				fmt.Printf("[scrape] relay FAIL (%s) minirelay start: %s\n", proto, truncateForLog(upstream, 50))
				return
			}
			defer relay.Close()

			// 测试 cloudflare trace + x.ai(hy2 等握手慢,给 20 秒)
			if testViaLocalProxy(listen, 20*time.Second) {
				mu.Lock()
				alive = append(alive, upstream)
				n := atomic.AddInt32(&okCount, 1)
				fmt.Printf("[scrape] relay OK #%d (%s): %s\n", n, proto, truncateForLog(upstream, 50))
				mu.Unlock()
			} else {
				atomic.AddInt32(&failCount, 1)
				fmt.Printf("[scrape] relay FAIL (%s) test: %s\n", proto, truncateForLog(upstream, 50))
			}
		}(node)
	}
	wg.Wait()

	fmt.Printf("[scrape] relay 测试完成: %d ok, %d fail\n", okCount, failCount)
	return alive
}

// testViaLocalProxy 通过本地 HTTP 代理测 cloudflare trace + x.ai。
// 两阶段:cloudflare trace(快速过滤)→ accounts.x.ai(严格判断)。
func testViaLocalProxy(proxyAddr string, timeout time.Duration) bool {
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: proxyAddr}),
		},
	}
	// 阶段 1: cloudflare trace
	resp, err := client.Get("https://www.cloudflare.com/cdn-cgi/trace")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil || len(body) <= 10 {
		return false
	}
	// 阶段 2: accounts.x.ai 严格判断
	resp2, err := client.Get("https://accounts.x.ai/")
	if err != nil {
		return false
	}
	defer resp2.Body.Close()
	return resp2.StatusCode == 200 || resp2.StatusCode == 403 || resp2.StatusCode == 302
}

// truncateForLog 截断字符串用于日志。
func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// _ 防止 import 被清理
var _ = net.Dial
