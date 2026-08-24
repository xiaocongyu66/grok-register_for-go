package minirelay

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

// outboundDialer 根据上游代理 URL scheme 选择协议实现。
// 支持：socks5:// http://（直接转发），后续加 vless:// vmess:// trojan:// ss://
type outboundDialer struct {
	scheme   string
	upstream string
	u        *url.URL // 解析后的 URL
}

func newOutboundDialer(upstream string) (*outboundDialer, error) {
	u, err := url.Parse(upstream)
	if err != nil {
		return nil, fmt.Errorf("parse upstream: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	return &outboundDialer{
		scheme:   scheme,
		upstream: upstream,
		u:        u,
	}, nil
}

// Dial 建立到 target（host:port）的连接，通过上游代理。
func (d *outboundDialer) Dial(ctx context.Context, target string) (net.Conn, error) {
	switch d.scheme {
	case "socks5", "socks5h", "socks":
		return d.dialSocks5(ctx, target)
	case "http", "https":
		return d.dialHTTP(ctx, target)
	case "direct":
		return (&net.Dialer{}).DialContext(ctx, "tcp", target)
	case "vless":
		return d.dialVless(ctx, target)
	case "vmess":
		return d.dialVmess(ctx, target)
	case "trojan":
		return d.dialTrojan(ctx, target)
	case "ss":
		return d.dialShadowsocks(ctx, target)
	case "hy2", "hysteria2":
		return d.dialHysteria2(ctx, target)
	case "tuic":
		return d.dialTUIC(ctx, target)
	case "shadowtls":
		return d.dialShadowTLS(ctx, target)
	default:
		return nil, fmt.Errorf("unsupported scheme: %s", d.scheme)
	}
}

// dialSocks5 用 golang.org/x/net/proxy 建立 socks5 连接。
func (d *outboundDialer) dialSocks5(ctx context.Context, target string) (net.Conn, error) {
	host := d.u.Hostname()
	port := d.u.Port()
	if port == "" {
		port = "1080"
	}
	var auth *proxy.Auth
	if d.u.User != nil {
		user := d.u.User.Username()
		pass, _ := d.u.User.Password()
		auth = &proxy.Auth{User: user, Password: pass}
	}
	dialer, err := proxy.SOCKS5("tcp", net.JoinHostPort(host, port), auth, &net.Dialer{
		Timeout: 15 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	return dialer.Dial("tcp", target)
}

// dialHTTP 通过 HTTP 代理建立 CONNECT 隧道。
func (d *outboundDialer) dialHTTP(ctx context.Context, target string) (net.Conn, error) {
	host := d.u.Hostname()
	port := d.u.Port()
	if port == "" {
		port = "8080"
	}
	conn, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, err
	}
	// 发 CONNECT
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target)
	if d.u.User != nil {
		// Basic auth
		user := d.u.User.Username()
		pass, _ := d.u.User.Password()
		creds := user + ":" + pass
		req += fmt.Sprintf("Proxy-Authorization: Basic %s\r\n", base64Encode([]byte(creds)))
	}
	req += "\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		return nil, err
	}
	// 读响应
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if !strings.Contains(string(buf[:n]), "200") {
		conn.Close()
		return nil, fmt.Errorf("http proxy: connect failed: %s", string(buf[:n]))
	}
	return conn, nil
}

// dialVless / dialVmess / dialTrojan 实现在 vless_outbound.go

// dialShadowsocks 实现在 ss_outbound.go（用 sing-shadowsocks）

func base64Encode(b []byte) string {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	result := make([]byte, 0, ((len(b)+2)/3)*4)
	for i := 0; i < len(b); i += 3 {
		c := int(b[i]) << 16
		if i+1 < len(b) {
			c |= int(b[i+1]) << 8
		}
		if i+2 < len(b) {
			c |= int(b[i+2])
		}
		result = append(result, chars[(c>>18)&63], chars[(c>>12)&63])
		if i+1 < len(b) {
			result = append(result, chars[(c>>6)&63])
		} else {
			result = append(result, '=')
		}
		if i+2 < len(b) {
			result = append(result, chars[c&63])
		} else {
			result = append(result, '=')
		}
	}
	return string(result)
}
