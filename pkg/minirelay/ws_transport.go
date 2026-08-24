package minirelay

import (
	"context"
	"fmt"
	"net"
	"net/url"

	tls "github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/transport/v2raywebsocket"
	"github.com/sagernet/sing/common/json/badoption"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// dialWSTransport 建立 WebSocket transport 连接（用于 vmess/vless + ws）。
// 返回一个已建立 WebSocket 连接的 net.Conn，上层协议（vless/vmess）在此基础上握手。
//
// 参数从 URL query 解析：path, host(header), sni 等。
func dialWSTransport(ctx context.Context, u *url.URL, host, port string) (net.Conn, error) {
	q := u.Query()
	wsPath := q.Get("path")
	if wsPath == "" {
		wsPath = "/"
	}

	// ws headers（host header）
	headers := make(map[string][]string)
	wsHost := q.Get("host")
	if wsHost == "" {
		wsHost = net.JoinHostPort(host, port)
	}
	headers["Host"] = []string{wsHost}

	// TLS 配置（ws + tls）
	var tlsConfig tls.Config
	serverAddr := M.ParseSocksaddr(net.JoinHostPort(host, port))
	if sec := q.Get("security"); sec == "tls" || sec == "reality" {
		tlsOpts := parseTLSOptions(u)
		if tlsOpts != nil {
			tc, err := tls.NewClient(ctx, net.JoinHostPort(host, port), *tlsOpts)
			if err != nil {
				return nil, fmt.Errorf("ws tls: %w", err)
			}
			tlsConfig = tc
		}
	}

	// 创建 ws transport
	transport, err := v2raywebsocket.NewClient(
		ctx,
		newStdDialer(net.JoinHostPort(host, port)),
		serverAddr,
		option.V2RayWebsocketOptions{
			Path:    wsPath,
			Headers: headersToHTTPHeader(headers),
		},
		tlsConfig,
	)
	if err != nil {
		return nil, fmt.Errorf("ws transport: %w", err)
	}
	defer transport.Close()

	// 建立 ws 连接
	conn, err := transport.DialContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("ws dial: %w", err)
	}
	return conn, nil
}

// headersToHTTPHeader 把 map[string][]string 转 badoption.HTTPHeader。
func headersToHTTPHeader(h map[string][]string) badoption.HTTPHeader {
	result := badoption.HTTPHeader{}
	for k, v := range h {
		result[k] = badoption.Listable[string](v)
	}
	return result
}

// _ 防止 import 被清理
var _ = N.NetworkTCP
