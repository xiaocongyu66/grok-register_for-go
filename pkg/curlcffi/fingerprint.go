package curlcffi

import (
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"
)


const (
	Firefox120 BrowserType = "firefox_120"
	Firefox119 BrowserType = "firefox_119"
)

// UserAgent 定义一个浏览器的 UA 与相关平台信息。
type UserAgent struct {
	String  string // 完整 UA 字符串
	Browser string // 浏览器名（chrome/firefox/edge/safari）
	Version string // 主版本号
	OS      string // 操作系统标识
}

// USER_AGENTS 对应 Python 的 USER_AGENTS 字典。
var USER_AGENTS = map[BrowserType]UserAgent{
	Chrome120: {
		String:  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		Browser: "chrome",
		Version: "120",
		OS:      "windows",
	},
	Chrome119: {
		String:  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/119.0.0.0 Safari/537.36",
		Browser: "chrome",
		Version: "119",
		OS:      "windows",
	},
	Firefox120: {
		String:  "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:120.0) Gecko/20100101 Firefox/120.0",
		Browser: "firefox",
		Version: "120",
		OS:      "windows",
	},
	Firefox119: {
		String:  "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:119.0) Gecko/20100101 Firefox/119.0",
		Browser: "firefox",
		Version: "119",
		OS:      "windows",
	},
	Edge101: {
		String:  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 Edg/120.0.0.0",
		Browser: "edge",
		Version: "120",
		OS:      "windows",
	},
	Safari170: {
		String:  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15",
		Browser: "safari",
		Version: "17",
		OS:      "macos",
	},
}

// ScreenResolution 表示屏幕分辨率。
type ScreenResolution struct {
	Width  int
	Height int
}

// Fingerprint 是一个完整的浏览器指纹。
type Fingerprint struct {
	UserAgent      string
	Browser        string
	BrowserVersion string
	OS             string
	Screen         ScreenResolution
	Timezone       string
	Language       string
	WebGL          WebGLInfo
	Canvas         CanvasInfo
}

// WebGLInfo 包含 WebGL 相关指纹信息。
type WebGLInfo struct {
	Vendor   string
	Renderer string
}

// CanvasInfo 包含 Canvas 指纹相关信息。
type CanvasInfo struct {
	// Winding 代表 canvas.toDataURL() 后的噪声标识。
	Winding bool
}

// TLSFingerprint 包含 TLS 指纹设置，对应 Python 的 get_tls_fingerprint。
type TLSFingerprint struct {
	Protocols         []string // ALPN 协议列表，如 ["h2", "http/1.1"]
	GreaseEnabled     bool     // 是否启用 GREASE
	CipherSuites      []uint16 // 密码套件
	SignatureAlgs     []uint16 // 签名算法
	Curves            []uint16 // 椭圆曲线
	SupportedVersions []uint16 // 支持的 TLS 版本
}

// FingerprintManager 管理浏览器指纹和 UA，对应 Python 的 FingerprintManager。
type FingerprintManager struct {
	mu  sync.Mutex
	rnd *rand.Rand
}

