package minirelay

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	tls "github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-quic/hysteria2"
	M "github.com/sagernet/sing/common/metadata"
)

// dialHysteria2 建立 hysteria2 连接（QUIC 协议）。
// hy2://password@host:port?sni=...&insecure=1&obfs=salamander&obfs-password=xxx&mport=20000-30000
// 或 hysteria2://...
func (d *outboundDialer) dialHysteria2(ctx context.Context, target string) (net.Conn, error) {
	u := d.u
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "443"
	}
	password := u.User.Username()

	serverAddr := M.ParseSocksaddr(net.JoinHostPort(host, port))

	// TLS 配置（hysteria2 用标准 TLS + ALPN h3）
	// 注意:sing-quic 的 hysteria2 client 需要标准 *tls.Config,
	// 不兼容 sing-box 的 utls(utls 不返回标准 Config,会报 "unsupported usage for uTLS"),
	// 所以这里不用 UTLS,用标准 TLS。
	q := u.Query()
	sni := q.Get("sni")
	if sni == "" {
		sni = q.Get("servername")
	}
	if sni == "" {
		sni = host
	}
	// hy2 节点通常用自签/共享证书,insecure 默认开(除非 URL 明确 insecure=0 且证书可信)
	// URL 里 insecure=0 时仍强制跳过验证(hy2 节点通常用自签/共享证书)
	insecure := true
	tlsOpts := &option.OutboundTLSOptions{
		Enabled:    true,
		ServerName: sni,
		Insecure:   insecure,
		ALPN:       []string{"h3"},
	}
	tlsConfig, err := tls.NewClient(ctx, net.JoinHostPort(host, port), *tlsOpts)
	if err != nil {
		return nil, fmt.Errorf("hy2 tls: %w", err)
	}

	// 解析带宽参数
	sendBPS := uint64(0)
	recvBPS := uint64(0)
	if up := q.Get("up"); up != "" {
		if bps, err := parseBandwidth(up); err == nil {
			sendBPS = bps
		}
	}
	if down := q.Get("down"); down != "" {
		if bps, err := parseBandwidth(down); err == nil {
			recvBPS = bps
		}
	}

	// 解析 obfs 混淆(目前只支持 salamander)
	// 注意:obfs-password 可能含特殊字符(如 /, =),URL 里被编码为 %2F %3D,这里解码
	var salamanderPassword string
	if obfs := q.Get("obfs"); obfs == "salamander" {
		rawPwd := q.Get("obfs-password")
		if decoded, err := url.QueryUnescape(rawPwd); err == nil {
			salamanderPassword = decoded
		} else {
			salamanderPassword = rawPwd
		}
	}

	// 解析端口跳跃(mport=20000-30000)
	var serverPorts []string
	if mport := q.Get("mport"); mport != "" {
		serverPorts = parseMportRange(mport)
	}

	client, err := hysteria2.NewClient(hysteria2.ClientOptions{
		Context:            ctx,
		Dialer:             newStdDialer(net.JoinHostPort(host, port)),
		Logger:             nopLogger{},
		ServerAddress:       serverAddr,
		ServerPorts:        serverPorts,
		SendBPS:            sendBPS,
		ReceiveBPS:         recvBPS,
		SalamanderPassword: salamanderPassword,
		Password:           password,
		TLSConfig:          tlsConfig,
		UDPDisabled:        true,
	})
	if err != nil {
		return nil, fmt.Errorf("hy2 client: %w", err)
	}

	destination := M.ParseSocksaddr(target)
	if !destination.IsValid() {
		return nil, fmt.Errorf("hy2: invalid target %s", target)
	}
	return client.DialConn(ctx, destination)
}

// parseMportRange 解析 mport 参数,转成 sing-quic 期望的格式。
// sing-quic 的 ParsePorts 要求 "start:end" 格式(用冒号分隔)。
// 输入支持: "20000-30000" / "20000:30000" / "20000,30000" / "20000"
// 输出: ["20000:30000"] 或 ["20000:20000"] 等
func parseMportRange(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var ports []string
	// 支持逗号分隔的多个范围
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// 统一把 - 替换成 :
		part = strings.ReplaceAll(part, "-", ":")
		if strings.Contains(part, ":") {
			// 范围格式 "start:end"
			subParts := strings.SplitN(part, ":", 2)
			start := strings.TrimSpace(subParts[0])
			end := ""
			if len(subParts) == 2 {
				end = strings.TrimSpace(subParts[1])
			}
			if start == "" {
				start = "1"
			}
			if end == "" {
				end = "65535"
			}
			ports = append(ports, start+":"+end)
		} else {
			// 单端口 "20000" -> "20000:20000"
			ports = append(ports, part+":"+part)
		}
	}
	return ports
}

// parseBandwidth 解析带宽参数（如 "100 mbps" → 字节/秒）。
func parseBandwidth(s string) (uint64, error) {
	// 支持 "100 mbps", "1 gbps", "100000000" 等
	var num float64
	var unit string
	fmt.Sscanf(s, "%f %s", &num, &unit)
	if num == 0 {
		return strconv.ParseUint(s, 10, 64)
	}
	switch unit {
	case "gbps", "Gbps", "g":
		return uint64(num * 1e9 / 8), nil
	case "mbps", "Mbps", "m":
		return uint64(num * 1e6 / 8), nil
	case "kbps", "Kbps", "k":
		return uint64(num * 1e3 / 8), nil
	default:
		return uint64(num), nil
	}
}

// _ 防止 import 被清理
var (
	_ = url.Parse
	_ = time.Second
)
