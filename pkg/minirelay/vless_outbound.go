package minirelay

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	vless "github.com/sagernet/sing-vmess/vless"
	M "github.com/sagernet/sing/common/metadata"
)

// dialVless 建立 vless 连接（支持 reality TLS），用 sing-box tls 包。
// vless://uuid@host:port?security=reality&sni=...&pbk=...&sid=...&fp=...&flow=...
func (d *outboundDialer) dialVless(ctx context.Context, target string) (net.Conn, error) {
	u := d.u
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "443"
	}
	uuid := u.User.Username()
	if uuid == "" {
		return nil, fmt.Errorf("vless: empty uuid")
	}

	flow := u.Query().Get("flow")
	network := u.Query().Get("type")
	if network == "" {
		network = u.Query().Get("net")
	}

	var tlsConn net.Conn
	var err error
	if network == "ws" {
		// WebSocket transport（vmess/vless + ws）
		tlsConn, err = dialWSTransport(ctx, u, host, port)
	} else {
		// TCP + TLS 握手（用 sing-box tls 包，支持 reality）
		tlsConn, err = dialTCPAndTLS(ctx, host, port, u)
	}
	if err != nil {
		return nil, fmt.Errorf("vless: %w", err)
	}

	// 2. vless 协议握手
	client, err := vless.NewClient(uuid, flow, nopLogger{})
	if err != nil {
		tlsConn.Close()
		return nil, fmt.Errorf("vless client: %w", err)
	}

	destination := M.ParseSocksaddr(target)
	if !destination.IsValid() {
		tlsConn.Close()
		return nil, fmt.Errorf("vless: invalid target %s", target)
	}

	// vless 握手（sing-box 用 DialEarlyConn）
	vlessConn, err := client.DialEarlyConn(tlsConn, destination)
	if err != nil {
		tlsConn.Close()
		return nil, fmt.Errorf("vless handshake: %w", err)
	}
	return vlessConn, nil
}

// dialVmess 建立 vmess 连接。
// vmess://base64(json) 或 vmess://uuid@host:port?...
func (d *outboundDialer) dialVmess(ctx context.Context, target string) (net.Conn, error) {
	u := d.u
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "443"
	}

	// vmess UUID 在 user 部分
	uuid := u.User.Username()
	if uuid == "" {
		return nil, fmt.Errorf("vmess: empty uuid")
	}

	// vmess 参数：security, alterId
	q := u.Query()
	security := q.Get("encryption")
	if security == "" {
		security = q.Get("scy")
	}
	if security == "" {
		security = "auto"
	}
	alterID := 0
	if aid := q.Get("aid"); aid != "" {
		fmt.Sscanf(aid, "%d", &alterID)
	}

	network := q.Get("type")
	if network == "" {
		network = q.Get("net")
	}

	var tlsConn net.Conn
	var err error
	if network == "ws" {
		// WebSocket transport（vmess + ws）
		tlsConn, err = dialWSTransport(ctx, u, host, port)
	} else {
		// TCP + TLS 握手
		tlsConn, err = dialTCPAndTLS(ctx, host, port, u)
	}
	if err != nil {
		return nil, fmt.Errorf("vmess: %w", err)
	}

	// 2. vmess 协议握手
	client, err := vmessNewClient(uuid, security, alterID)
	if err != nil {
		tlsConn.Close()
		return nil, fmt.Errorf("vmess client: %w", err)
	}

	destination := M.ParseSocksaddr(target)
	if !destination.IsValid() {
		tlsConn.Close()
		return nil, fmt.Errorf("vmess: invalid target %s", target)
	}

	vmessConn := client.DialEarlyConn(tlsConn, destination)
	return vmessConn, nil
}

// dialTrojan 建立 trojan 连接。
// trojan://password@host:port?sni=...&security=tls
func (d *outboundDialer) dialTrojan(ctx context.Context, target string) (net.Conn, error) {
	u := d.u
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "443"
	}

	password := u.User.Username()
	if password == "" {
		return nil, fmt.Errorf("trojan: empty password")
	}

	// 1. TCP + TLS 握手（trojan 默认要 TLS）
	q := u.Query()
	if q.Get("security") == "" {
		// trojan 默认 security=tls
		newQ := u.Query()
		newQ.Set("security", "tls")
		u.RawQuery = newQ.Encode()
	}
	tlsConn, err := dialTCPAndTLS(ctx, host, port, u)
	if err != nil {
		return nil, fmt.Errorf("trojan: %w", err)
	}

	// 2. trojan 协议握手（密码 hash + 目标地址）
	destination := M.ParseSocksaddr(target)
	if !destination.IsValid() {
		tlsConn.Close()
		return nil, fmt.Errorf("trojan: invalid target %s", target)
	}

	trojanConn, err := trojanHandshake(tlsConn, password, destination)
	if err != nil {
		tlsConn.Close()
		return nil, fmt.Errorf("trojan handshake: %w", err)
	}
	return trojanConn, nil
}

// _ 防止 import 被清理
var (
	_ = url.Parse
	_ = strings.Contains
	_ = time.Second
)
