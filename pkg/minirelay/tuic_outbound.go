package minirelay

import (
	"context"
	"fmt"
	"net"
	"time"

	tls "github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-quic/tuic"
	"github.com/gofrs/uuid/v5"
	M "github.com/sagernet/sing/common/metadata"
)

// parseUUID 把 UUID 字符串转成 [16]byte。
func parseUUID(s string) ([16]byte, error) {
	id, err := uuid.FromString(s)
	if err != nil {
		return [16]byte{}, err
	}
	var result [16]byte
	copy(result[:], id[:])
	return result, nil
}

// dialTUIC 建立 tuic 连接（QUIC 协议）。
// tuic://uuid:password@host:port?sni=...&congestion_control=bbr
func (d *outboundDialer) dialTUIC(ctx context.Context, target string) (net.Conn, error) {
	u := d.u
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "443"
	}
	uuid := u.User.Username()
	password, _ := u.User.Password()
	if password == "" {
		password = uuid
	}

	serverAddr := M.ParseSocksaddr(net.JoinHostPort(host, port))

	// TLS 配置（tuic 用 h3 ALPN）
	tlsOpts := parseTLSOptions(u)
	if tlsOpts == nil {
		tlsOpts = &option.OutboundTLSOptions{
			Enabled:    true,
			ServerName: host,
			Insecure:   true,
		}
	}
	tlsOpts.ALPN = []string{"h3"}
	tlsConfig, err := tls.NewClient(ctx, net.JoinHostPort(host, port), *tlsOpts)
	if err != nil {
		return nil, fmt.Errorf("tuic tls: %w", err)
	}

	// UUID 转 [16]byte
	var uuidBytes [16]byte
	parsedUUID, err := parseUUID(uuid)
	if err != nil {
		return nil, fmt.Errorf("tuic uuid: %w", err)
	}
	uuidBytes = parsedUUID

	congestion := u.Query().Get("congestion_control")
	if congestion == "" {
		congestion = "bbr"
	}

	client, err := tuic.NewClient(tuic.ClientOptions{
		Context:           ctx,
		Dialer:            newStdDialer(net.JoinHostPort(host, port)),
		ServerAddress:     serverAddr,
		TLSConfig:         tlsConfig,
		UUID:              uuidBytes,
		Password:          password,
		CongestionControl: congestion,
		UDPStream:         false,
		ZeroRTTHandshake:  false,
		Heartbeat:         10 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("tuic client: %w", err)
	}

	destination := M.ParseSocksaddr(target)
	if !destination.IsValid() {
		return nil, fmt.Errorf("tuic: invalid target %s", target)
	}
	return client.DialConn(ctx, destination)
}
