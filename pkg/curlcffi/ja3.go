package curlcffi

// ja3.go — JA3 字符串解析器 + utls ClientHelloSpec 构建
// 替代 curl_cffi 的 ja3 参数：直接用 utls 底层库实现自定义 TLS 指纹
//
// JA3 格式: "771,4865-4866-4867-49195-49199-49196-49200-52393-52392-49171-49172-156-157-47-53,0-23-65281-10-11-35-16-5-13-18-51-45-43-27-17513,29-23-24,0"
// 字段: TLSVersion,Ciphers,Extensions,EllipticCurves,EllipticCurvePointFormats

import (
	"fmt"
	"strconv"
	"strings"

	utls "github.com/bogdanfinn/utls"
)

// ParseJA3 将 JA3 字符串解析为 utls ClientHelloSpec
func ParseJA3(ja3 string) (*utls.ClientHelloSpec, error) {
	parts := strings.Split(strings.TrimSpace(ja3), ",")
	if len(parts) < 3 || len(parts) > 5 {
		return nil, fmt.Errorf("invalid JA3 format: expected 3-5 comma-separated fields, got %d", len(parts))
	}

	// 1. TLS Version
	versionParts, err := parseJA3Part(parts[0])
	if err != nil {
		return nil, fmt.Errorf("JA3 TLS version: %w", err)
	}
	var tlsVersion uint16
	if len(versionParts) > 0 {
		tlsVersion = versionParts[0]
	}

	// 2. Cipher Suites
	cipherIDs, err := parseJA3Part(parts[1])
	if err != nil {
		return nil, fmt.Errorf("JA3 cipher suites: %w", err)
	}

	// 3. Extensions
	extIDs, err := parseJA3Part(parts[2])
	if err != nil {
		return nil, fmt.Errorf("JA3 extensions: %w", err)
	}

	// 4. Elliptic Curves (可选)
	var curveIDs []utls.CurveID
	if len(parts) > 3 && parts[3] != "" {
		curveRaw, err := parseJA3Part(parts[3])
		if err != nil {
			return nil, fmt.Errorf("JA3 curves: %w", err)
		}
		for _, id := range curveRaw {
			curveIDs = append(curveIDs, utls.CurveID(id))
		}
	}

	// 5. EC Point Formats (可选)
	var pointFormats []byte
	if len(parts) > 4 && parts[4] != "" {
		pfRaw, err := parseJA3Part(parts[4])
		if err != nil {
			return nil, fmt.Errorf("JA3 point formats: %w", err)
		}
		for _, p := range pfRaw {
			pointFormats = append(pointFormats, byte(p))
		}
	}

	// 构建 ClientHelloSpec
	spec := &utls.ClientHelloSpec{
		CipherSuites:       cipherIDs,
		CompressionMethods: []byte{0}, // null compression
		Extensions:         buildExtensionsFromJA3(extIDs, curveIDs, pointFormats),
	}

	// 根据 TLS 版本设置
	switch tlsVersion {
	case 0x0301:
		spec.TLSVersMin = utls.VersionTLS10
		spec.TLSVersMax = utls.VersionTLS10
		spec.TLSVersMax = utls.VersionTLS10
	case 0x0302:
		spec.TLSVersMin = utls.VersionTLS11
		spec.TLSVersMax = utls.VersionTLS11
	case 0x0303:
		spec.TLSVersMin = utls.VersionTLS12
		spec.TLSVersMax = utls.VersionTLS12
	case 0x0304:
		spec.TLSVersMin = utls.VersionTLS12
		spec.TLSVersMax = utls.VersionTLS13
	}

	return spec, nil
}

// parseJA3Part 解析 JA3 的一个字段（"4865-4866-4867" → []uint16）
func parseJA3Part(s string) ([]uint16, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, "-")
	result := make([]uint16, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// 支持十六进制 (0x) 和十进制
		base := 10
		if strings.HasPrefix(p, "0x") || strings.HasPrefix(p, "0X") {
			base = 16
			p = p[2:]
		}
		val, err := strconv.ParseUint(p, base, 16)
		if err != nil {
			return nil, fmt.Errorf("invalid value '%s': %w", p, err)
		}
		result = append(result, uint16(val))
	}
	return result, nil
}

// toCipherSuites 已移除——直接用 cipherIDs ([]uint16)

