package minirelay

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	tls "github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/option"
)

// parseTLSOptions 从 share-link URL 的 query 参数解析出 sing-box TLS 配置。
// 支持 security=tls/reality/none, sni, fp(utls 指纹), alpn, pbk/sid(reality)。
func parseTLSOptions(u *url.URL) *option.OutboundTLSOptions {
	q := u.Query()
	security := strings.ToLower(q.Get("security"))
	if security == "" || security == "none" {
		return nil
	}

	sni := q.Get("sni")
	if sni == "" {
		sni = q.Get("servername")
	}
	if sni == "" {
		sni = u.Hostname()
	}

	fingerprint := q.Get("fp")
	if fingerprint == "" {
		fingerprint = q.Get("fingerprint")
	}
	if fingerprint == "" {
		fingerprint = "chrome"
	}

	alpn := q.Get("alpn")
	var alpnList []string
	if alpn != "" {
		alpnList = strings.Split(alpn, ",")
	} else {
		alpnList = []string{"h2", "http/1.1"}
	}

	opts := &option.OutboundTLSOptions{
		Enabled:    true,
		ServerName: sni,
		Insecure:   q.Get("allowInsecure") == "1" || q.Get("insecure") == "1",
		ALPN:       alpnList,
		UTLS: &option.OutboundUTLSOptions{
			Enabled:     true,
			Fingerprint: fingerprint,
		},
	}

	if security == "reality" {
		opts.Reality = &option.OutboundRealityOptions{
			Enabled:   true,
			PublicKey:  q.Get("pbk"),
			ShortID:    q.Get("sid"),
		}
	}

	return opts
}

// doTLSHandshake 用 sing-box tls 包做 TLS 握手（支持 reality + utls 指纹）。
func doTLSHandshake(ctx context.Context, conn net.Conn, serverAddr string, u *url.URL) (net.Conn, error) {
	tlsOpts := parseTLSOptions(u)
	if tlsOpts == nil {
		// 无 TLS
		return conn, nil
	}

	tlsConfig, err := tls.NewClient(ctx, serverAddr, *tlsOpts)
	if err != nil {
		return nil, fmt.Errorf("tls config: %w", err)
	}
	if tlsConfig == nil {
		return conn, nil
	}

	handshakeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	singConn, err := tls.ClientHandshake(handshakeCtx, conn, tlsConfig)
	if err != nil {
		return nil, fmt.Errorf("tls handshake: %w", err)
	}
	return singConn, nil
}

// dialTCPAndTLS 建立 TCP 连接并完成 TLS 握手（如果需要）。
func dialTCPAndTLS(ctx context.Context, host, port string, u *url.URL) (net.Conn, error) {
	serverAddr := net.JoinHostPort(host, port)
	rawConn, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", serverAddr)
	if err != nil {
		return nil, fmt.Errorf("tcp dial: %w", err)
	}
	return doTLSHandshake(ctx, rawConn, serverAddr, u)
}
