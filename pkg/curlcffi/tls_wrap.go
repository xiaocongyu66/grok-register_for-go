package curlcffi

// tls_wrap.go — TLS 包装辅助（避免与 utls/tls-client 冲突）

import (
	"crypto/tls"
	"net"
)

func tlsDial(conn net.Conn, hostname string) (net.Conn, error) {
	tlsConfig := &tls.Config{
		ServerName: hostname,
	}
	tlsConn := tls.Client(conn, tlsConfig)
	if err := tlsConn.Handshake(); err != nil {
		return nil, err
	}
	return tlsConn, nil
}
