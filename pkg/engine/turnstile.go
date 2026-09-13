package engine

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"grok-free-register/grok/pkg/obscura"
)

// SolveTurnstile 用随机 UA 求解 turnstile(会话级 UA 一致性)。
func SolveTurnstile(siteKey, proxy string) (string, error) {
	return SolveTurnstileWithUA(siteKey, proxy, RandomUAProfile())
}

// SolveTurnstileWithUA 用指定的 UA profile 求解 turnstile(会话级 UA 一致性)。
// 求解链:
//   1. obscura(Servo 内核 headless 浏览器,CDP 端口,真实鼠标/键盘输入)
//   2. HTTP solver API(TURNSTILE_API_URL,默认关闭)
//
// chaser-oxide(CGO)、playwright/CloakBrowser 与 camoufox 回退均已移除:
// chaser 的 Rust FFI 会 SIGSEGV;Servo 内核已完整走通 Turnstile(CF demo
// 拿到 token),作为唯一浏览器求解路径。
func SolveTurnstileWithUA(siteKey, proxy string, ua UAProfile) (string, error) {
	// 1. obscura(Servo 内核,唯一浏览器求解路径)
	tok, err := solveTurnstileObscura(siteKey, proxy, ua)
	if err == nil {
		return tok, nil
	}
	fmt.Printf("[ts] obscura solver failed: %v\n", err)

	// 2. HTTP solver API
	if apiURL := strings.TrimSpace(envFirst("TURNSTILE_API_URL")); apiURL != "" {
		tok, err := SolveTurnstileViaAPI(siteKey)
		if err == nil {
			return tok, nil
		}
		fmt.Printf("[ts] API solver failed: %v\n", err)
	}

	return "", fmt.Errorf("all turnstile solvers failed: %w", err)
}

