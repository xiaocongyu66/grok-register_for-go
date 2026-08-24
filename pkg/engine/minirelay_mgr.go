package engine

import (
	"fmt"
	"net"
	"strconv"
	"sync"

	"grok-free-register/grok/pkg/minirelay"
)

// startMinirelays 对每个 relay 节点（ss/vmess/trojan/vless）启动 minirelay 中继，
// 返回本地 HTTP 代理 URL 列表（如 http://127.0.0.1:19301）。
// 中继进程在 grok 进程生命周期内常驻，按节点分配本地端口。
var (
	minirelayMu      sync.Mutex
	minirelayStarted = make(map[string]string) // upstream → localURL
	minirelayPort    = 19300
)

func startMinirelays(nodes []string) []string {
	minirelayMu.Lock()
	defer minirelayMu.Unlock()

	var localProxies []string
	for _, upstream := range nodes {
		if localURL, ok := minirelayStarted[upstream]; ok {
			localProxies = append(localProxies, localURL)
			continue
		}
		minirelayPort++
		listen := fmt.Sprintf("127.0.0.1:%d", minirelayPort)
		relay, err := minirelay.New(listen, upstream)
		if err != nil {
			fmt.Printf("[minirelay] failed to create for %s: %v\n", truncateStr(upstream, 40), err)
			continue
		}
		if err := relay.Start(); err != nil {
			fmt.Printf("[minirelay] failed to start for %s: %v\n", truncateStr(upstream, 40), err)
			continue
		}
		localURL := "http://" + listen
		minirelayStarted[upstream] = localURL
		localProxies = append(localProxies, localURL)
		fmt.Printf("[minirelay] %s → %s\n", listen, truncateStr(upstream, 40))
	}
	return localProxies
}

// truncateStr 截断字符串（避免日志太长）。
func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// _ 防止 import 被清理
var _ = net.Dial
var _ = strconv.Itoa
