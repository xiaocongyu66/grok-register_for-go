package minirelay

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"

	vmess "github.com/sagernet/sing-vmess"
	M "github.com/sagernet/sing/common/metadata"
)

// vmessNewClient 创建 vmess 客户端（包装 sing-vmess.NewClient，用 nopLogger）。
func vmessNewClient(uuid, security string, alterID int) (*vmess.Client, error) {
	return vmess.NewClient(uuid, security, alterID)
}

// trojanHandshake 实现 trojan 协议握手。
// trojan 协议：SHA224(password) + CRLF + SOCKS5 地址格式 + CRLF
func trojanHandshake(conn net.Conn, password string, destination M.Socksaddr) (net.Conn, error) {
	// 1. 密码的 SHA224 hex
	h := sha256.Sum224([]byte(password))
	passwordHash := hex.EncodeToString(h[:])

	// 2. SOCKS5 地址格式
	addr, err := trojanFormatAddress(destination)
	if err != nil {
		return nil, err
	}

	// 3. 发送：passwordHash + CRLF + cmd(1) + addr + CRLF
	// cmd = 0x01 (CONNECT)
	header := passwordHash + "\r\n\x01" + addr + "\r\n"
	_, err = conn.Write([]byte(header))
	if err != nil {
		return nil, fmt.Errorf("trojan write header: %w", err)
	}

	return conn, nil
}

// trojanFormatAddress 把目标地址转成 trojan 的 SOCKS5 地址格式。
// 格式：ATYP(1) + ADDR + PORT(2)
//   ATYP=1: IPv4(4) + PORT(2)
//   ATYP=3: LEN(1) + DOMAIN + PORT(2)
//   ATYP=4: IPv6(16) + PORT(2)
func trojanFormatAddress(dest M.Socksaddr) (string, error) {
	port := int(dest.Port)

	if dest.IsFqdn() {
		domain := dest.Fqdn
		return fmt.Sprintf("\x03%s%s%s",
			string(rune(len(domain))),
			domain,
			string([]byte{byte(port >> 8), byte(port & 0xff)}),
		), nil
	}

	addr := dest.Addr
	if addr.Is4() {
		ip4 := addr.As4()
		return fmt.Sprintf("\x01%c%c%c%c%c%c",
			ip4[0], ip4[1], ip4[2], ip4[3],
			byte(port>>8), byte(port&0xff),
		), nil
	}
	if addr.Is6() {
		ip6 := addr.As16()
		result := "\x04"
		for _, b := range ip6 {
			result += string(rune(b))
		}
		result += string([]byte{byte(port >> 8), byte(port & 0xff)})
		return result, nil
	}
	return "", fmt.Errorf("trojan: unsupported address type")
}
