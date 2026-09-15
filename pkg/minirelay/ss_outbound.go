package minirelay

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sagernet/sing-shadowsocks/shadowimpl"
	"github.com/shadowsocks/go-shadowsocks2/core"
	"github.com/shadowsocks/go-shadowsocks2/socks"
	"github.com/sagernet/sing-shadowsocks"
	M "github.com/sagernet/sing/common/metadata"
)

// ssMethodCache 缓存 ss method(加密实现),避免每次连接都重新创建
var (
	ssMethodMu    sync.Mutex
	ssMethodCache = make(map[string]shadowsocks.Method)
)

// getSSMethod 获取或创建缓存的 ss method
func getSSMethod(method, password string) (shadowsocks.Method, error) {
	key := method + ":" + password
	ssMethodMu.Lock()
	defer ssMethodMu.Unlock()
	if m, ok := ssMethodCache[key]; ok {
		return m, nil
	}
	m, err := shadowimpl.FetchMethod(method, password, time.Now)
	if err != nil {
		return nil, err
	}
	ssMethodCache[key] = m
	return m, nil
}

// dialShadowsocks 建立 shadowsocks 连接。
// ss://base64(method:password)@host:port  或  ss://method:password@host:port
func (d *outboundDialer) dialShadowsocks(ctx context.Context, target string) (net.Conn, error) {
	u := d.u
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "8388"
	}

	method, password, err := parseSSCredentials(u.User.Username(), u.User)
	if err != nil {
		return nil, err
	}
	if method == "" || password == "" {
		return nil, fmt.Errorf("ss: empty method or password")
	}

	// 用缓存的 ss method(避免每次重新创建)
	ssMethod, err := getSSMethod(method, password)
	if err != nil {
		return nil, fmt.Errorf("ss: %w", err)
	}

	// 建立 TCP 连接到 ss 服务器
	rawConn, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, fmt.Errorf("ss tcp: %w", err)
	}

	// Legacy stream ciphers (aes-256-cfb 等)不走 sing-shadowsocks,
	// 用 go-shadowsocks2(core)——shadowsocks 官方 Go 实现的全 cipher 表。
	if isLegacySSMethod(method) {
		return dialGoSS2(rawConn, method, password, target)
	}

	// ss 握手（写入目标地址）
	destination := M.ParseSocksaddr(target)
	if !destination.IsValid() {
		rawConn.Close()
		return nil, fmt.Errorf("ss: invalid target %s", target)
	}
	ssConn, err := ssMethod.DialConn(rawConn, destination)
	if err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("ss handshake: %w", err)
	}
	return ssConn, nil
}

// parseSSCredentials 解析 ss URL 的 method:password。
// 用户信息可能是 base64 编码的 "method:password"，也可能是明文。
func parseSSCredentials(userInfo string, user *url.Userinfo) (method, password string, err error) {
	if userInfo == "" {
		return "", "", fmt.Errorf("ss: empty user info")
	}
	// 先尝试 base64 解码
	if !strings.Contains(userInfo, ":") {
		decoded, decErr := base64.RawURLEncoding.DecodeString(userInfo)
		if decErr != nil {
			decoded, decErr = base64.StdEncoding.DecodeString(userInfo)
			if decErr != nil {
				return "", "", fmt.Errorf("ss: base64 decode: %w", decErr)
			}
		}
		userInfo = string(decoded)
	}
	// 现在 userInfo 应该是 "method:password"
	parts := strings.SplitN(userInfo, ":", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("ss: invalid credentials format")
	}
	return parts[0], parts[1], nil
}

// isLegacySSMethod: sing-shadowsocks 不支持的 stream cipher 走 go-shadowsocks2。
func isLegacySSMethod(method string) bool {
	switch strings.ToLower(method) {
	case "aes-256-cfb", "aes-192-cfb", "aes-128-cfb", "aes-256-ctr", "aes-128-ctr",
		"rc4-md5", "chacha20", "chacha20-ietf", "bf-cfb", "cast5-cfb", "des-cfb":
		return true
	}
	return false
}

// dialGoSS2 用 go-shadowsocks2 core 建立 legacy stream-cipher SS 连接。
// 地址按 SOCKS5 ATYP 编码,域名由服务端远程解析。
func dialGoSS2(rawConn net.Conn, method, password, target string) (net.Conn, error) {
	cipherBlock, err := core.PickCipher(method, nil, password)
	if err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("ss2: pick cipher: %w", err)
	}
	addr := socks.ParseAddr(target)
	if addr == nil {
		rawConn.Close()
		return nil, fmt.Errorf("ss2: bad target %q", target)
	}
	conn := cipherBlock.StreamConn(rawConn)
	// shadowsocks2 的流式语义:目标地址是加密流的第一段 payload,先写出去。
	if _, err := conn.Write(addr); err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("ss2: write target: %w", err)
	}
	return conn, nil
}
