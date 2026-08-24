package minirelay

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
)

// handleSocks5 处理 socks5 握手，返回目标地址（host:port）。
// conn 已经读了第一个字节（0x05）。
func (r *Relay) handleSocks5(conn net.Conn) (string, error) {
	// 读 nmethods + methods
	var hdr [1]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return "", err
	}
	methods := make([]byte, int(hdr[0]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return "", err
	}
	// 不要求认证
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return "", err
	}

	// 读请求：VER CMD RSV ATYP DST.ADDR DST.PORT
	var req [4]byte
	if _, err := io.ReadFull(conn, req[:]); err != nil {
		return "", err
	}
	if req[0] != 0x05 {
		return "", fmt.Errorf("socks5: bad version %d", req[0])
	}
	if req[1] != 0x01 { // 只支持 CONNECT
		conn.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0})
		return "", fmt.Errorf("socks5: only CONNECT supported")
	}

	host, err := readSocks5Addr(conn, req[3])
	if err != nil {
		return "", err
	}
	var portBuf [2]byte
	if _, err := io.ReadFull(conn, portBuf[:]); err != nil {
		return "", err
	}
	port := binary.BigEndian.Uint16(portBuf[:])

	// 回复成功
	conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

// readSocks5Addr 读取 socks5 地址（ATYP）。
func readSocks5Addr(conn net.Conn, atyp byte) (string, error) {
	switch atyp {
	case 0x01: // IPv4
		var buf [4]byte
		if _, err := io.ReadFull(conn, buf[:]); err != nil {
			return "", err
		}
		return net.IP(buf[:]).String(), nil
	case 0x03: // 域名
		var lenBuf [1]byte
		if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
			return "", err
		}
		domain := make([]byte, int(lenBuf[0]))
		if _, err := io.ReadFull(conn, domain); err != nil {
			return "", err
		}
		return string(domain), nil
	case 0x04: // IPv6
		var buf [16]byte
		if _, err := io.ReadFull(conn, buf[:]); err != nil {
			return "", err
		}
		return net.IP(buf[:]).String(), nil
	default:
		return "", fmt.Errorf("socks5: unsupported atyp %d", atyp)
	}
}

// handleHTTP 处理 HTTP/HTTPS 代理请求，返回目标地址。
// firstByte 是已读的第一个字节。
func (r *Relay) handleHTTP(conn net.Conn, firstByte byte) (string, error) {
	// 读剩余的 HTTP 请求行
	buf := make([]byte, 4096)
	buf[0] = firstByte
	n, err := conn.Read(buf[1:])
	if err != nil {
		return "", err
	}
	line := string(buf[:n+1])
	// 解析 "CONNECT host:port HTTP/1.1" 或 "GET http://host/..."
	var method, target string
	fmt.Sscanf(line, "%s %s", &method, &target)

	target = strings.TrimSpace(target)
	if target == "" {
		return "", fmt.Errorf("http: empty target")
	}

	if method == "CONNECT" {
		// target 就是 host:port
		conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		return target, nil
	}

	// 普通 HTTP 请求：target 是完整 URL，提取 host
	// 去掉 scheme
	if idx := strings.Index(target, "://"); idx >= 0 {
		rest := target[idx+3:]
		// 取到第一个 / 之前
		if idx2 := strings.Index(rest, "/"); idx2 >= 0 {
			rest = rest[:idx2]
		}
		target = rest
	}
	// 回复 200（浏览器会通过代理发请求）
	conn.Write([]byte("HTTP/1.1 200 OK\r\n\r\n"))
	return target, nil
}