// NewFingerprintManager 创建一个 FingerprintManager。
func NewFingerprintManager() *FingerprintManager {
	return &FingerprintManager{
		rnd: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// getUserAgentInternal 返回指定浏览器的 UA。当 browserType 为空或未匹配时，随机返回一个。
// 调用者需要持有锁。
func (fm *FingerprintManager) getUserAgentInternal(browserType BrowserType) (BrowserType, UserAgent) {
	if ua, ok := USER_AGENTS[browserType]; ok {
		return browserType, ua
	}
	// 随机选择
	keys := make([]BrowserType, 0, len(USER_AGENTS))
	for k := range USER_AGENTS {
		keys = append(keys, k)
	}
	idx := fm.rnd.Intn(len(keys))
	chosen := keys[idx]
	return chosen, USER_AGENTS[chosen]
}

// GetUserAgent 返回指定浏览器的 UA 字符串，或随机一个，对应 Python 的 get_user_agent(browser_type)。
func (fm *FingerprintManager) GetUserAgent(browserType BrowserType) string {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	_, ua := fm.getUserAgentInternal(browserType)
	return ua.String
}

// GetUserAgentInfo 返回指定浏览器的 UA 详细信息，或随机一个。
func (fm *FingerprintManager) GetUserAgentInfo(browserType BrowserType) (BrowserType, UserAgent) {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	return fm.getUserAgentInternal(browserType)
}

// ListBrowserTypes 返回所有支持的浏览器类型。
func (fm *FingerprintManager) ListBrowserTypes() []BrowserType {
	out := make([]BrowserType, 0, len(USER_AGENTS))
	for k := range USER_AGENTS {
		out = append(out, k)
	}
	return out
}

// generateScreenResolution 根据浏览器类型生成屏幕分辨率。
func (fm *FingerprintManager) generateScreenResolution(browserType BrowserType) ScreenResolution {
	ua, ok := USER_AGENTS[browserType]
	if !ok {
		_, ua = fm.getUserAgentInternal(browserType)
	}
	// Safari 通常搭配 macOS 常见分辨率
	if ua.Browser == "safari" {
		opts := []ScreenResolution{
			{2560, 1440},
			{1920, 1080},
			{1680, 1050},
			{1440, 900},
		}
		return opts[fm.rnd.Intn(len(opts))]
	}
	// Chrome/Edge/Firefox 搭配 Windows 常见分辨率
	opts := []ScreenResolution{
		{1920, 1080},
		{2560, 1440},
		{1366, 768},
		{1536, 864},
		{1440, 900},
	}
	return opts[fm.rnd.Intn(len(opts))]
}

// generateTimezone 根据浏览器/操作系统生成时区。
func (fm *FingerprintManager) generateTimezone(ua UserAgent) string {
	if ua.OS == "macos" {
		opts := []string{"America/Los_Angeles", "America/New_York", "Europe/London"}
		return opts[fm.rnd.Intn(len(opts))]
	}
	opts := []string{
		"America/New_York",
		"America/Chicago",
		"America/Los_Angeles",
		"America/Denver",
		"Europe/London",
		"Europe/Berlin",
		"Asia/Tokyo",
		"Asia/Shanghai",
	}
	return opts[fm.rnd.Intn(len(opts))]
}

// generateLanguage 根据浏览器/操作系统生成语言。
func (fm *FingerprintManager) generateLanguage(ua UserAgent) string {
	opts := []string{"en-US", "en-US,en;q=0.9", "en-GB", "en-US,en;q=0.8"}
	return opts[fm.rnd.Intn(len(opts))]
}

// generateWebGL 根据浏览器/操作系统生成 WebGL 信息。
func (fm *FingerprintManager) generateWebGL(ua UserAgent) WebGLInfo {
	if ua.OS == "macos" {
		return WebGLInfo{
			Vendor:   "Apple Inc.",
			Renderer: "Apple GPU",
		}
	}
	// Windows 上常见 Intel/NVIDIA/AMD
	opts := []WebGLInfo{
		{Vendor: "Google Inc. (Intel)", Renderer: "ANGLE (Intel, Intel(R) UHD Graphics 630 Direct3D11 vs_5_0 ps_5_0, D3D11)"},
		{Vendor: "Google Inc. (NVIDIA)", Renderer: "ANGLE (NVIDIA, NVIDIA GeForce GTX 1060 Direct3D11 vs_5_0 ps_5_0, D3D11)"},
		{Vendor: "Google Inc. (AMD)", Renderer: "ANGLE (AMD, AMD Radeon RX 580 Direct3D11 vs_5_0 ps_5_0, D3D11)"},
	}
	return opts[fm.rnd.Intn(len(opts))]
}

// generateCanvas 生成 Canvas 指纹信息（这里用 winding 布尔值代表噪声位）。
func (fm *FingerprintManager) generateCanvas() CanvasInfo {
	return CanvasInfo{Winding: fm.rnd.Intn(2) == 1}
}

// GenerateFingerprint 生成完整指纹（UA+屏幕分辨率+时区+语言+WebGL+canvas），
// 对应 Python 的 generate_fingerprint(browser_type)。
func (fm *FingerprintManager) GenerateFingerprint(browserType BrowserType) Fingerprint {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	chosen, ua := fm.getUserAgentInternal(browserType)
	screen := fm.generateScreenResolution(chosen)
	timezone := fm.generateTimezone(ua)
	language := fm.generateLanguage(ua)
	webgl := fm.generateWebGL(ua)
	canvas := fm.generateCanvas()

	return Fingerprint{
		UserAgent:      ua.String,
		Browser:        ua.Browser,
		BrowserVersion: ua.Version,
		OS:             ua.OS,
		Screen:         screen,
		Timezone:       timezone,
		Language:       language,
		WebGL:          webgl,
		Canvas:         canvas,
	}
}

// GetTLSFingerprint 返回 TLS 指纹设置（h2/grease/签名算法等），
// 对应 Python 的 get_tls_fingerprint(browser_type)。
func (fm *FingerprintManager) GetTLSFingerprint(browserType BrowserType) TLSFingerprint {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	ua, ok := USER_AGENTS[browserType]
	if !ok {
		_, ua = fm.getUserAgentInternal(browserType)
	}

	switch strings.ToLower(ua.Browser) {
	case "chrome", "edge":
		return TLSFingerprint{
			Protocols:         []string{"h2", "http/1.1"},
			GreaseEnabled:     true,
			CipherSuites:      defaultChromeCipherSuites(),
			SignatureAlgs:     defaultChromeSignatureAlgs(),
			Curves:            []uint16{29, 23, 24, 25, 256, 257},
			SupportedVersions: []uint16{0x0304, 0x0303},
		}
	case "firefox":
		return TLSFingerprint{
			Protocols:         []string{"h2", "http/1.1"},
			GreaseEnabled:     false,
			CipherSuites:      defaultFirefoxCipherSuites(),
			SignatureAlgs:     defaultFirefoxSignatureAlgs(),
			Curves:            []uint16{29, 23, 24, 25},
			SupportedVersions: []uint16{0x0304, 0x0303},
		}
	case "safari":
		return TLSFingerprint{
			Protocols:         []string{"h2", "http/1.1"},
			GreaseEnabled:     false,
			CipherSuites:      defaultSafariCipherSuites(),
			SignatureAlgs:     defaultSafariSignatureAlgs(),
			Curves:            []uint16{29, 23, 24},
			SupportedVersions: []uint16{0x0304, 0x0303},
		}
	default:
		return TLSFingerprint{
			Protocols:         []string{"h2", "http/1.1"},
			GreaseEnabled:     true,
			CipherSuites:      defaultChromeCipherSuites(),
			SignatureAlgs:     defaultChromeSignatureAlgs(),
			Curves:            []uint16{29, 23, 24, 25, 256, 257},
			SupportedVersions: []uint16{0x0304, 0x0303},
		}
	}
}

// --- 默认密码套件与签名算法 ---

func defaultChromeCipherSuites() []uint16 {
	return []uint16{
		// GREASE 占位（实际值动态），这里用一个 GREASE 值表示
		0x0A0A,
		0x1301, // TLS_AES_128_GCM_SHA256
		0x1302, // TLS_AES_256_GCM_SHA384
		0x1303, // TLS_CHACHA20_POLY1305_SHA256
		0xC02B, // TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256
		0xC02C, // TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384
		0xC02F, // TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256
		0xC030, // TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384
		0xCCA9, // TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256
		0xCCA8, // TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256
	}
}

func defaultChromeSignatureAlgs() []uint16 {
	return []uint16{
		0x0403, // ecdsa_secp256r1_sha256
		0x0804, // ecdsa_secp384r1_sha384
		0x0401, // rsa_pkcs1_sha256
		0x0501, // rsa_pkcs1_sha384
		0x0806, // rsa_pss_rsae_sha256
		0x0805, // rsa_pss_rsae_sha384
		0x080B, // rsa_pss_pss_sha256
		0x080A, // rsa_pss_pss_sha384
		0x0201, // rsa_pkcs1_sha1
	}
}

func defaultFirefoxCipherSuites() []uint16 {
	return []uint16{
		0x1301, // TLS_AES_128_GCM_SHA256
		0x1303, // TLS_CHACHA20_POLY1305_SHA256
		0x1302, // TLS_AES_256_GCM_SHA384
		0xC02B, // TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256
		0xC02F, // TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256
		0xCCA9, // TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256
		0xCCA8, // TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256
		0xC02C, // TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384
		0xC030, // TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384
	}
}

func defaultFirefoxSignatureAlgs() []uint16 {
	return []uint16{
		0x0804, // ecdsa_secp384r1_sha384
		0x0805, // rsa_pss_rsae_sha384
		0x0806, // rsa_pss_rsae_sha256
		0x0401, // rsa_pkcs1_sha256
		0x0501, // rsa_pkcs1_sha384
		0x0403, // ecdsa_secp256r1_sha256
		0x0807, // ed25519
		0x0809, // rsa_pss_pss_sha384
		0x080A, // rsa_pss_pss_sha256
		0x080B, // ed448
	}
}

func defaultSafariCipherSuites() []uint16 {
	return []uint16{
		0x1301, // TLS_AES_128_GCM_SHA256
		0x1302, // TLS_AES_256_GCM_SHA384
		0x1303, // TLS_CHACHA20_POLY1305_SHA256
		0xC02B, // TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256
		0xC02C, // TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384
		0xC02F, // TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256
		0xC030, // TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384
		0xCCA9, // TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256
		0xCCA8, // TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256
	}
}

func defaultSafariSignatureAlgs() []uint16 {
	return []uint16{
		0x0804, // ecdsa_secp384r1_sha384
		0x0805, // rsa_pss_rsae_sha384
		0x0806, // rsa_pss_rsae_sha256
		0x0401, // rsa_pkcs1_sha256
		0x0501, // rsa_pkcs1_sha384
		0x0403, // ecdsa_secp256r1_sha256
	}
}

// String 返回 TLSFingerprint 的可读字符串。
func (t TLSFingerprint) String() string {
	return fmt.Sprintf("TLSFingerprint(protocols=%v, grease=%v, ciphers=%d, sigs=%d, curves=%d, versions=%d)",
		t.Protocols, t.GreaseEnabled, len(t.CipherSuites), len(t.SignatureAlgs), len(t.Curves), len(t.SupportedVersions))
}
