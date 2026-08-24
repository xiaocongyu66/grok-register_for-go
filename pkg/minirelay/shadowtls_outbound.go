package minirelay

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"

	shadowtls "github.com/sagernet/sing-shadowtls"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// dialShadowTLS 建立 shadowtls 连接。
// shadowtls://password@host:port?v3=1&sni=...
func (d *outboundDialer) dialShadowTLS(ctx context.Context, target string) (net.Conn, error) {
	u := d.u
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "443"
	}
	password := u.User.Username()
	if password == "" {
		return nil, fmt.Errorf("shadowtls: empty password")
	}

	// 版本（默认 3）
	version := 3
	if v := u.Query().Get("version"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			version = n
		}
	}
	if v := u.Query().Get("v3"); v == "1" || v == "true" {
		version = 3
	}

	sni := u.Query().Get("sni")
	if sni == "" {
		sni = u.Query().Get("servername")
	}
	if sni == "" {
		sni = host
	}

	serverAddr := M.ParseSocksaddr(net.JoinHostPort(host, port))

	// shadowtls 的 TLS 握手用标准 tls.Config
	tlsConfig := &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: u.Query().Get("insecure") == "1" || u.Query().Get("allowInsecure") == "1",
		NextProtos:         []string{"h2", "http/1.1"},
	}

	client, err := shadowtls.NewClient(shadowtls.ClientConfig{
		Version:      version,
		Password:     password,
		Server:       serverAddr,
		Dialer:       N.SystemDialer,
		TLSHandshake: shadowtls.DefaultTLSHandshakeFunc(password, tlsConfig),
	})
	if err != nil {
		return nil, fmt.Errorf("shadowtls client: %w", err)
	}

	// DialContext 返回 shadowtls 隧道连接（已做 TLS 握手）
	conn, err := client.DialContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("shadowtls dial: %w", err)
	}

	// shadowtls 只提供 TLS 隧道，上层协议（ss/trojan）需要在 conn 上自己握手
	// 但作为中继，我们直接返回 conn，让数据透传
	return conn, nil
}
