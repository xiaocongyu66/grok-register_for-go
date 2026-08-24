package minirelay

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/sagernet/sing-shadowsocks/shadowimpl"
	M "github.com/sagernet/sing/common/metadata"
)

// dialShadowsocks 建立 shadowsocks 连接。
// ss://base64(method:password)@host:port  或  ss://method:password@host:port
// 或带 #name 的变体。
func (d *outboundDialer) dialShadowsocks(ctx context.Context, target string) (net.Conn, error) {
	u := d.u
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "8388"
	}

	// 解析 method:password
	method, password, err := parseSSCredentials(u.User.Username(), u.User)
	if err != nil {
		return nil, err
	}
	if method == "" || password == "" {
		return nil, fmt.Errorf("ss: empty method or password")
	}

	// 创建 ss method（加密方法实现）
	ssMethod, err := shadowimpl.FetchMethod(method, password, time.Now)
	if err != nil {
		return nil, fmt.Errorf("ss: %w", err)
	}

	// 建立 TCP 连接到 ss 服务器
	rawConn, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, fmt.Errorf("ss tcp: %w", err)
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
