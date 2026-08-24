package minirelay

import (
	"context"
	"net"

	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// stdDialer 实现 sing 的 N.Dialer 接口（DialContext + ListenPacket）。
// 用于 hysteria2/tuic 等 QUIC 协议（需要 UDP 拨号到服务器）。
type stdDialer struct {
	serverAddr string // 服务器地址 host:port
}

// newStdDialer 创建一个拨号到 serverAddr 的 N.Dialer。
func newStdDialer(serverAddr string) N.Dialer {
	return &stdDialer{serverAddr: serverAddr}
}

// DialContext 拨号连接到服务器。
// hysteria2/QUIC 底层是 UDP,这里用 UDP 拨号(包装成 net.Conn)。
// sing-quic 的 hysteria2 实现会调用 DialContext 建立底层连接。
func (d *stdDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	host, port, _ := net.SplitHostPort(d.serverAddr)
	if host == "" {
		host = destination.AddrString()
	}
	if port == "" {
		port = "443"
	}
	serverUDPAddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, err
	}
	conn, err := net.DialUDP("udp", nil, serverUDPAddr)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// ListenPacket 拨号 UDP 连接（到服务器）。
// QUIC over UDP，需要建立 UDP socket 连到服务器。
func (d *stdDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	// 建立 UDP 连接到服务器地址
	// 解析服务器地址
	host, port, _ := net.SplitHostPort(d.serverAddr)
	if host == "" {
		host = destination.AddrString()
	}
	if port == "" {
		port = "443"
	}
	serverUDPAddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, err
	}
	// 建立 UDP 连接
	conn, err := net.DialUDP("udp", nil, serverUDPAddr)
	if err != nil {
		return nil, err
	}
	return conn, nil
}
