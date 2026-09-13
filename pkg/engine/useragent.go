package engine

import (
	"strings"
	"fmt"
	"math/rand"
	"sync"
)

// UAProfile 描述一个浏览器 UA 配置(User-Agent + client hints + TLS 指纹)。
// CF/grok/xAI 会检测 UA 与浏览器实际版本、TLS 指纹是否一致,所以三者必须配套。
//
// 限制:curlcffi 用的 bogdanfinn/tls-client 只支持 Chrome 131/124/120/110 的 TLS profile,
// 所以 UA 版本也限定在这 4 个,不能用 146/150(没有对应 TLS profile,会被检测)。
type UAProfile struct {
	// UA 字符串
	UserAgent string
	// sec-ch-ua header 值
	CHUA string
	// sec-ch-ua-platform 值
	CHUAPlatform string
	// sec-ch-ua-mobile 值
	CHUAMobile string
	// 对应的 curl_cffi TLS 指纹(用于 WithImpersonate)
	// 必须和 UA 版本号一致:UA=Chrome/131 → Impersonate=chrome131
	Impersonate string
}

// uaProfiles 是多个浏览器版本/平台的 UA 池。
// 4 个版本(131/124/120/110) × 3 个平台(Linux/macOS/Windows) = 12 个 profile。
// 每个 profile 的 UA、sec-ch-ua、TLS 指纹配套,避免 CF/xAI 检测到不一致。
var uaProfiles = []UAProfile{
	// === Chrome 131 (最新,TLS Chrome_131) ===
	{
		UserAgent:    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		CHUA:         `"Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"`,
		CHUAPlatform: `"Linux"`,
		CHUAMobile:   "?0",
		Impersonate:  "chrome131",
	},
	{
		UserAgent:    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		CHUA:         `"Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"`,
		CHUAPlatform: `"macOS"`,
		CHUAMobile:   "?0",
		Impersonate:  "chrome131",
	},
	{
		UserAgent:    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		CHUA:         `"Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"`,
		CHUAPlatform: `"Windows"`,
		CHUAMobile:   "?0",
		Impersonate:  "chrome131",
	},
	// === Chrome 124 (TLS Chrome_124) ===
	{
		UserAgent:    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
		CHUA:         `"Chromium";v="124", "Google Chrome";v="124", "Not-A.Brand";v="99"`,
		CHUAPlatform: `"Linux"`,
		CHUAMobile:   "?0",
		Impersonate:  "chrome124",
	},
	{
		UserAgent:    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
		CHUA:         `"Chromium";v="124", "Google Chrome";v="124", "Not-A.Brand";v="99"`,
		CHUAPlatform: `"macOS"`,
		CHUAMobile:   "?0",
		Impersonate:  "chrome124",
	},
	{
		UserAgent:    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
		CHUA:         `"Chromium";v="124", "Google Chrome";v="124", "Not-A.Brand";v="99"`,
		CHUAPlatform: `"Windows"`,
		CHUAMobile:   "?0",
		Impersonate:  "chrome124",
	},
	// === Chrome 120 (TLS Chrome_120) ===
	{
		UserAgent:    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		CHUA:         `"Not_A Brand";v="8", "Chromium";v="120", "Google Chrome";v="120"`,
		CHUAPlatform: `"Linux"`,
		CHUAMobile:   "?0",
		Impersonate:  "chrome120",
	},
	{
		UserAgent:    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		CHUA:         `"Not_A Brand";v="8", "Chromium";v="120", "Google Chrome";v="120"`,
		CHUAPlatform: `"macOS"`,
		CHUAMobile:   "?0",
		Impersonate:  "chrome120",
	},
	{
		UserAgent:    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		CHUA:         `"Not_A Brand";v="8", "Chromium";v="120", "Google Chrome";v="120"`,
		CHUAPlatform: `"Windows"`,
		CHUAMobile:   "?0",
		Impersonate:  "chrome120",
	},
	// === Chrome 110 (TLS Chrome_110) ===
	{
		UserAgent:    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/110.0.0.0 Safari/537.36",
		CHUA:         `"Not_A Brand";v="8", "Chromium";v="110", "Google Chrome";v="110"`,
		CHUAPlatform: `"Linux"`,
		CHUAMobile:   "?0",
		Impersonate:  "chrome110",
	},
	{
		UserAgent:    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/110.0.0.0 Safari/537.36",
		CHUA:         `"Not_A Brand";v="8", "Chromium";v="110", "Google Chrome";v="110"`,
		CHUAPlatform: `"macOS"`,
		CHUAMobile:   "?0",
		Impersonate:  "chrome110",
	},
	{
		UserAgent:    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/110.0.0.0 Safari/537.36",
		CHUA:         `"Not_A Brand";v="8", "Chromium";v="110", "Google Chrome";v="110"`,
		CHUAPlatform: `"Windows"`,
		CHUAMobile:   "?0",
		Impersonate:  "chrome110",
	},
}

var (
	uaRandMu sync.Mutex
	uaRand   = rand.New(rand.NewSource(rand.Int63()))
)

// RandomUAProfile 随机返回一个 UA profile。
// 每个注册会话在开头调用一次,整个会话(curlcffi + chaser 浏览器)共用同一个 profile,
// 保证 UA/sec-ch-ua/TLS 指纹三者配套且会话间不同。
func RandomUAProfile() UAProfile {
	uaRandMu.Lock()
	defer uaRandMu.Unlock()
	return uaProfiles[uaRand.Intn(len(uaProfiles))]
}

// ProfileFromKernelUA 用 Servo 内核指纹 profile 的真实 UA 反查配套 profile,
// 保证 JS 注入(navigator/CHUA)与内核请求头 UA 严格同源——CF 必查的一致性。
// 找不到配套时退回原 profile(调用方仍需以内核 UA 覆盖 UserAgent)。
func ProfileFromKernelUA(kernelUA string, fallback UAProfile) UAProfile {
	for _, p := range uaProfiles {
		if p.UserAgent == kernelUA {
			return p
		}
	}
	// 版本号级匹配:内核 profile 池的版本(如 124)对应 Go 池同版本不同平台。
	kv := chromeVer(kernelUA)
	if kv != "" {
		for _, p := range uaProfiles {
			if chromeVer(p.UserAgent) == kv {
				return p
			}
		}
	}
	out := fallback
	out.UserAgent = kernelUA
	return out
}

// chromeVer 提取 UA 里的 Chrome 主版本号。
func chromeVer(ua string) string {
	i := strings.Index(ua, "Chrome/")
	if i < 0 {
		return ""
	}
	rest := ua[i+7:]
	j := strings.Index(rest, ".")
	if j < 0 {
		return rest
	}
	return rest[:j]
}

// String 返回 UA 字符串(方便调试)。
func (p UAProfile) String() string {
	return fmt.Sprintf("UA(Chrome %s, platform=%s)", p.Impersonate, p.CHUAPlatform)
}