// buildExtensionsFromJA3 根据扩展 ID 列表构建 utls TLSExtension
func buildExtensionsFromJA3(extIDs []uint16, curveIDs []utls.CurveID, pointFormats []byte) []utls.TLSExtension {
	var exts []utls.TLSExtension

	for _, id := range extIDs {
		switch id {
		case 0: // server_name
			exts = append(exts, &utls.SNIExtension{})
		case 5: // status_request
			exts = append(exts, &utls.StatusRequestExtension{})
		case 10: // supported_groups
			if len(curveIDs) > 0 {
				exts = append(exts, &utls.SupportedCurvesExtension{Curves: curveIDs})
			} else {
				exts = append(exts, &utls.SupportedCurvesExtension{Curves: []utls.CurveID{
					utls.X25519, utls.CurveP256, utls.CurveP384, utls.CurveP521,
				}})
			}
		case 11: // ec_point_formats
			if len(pointFormats) > 0 {
				exts = append(exts, &utls.SupportedPointsExtension{SupportedPoints: pointFormats})
			} else {
				exts = append(exts, &utls.SupportedPointsExtension{SupportedPoints: []byte{0, 1, 2}})
			}
		case 13: // signature_algorithms
			exts = append(exts, &utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: []utls.SignatureScheme{
				utls.ECDSAWithP256AndSHA256,
				utls.PSSWithSHA256,
				utls.PKCS1WithSHA256,
				utls.ECDSAWithP384AndSHA384,
				utls.PSSWithSHA384,
				utls.PKCS1WithSHA384,
				utls.PSSWithSHA512,
				utls.PKCS1WithSHA512,
			}})
		case 16: // ALPN
			exts = append(exts, &utls.ALPNExtension{AlpnProtocols: []string{"h2", "http/1.1"}})
		case 18: // signed_certificate_timestamp
			exts = append(exts, &utls.SCTExtension{})
		case 23: // extended_master_secret
			exts = append(exts, &utls.UtlsExtendedMasterSecretExtension{})
		case 27: // compress_certificate
			exts = append(exts, &utls.UtlsCompressCertExtension{[]utls.CertCompressionAlgo{utls.CertCompressionBrotli}})
		case 35: // session_ticket
			exts = append(exts, &utls.SessionTicketExtension{})
		case 43: // supported_versions
			exts = append(exts, &utls.SupportedVersionsExtension{
				Versions: []uint16{
					utls.VersionTLS13, utls.VersionTLS12, utls.VersionTLS11, utls.VersionTLS10,
				}})
		case 45: // psk_key_exchange_modes
			exts = append(exts, &utls.PSKKeyExchangeModesExtension{Modes: []uint8{1}}) // psk_dhe_ke
		case 47: // certificate_compression
			exts = append(exts, &utls.UtlsCompressCertExtension{[]utls.CertCompressionAlgo{utls.CertCompressionBrotli}})
		case 50: // signature_algorithms_cert
			exts = append(exts, &utls.SignatureAlgorithmsCertExtension{SupportedSignatureAlgorithms: []utls.SignatureScheme{
				utls.ECDSAWithP256AndSHA256, utls.PSSWithSHA256, utls.PKCS1WithSHA256,
			}})
		case 51: // key_share
			exts = append(exts, &utls.KeyShareExtension{KeyShares: []utls.KeyShare{
				{Group: utls.X25519}, {Group: utls.CurveP256},
			}})
		case 65281: // renegotiation_info
			exts = append(exts, &utls.RenegotiationInfoExtension{})
		case 65282: // grease (padding)
			exts = append(exts, &utls.UtlsPaddingExtension{GetPaddingLen: utls.BoringPaddingStyle})
		case 17513: // application_settings
			exts = append(exts, &utls.ApplicationSettingsExtension{SupportedProtocols: []string{"h2"}})
		default:
			// 未知扩展，用 GenericExtension 保留
			exts = append(exts, &utls.GenericExtension{Id: id})
		}
	}

	return exts
}

