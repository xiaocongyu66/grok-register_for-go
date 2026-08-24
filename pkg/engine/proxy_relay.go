package engine

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// proxyRelayManager 管理为带认证 socks5 代理启动的本地中继进程。
// Chrome/chromedp 不支持 socks5://user:pass@host:port，需要先用 socks5relay
// 转成无认证的本地 socks5，再传给浏览器。
type proxyRelayManager struct {
	mu       sync.Mutex
	relays   map[string]*relayProc
	binary   string
	startPort int
}

type relayProc struct {
	localURL string
	cmd      *exec.Cmd
	port     int
}

type relayResult struct {
	localURL string
	proc     *relayProc
}

var proxyRelay = &proxyRelayManager{
	relays:    make(map[string]*relayProc),
	startPort: 19200,
}

// maybeRelayProxy 为浏览器准备代理 URL：
// 1. 清理 fragment (#name) 和 query (?x=y) — Chrome 的 --proxy-server 不支持这些
// 2. 带认证的 socks5 → 启动本地无认证中继（Chrome 不支持 socks5 认证）
// 3. 无认证的 socks5/http → 原样返回（清理后）
func maybeRelayProxy(proxy string) string {
	proxy = strings.TrimSpace(proxy)
	if proxy == "" {
		return ""
	}
	// 取 scheme（在 :// 之前）
	scheme := ""
	if idx := strings.Index(proxy, "://"); idx > 0 {
		scheme = strings.ToLower(proxy[:idx])
	}
	if !strings.HasPrefix(scheme, "socks") {
		// http/https 代理：清理 fragment/query，带认证 Chrome 原生支持
		return cleanProxyURL(proxy)
	}
	// socks5：判断是否带认证（user:pass@）
	atIdx := strings.Index(proxy, "@")
	schemeEnd := strings.Index(proxy, "://")
	hasAuth := atIdx > 0 && atIdx > schemeEnd
	if !hasAuth {
		// socks5 无认证：清理 fragment/query，Chrome 原生支持
		return cleanProxyURL(proxy)
	}

	// 带认证的 socks5：启动本地中继
	proxyRelay.mu.Lock()
	defer proxyRelay.mu.Unlock()

	if existing, ok := proxyRelay.relays[proxy]; ok && relayAlive(existing) {
		return existing.localURL
	}

	result := proxyRelay.startRelay(proxy)
	if result == nil {
		// 中继失败，返回清理后的原代理（浏览器会报 ERR_NO_SUPPORTED_PROXIES，但至少不卡死）
		return cleanProxyURL(proxy)
	}
	proxyRelay.relays[proxy] = result.proc
	return result.localURL
}

// cleanProxyURL 去掉代理 URL 的 fragment (#...) 和 query (?...)，
// 因为 Chrome 的 --proxy-server 参数不支持这些部分。
// 保留 scheme://[user:pass@]host:port。
func cleanProxyURL(proxy string) string {
	// 去掉 fragment
	if idx := strings.IndexAny(proxy, "#"); idx >= 0 {
		proxy = proxy[:idx]
	}
	// 去掉 query
	if idx := strings.Index(proxy, "?"); idx >= 0 {
		proxy = proxy[:idx]
	}
	return strings.TrimSpace(proxy)
}

func (m *proxyRelayManager) startRelay(upstream string) *relayResult {
	bin := m.findBinary()
	if bin == "" {
		fmt.Println("[relay] socks5relay binary not found, cannot relay auth proxy")
		return nil
	}

	port := m.allocatePort()
	listen := fmt.Sprintf("127.0.0.1:%d", port)
	localURL := fmt.Sprintf("socks5://%s", listen)

	cmd := exec.Command(bin, "--listen", listen, "--upstream", upstream)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	logDir := filepath.Join(ProjectRoot(), "logs", "proxy-relay")
	os.MkdirAll(logDir, 0755)
	logPath := filepath.Join(logDir, fmt.Sprintf("relay-%d.log", port))
	logFile, _ := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		fmt.Printf("[relay] failed to start socks5relay: %v\n", err)
		logFile.Close()
		return nil
	}

	// 等待本地端口就绪（最多 5 秒）
	if !waitPortReady("127.0.0.1", port, 5*time.Second) {
		fmt.Printf("[relay] socks5relay port not ready on %s\n", listen)
		cmd.Process.Kill()
		logFile.Close()
		return nil
	}

	fmt.Printf("[relay] %s → %s (auth stripped)\n", listen, upstream)

	return &relayResult{
		localURL: localURL,
		proc:     &relayProc{localURL: localURL, cmd: cmd, port: port},
	}
}

func (m *proxyRelayManager) findBinary() string {
	if m.binary != "" {
		return m.binary
	}
	// 优先用项目内的 socks5relay
	candidates := []string{
		filepath.Join(ProjectRoot(), "native", "grok", "socks5relay"),
		filepath.Join(ProjectRoot(), "native", "grok", "socks5relay.exe"),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			m.binary = p
			return p
		}
	}
	if p, err := exec.LookPath("socks5relay"); err == nil {
		m.binary = p
		return p
	}
	return ""
}

func (m *proxyRelayManager) allocatePort() int {
	for port := m.startPort; port < m.startPort+500; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			ln.Close()
			return port
		}
	}
	return m.startPort
}

func relayAlive(r *relayProc) bool {
	if r == nil || r.cmd == nil || r.cmd.Process == nil {
		return false
	}
	return r.cmd.Process.Signal(syscall.Signal(0)) == nil
}

func waitPortReady(host string, port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// StopAllRelays 清理所有中继进程（进程退出时调用）。
func StopAllRelays() {
	proxyRelay.mu.Lock()
	defer proxyRelay.mu.Unlock()
	for _, r := range proxyRelay.relays {
		if r.cmd != nil && r.cmd.Process != nil {
			r.cmd.Process.Kill()
			r.cmd.Wait()
		}
	}
	proxyRelay.relays = make(map[string]*relayProc)
}
