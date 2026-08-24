package curlcffi

// conn_info.go — 连接信息（替代 Python curl_cffi 的 primary_ip/local_ip 等）
// tls-client 不直接暴露连接信息，通过 net.Dial + 反查获取

import (
	"fmt"
	"net"
	"net/url"
	"time"
)

// ConnInfo 连接信息
type ConnInfo struct {
	PrimaryIP   string
	PrimaryPort int
	LocalIP     string
	LocalPort   int
	RemoteAddr  string
	LocalAddr   string
	Duration    time.Duration
}

// GetConnInfo 主动探测连接信息
// 通过 TCP 连接到目标主机，获取 LocalAddr 和 RemoteAddr
func GetConnInfo(targetURL string, proxy string) (*ConnInfo, error) {
	u, err := url.Parse(targetURL)
	if err != nil {
		return nil, fmt.Errorf("parse URL: %w", err)
	}

	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}

	// 如果有代理，连接代理而不是目标
	dialAddr := net.JoinHostPort(host, port)
	if proxy != "" {
		pu, err := url.Parse(proxy)
		if err == nil {
			dialAddr = net.JoinHostPort(pu.Hostname(), pu.Port())
		}
	}

	start := time.Now()
	conn, err := net.DialTimeout("tcp", dialAddr, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	elapsed := time.Since(start)

	localAddr := conn.LocalAddr().(*net.TCPAddr)
	remoteAddr := conn.RemoteAddr().(*net.TCPAddr)

	return &ConnInfo{
		PrimaryIP:   remoteAddr.IP.String(),
		PrimaryPort: remoteAddr.Port,
		LocalIP:     localAddr.IP.String(),
		LocalPort:   localAddr.Port,
		RemoteAddr:  remoteAddr.String(),
		LocalAddr:   localAddr.String(),
		Duration:    elapsed,
	}, nil
}

// WithConnInfo 请求选项：附加连接信息到 Response
func WithConnInfo() func(*Request) {
	return func(r *Request) {
		r.collectConnInfo = true
	}
}

// GetPrimaryIP 获取 Response 的 PrimaryIP（如果已收集）
func (r *Response) GetPrimaryIP() string {
	return r.PrimaryIP
}

// GetPrimaryPort 获取 Response 的 PrimaryPort
func (r *Response) GetPrimaryPort() int {
	return r.PrimaryPort
}

// GetLocalIP 获取本地 IP（需要主动探测）
func (s *Session) GetLocalIP(targetURL string) (string, error) {
	info, err := GetConnInfo(targetURL, s.proxy)
	if err != nil {
		return "", err
	}
	return info.LocalIP, nil
}

// GetLocalPort 获取本地端口（需要主动探测）
func (s *Session) GetLocalPort(targetURL string) (int, error) {
	info, err := GetConnInfo(targetURL, s.proxy)
	if err != nil {
		return 0, err
	}
	return info.LocalPort, nil
}

// GetConnInfo 获取完整连接信息
func (s *Session) GetConnInfo(targetURL string) (*ConnInfo, error) {
	return GetConnInfo(targetURL, s.proxy)
}

// RequestSize 返回请求大小（估算）
func (r *Response) RequestSize() int64 {
	// 请求行 + headers + body
	total := len(r.URL) + 20 // "GET /url HTTP/1.1\r\n"
	if r.Headers != nil {
		for k := range r.Headers {
			total += len(k) + 30 // 估算每个头平均 30 字节
		}
	}
	return int64(total)
}

// ResponseSize 返回响应总大小
func (r *Response) ResponseSize() int64 {
	return r.DownloadSize() + int64(r.HeaderSize())
}