// HelloIDFromBrowserType 将 BrowserType 映射到 utls ClientHelloID
func HelloIDFromBrowserType(bt BrowserType) utls.ClientHelloID {
	switch bt {
	case Chrome99:
		return utls.HelloChrome_100 // 接近
	case Chrome100, Chrome101:
		return utls.HelloChrome_102
	case Chrome104, Chrome107:
		return utls.HelloChrome_106_Shuffle
	case Chrome110:
		return utls.HelloChrome_110
	case Chrome116:
		return utls.HelloChrome_114_Padding_PSK // 接近
	case Chrome119, Chrome120:
		return utls.HelloChrome_120
	case Chrome123, Chrome124:
		return utls.HelloChrome_120
	case Chrome131:
		return utls.HelloChrome_131
	case Chrome133a, Chrome136, Chrome142, Chrome145, Chrome146, Chrome150, Chrome:
		return utls.HelloChrome_133
	case ChromeAndroid:
		return utls.HelloChrome_Auto
	case Edge99, Edge101, Edge:
		return utls.HelloChrome_Auto
	case Safari153, Safari155:
		return utls.HelloIOS_14
	case Safari170, Safari180, Safari184, Safari260, Safari2601, Safari:
		return utls.HelloIOS_Auto
	case Firefox133, Firefox135:
		return utls.HelloFirefox_120
	case Firefox144, Firefox147, Firefox, Tor145:
		return utls.HelloFirefox_Auto
	default:
		return utls.HelloChrome_Auto
	}
}

// ParseAkamai 将 Akamai 指纹字符串解析为 HTTP/2 设置
// Akamai 格式: "1:65536;2:0;3:0;4:6291456;6:262144|15663105|0|m,a,s,p"
// 格式: SETTINGS frames | WINDOW_UPDATE | PRIORITY | HEADERS frame pseudo-header order
func ParseAkamai(akamai string) (*H2Fingerprint, error) {
	if strings.TrimSpace(akamai) == "" {
		return nil, nil
	}

	parts := strings.Split(akamai, "|")
	if len(parts) < 4 {
		return nil, fmt.Errorf("invalid akamai format: expected 4 pipe-separated parts, got %d", len(parts))
	}

	fp := &H2Fingerprint{}

	// 1. SETTINGS
	settings, err := parseAkamaiSettings(parts[0])
	if err != nil {
		return nil, err
	}
	fp.Settings = settings

	// 2. WINDOW_UPDATE
	fp.WindowUpdate, _ = parseAkamaiWindowUpdate(parts[1])

	// 3. PRIORITY
	fp.Priority, _ = parseAkamaiPriority(parts[2])

	// 4. HEADERS order
	fp.HeaderOrder = parseAkamaiHeaderOrder(parts[3])

	return fp, nil
}

// H2Fingerprint HTTP/2 指纹
type H2Fingerprint struct {
	Settings    []H2Setting
	WindowUpdate uint32
	Priority    []H2Priority
	HeaderOrder []string
}

// H2Setting HTTP/2 设置项
type H2Setting struct {
	ID    uint16
	Value uint32
}

// H2Priority HTTP/2 优先级
type H2Priority struct {
	StreamID uint32
	Weight   uint8
	Exclusive bool
	DependsOn uint32
}

// parseAkamaiSettings 解析 "1:65536;2:0;3:0;4:6291456;6:262144"
func parseAkamaiSettings(s string) ([]H2Setting, error) {
	var settings []H2Setting
	for _, pair := range strings.Split(s, ";") {
		parts := strings.SplitN(pair, ":", 2)
		if len(parts) != 2 {
			continue
		}
		id, err := strconv.ParseUint(parts[0], 10, 16)
		if err != nil {
			continue
		}
		val, err := strconv.ParseUint(parts[1], 10, 32)
		if err != nil {
			continue
		}
		settings = append(settings, H2Setting{ID: uint16(id), Value: uint32(val)})
	}
	return settings, nil
}

func parseAkamaiWindowUpdate(s string) (uint32, error) {
	val, err := strconv.ParseUint(strings.TrimSpace(s), 10, 32)
	return uint32(val), err
}

func parseAkamaiPriority(s string) ([]H2Priority, error) {
	var priorities []H2Priority
	for _, item := range strings.Split(s, ",") {
		parts := strings.Split(item, ":")
		if len(parts) < 2 {
			continue
		}
		streamID, _ := strconv.ParseUint(parts[0], 10, 32)
		weight, _ := strconv.ParseUint(parts[1], 10, 8)
		priorities = append(priorities, H2Priority{
			StreamID: uint32(streamID),
			Weight:   uint8(weight),
		})
	}
	return priorities, nil
}

func parseAkamaiHeaderOrder(s string) []string {
	parts := strings.Split(s, ",")
	order := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			order = append(order, p)
		}
	}
	return order
}

// ParsePerk 解析 Perk 指纹（cookie/session 指纹）
func ParsePerk(perk string) map[string]string {
	result := make(map[string]string)
	for _, pair := range strings.Split(perk, ";") {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) == 2 {
			result[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return result
}