// solveTurnstileObscura 用 obscura(Rust headless 浏览器)求解 turnstile。
// obscura 通过 CDP 端口连接,内置 stealth,协议级代理(不用 --proxy-server)。
// 在 about:blank 页面注入 turnstile widget,不需要导航 xAI。
func solveTurnstileObscura(siteKey, proxy string, ua UAProfile) (string, error) {
	// 准备代理(obscura 支持 http:// 和 socks5://)
	browserProxy := maybeRelayProxy(proxy)

	// 启动 obscura serve(带 stealth + proxy + UA)
	// Rust WebGL 后端默认启用。OBSCURA_NO_WEBGL_RUST=1 用 JS stub。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	useJsStub := os.Getenv("OBSCURA_NO_WEBGL_RUST") == "1"
	cmd, port, err := obscura.StartObscuraCmdWithOpts(ctx, browserProxy, ua.UserAgent, 9222, useJsStub)
	if err != nil {
		return "", fmt.Errorf("start obscura: %w", err)
	}
	defer cmd.Process.Kill()

	// 连接 CDP(用返回的实际端口)
	client, err := obscura.NewClient(fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return "", fmt.Errorf("obscura CDP: %w", err)
	}
	defer client.Close()

	// 注入 UA + sec-ch-ua(确保和 curlcffi 一致)
	if err := client.InjectUAProfile(ua.UserAgent, ua.CHUA, ua.CHUAPlatform); err != nil {
		return "", fmt.Errorf("obscura inject UA: %w", err)
	}

	// Servo 内核的 UA 由 embedder 指纹 profile 决定(启动参数已不再覆盖):
	// 读回真实 UA 反查配套 profile(CHUA/平台与内核严格同源),curlcffi 侧与
	// 浏览器同指纹——请求头 UA 与 JS navigator 必须一致,CF 必查。
	if kernelUA, err := client.GetUserAgent(); err == nil && len(kernelUA) > 20 {
		ua = ProfileFromKernelUA(kernelUA, ua)
	}

	// 导航到 accounts.x.ai 注册页:sitekey 绑定该域名,CF 2026-08 起强制
	// origin 校验——about:blank 上渲染 widget 返回 110200 (Domain not
	// authorized),永远拿不到 token。必须在真实 origin 上渲染。
	// 页面自身会加载 challenges.cloudflare.com 的 api.js,注入前先探 ready。
	signupURL := "https://accounts.x.ai/sign-up?redirect=grok-com"
	if err := client.Navigate(signupURL); err != nil {
		return "", fmt.Errorf("obscura navigate %s: %w", signupURL, err)
	}
	time.Sleep(2 * time.Second)

	// 导航后 JS 覆盖:navigator.userAgentData / hardwareConcurrency / deviceMemory
	// (CDP metadata 不改 JS brands,这里补齐 JS 层一致性)
	client.Evaluate(buildNavOverrideScript(ua))

	// 注入 turnstile widget
	client.Evaluate(fmt.Sprintf(`(() => {
		var s = document.createElement('script');
		s.src = 'https://challenges.cloudflare.com/turnstile/v0/api.js';
		s.async = true;
		document.head.appendChild(s);
	})()`))

	// 等 turnstile API ready
	ready := false
	for i := 0; i < 90; i++ {
		r, _ := client.Evaluate("typeof turnstile !== 'undefined' ? 'yes' : 'no'")
		if r == "yes" {
			ready = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !ready {
		return "", fmt.Errorf("turnstile api.js failed to load after 45s")
	}
	fmt.Println("[ts] obscura turnstile API ready")

	// 渲染 turnstile widget
	client.Evaluate(fmt.Sprintf(`(() => {
		turnstile.render(document.body, {
			sitekey: '%s',
			callback: function(token) { window.__ts_token = token; }
		});
	})()`, siteKey))

	// 轮询 token;managed 模式自动跑,交互模式需要点击。先纯等 5s,然后
	// 用真实鼠标(人形贝塞尔轨迹 → compositor 命中测试,非 JS 合成事件)点
	// widget host 中心——事件穿过 closed shadow 命中 challenge iframe。
	// 最多点 3 次,每次间隔 5s,总窗口 50s。
	clicked := 0
	for i := 0; i < 100; i++ {
		r, _ := client.Evaluate("window.__ts_token || ''")
		if len(r) > 20 {
			fmt.Printf("[ts] obscura token: %s...\n", r[:20])
			return r, nil
		}
		if i >= 10 && clicked < 3 && (i-10)%10 == 0 {
			if x, y, err := client.ElementCenter(`[class*=cf-turnstile], [id^=cf-chl-widget]`, 0); err == nil {
				fmt.Printf("[ts] obscura human click (%.0f, %.0f) #%d\n", x, y, clicked+1)
				if clickErr := client.HumanClick(x, y); clickErr != nil {
					fmt.Printf("[ts] obscura human click error: %v\n", clickErr)
				}
			}
			clicked++
		}
		time.Sleep(500 * time.Millisecond)
	}

	return "", fmt.Errorf("turnstile: timeout after 50s (obscura)")
}

// secureRandInt 返回 [0, max) 的加密安全随机整数(max > 0)。
func secureRandInt(max int) int {
	if max <= 0 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		return 0
	}
	return int(n.Int64())
}

// secureRandRead 用加密安全随机源填充 b。
func secureRandRead(b []byte) {
	_, _ = rand.Read(b)
}

// buildNavOverrideScript 生成导航后 JS 覆盖脚本
// 只覆盖 navigator.userAgentData(CDP metadata 不改 JS brands)和 hardwareConcurrency/deviceMemory
// UA/platform/language/timezone 由 CDP + 启动参数处理(无 JS 注入痕迹)
func buildNavOverrideScript(ua UAProfile) string {
	mobileBool := "false"
	if ua.CHUAMobile == "?1" { mobileBool = "true" }
	platformClean := strings.Trim(ua.CHUAPlatform, `"`)
	hc := 4
	if platformClean == "macOS" || platformClean == "Windows" { hc = 8 }
	return fmt.Sprintf(`try {
		var chuaStr = '%s';
		var brands = [];
		chuaStr.split(', ').forEach(function(part) {
			var m = part.match(/^"([^"]+)";v="([^"]+)"/);
			if (m) brands.push({brand: m[1], version: m[2]});
		});
		var uaData = {brands: brands, mobile: %s, platform: '%s'};
		uaData.getHighEntropyValues = function() { return Promise.resolve({brands: brands, mobile: %s, platform: '%s', platformVersion: '10.0.0', architecture: 'x86', model: '', bitness: '64', uaFullVersion: '124.0.6367.60'}); };
		uaData.toJSON = function() { return {brands: brands, mobile: %s, platform: '%s'}; };
		Object.defineProperty(Object.getPrototypeOf(navigator), 'userAgentData', {get: function() { return uaData; }, configurable: true});
		Object.defineProperty(Object.getPrototypeOf(navigator), 'hardwareConcurrency', {get: function() { return %d; }, configurable: true});
		Object.defineProperty(Object.getPrototypeOf(navigator), 'deviceMemory', {get: function() { return 8; }, configurable: true});
	} catch(e) {}`,
		ua.CHUA, mobileBool, platformClean, mobileBool, platformClean, mobileBool, platformClean, hc)
}

// BuildNavOverrideScript 导出版：导航后 JS 覆盖 userAgentData/hardwareConcurrency。
func BuildNavOverrideScript(ua UAProfile) string { return buildNavOverrideScript(ua) }
