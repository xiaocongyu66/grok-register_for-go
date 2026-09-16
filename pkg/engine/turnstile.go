package engine

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"grok-free-register/grok/pkg/obscura"
)

// SolveTurnstile 用随机 UA 求解 turnstile(会话级 UA 一致性)。
func SolveTurnstile(siteKey, proxy, email string) (string, error) {
	return SolveTurnstileWithUA(siteKey, proxy, email, RandomUAProfile())
}

// SolveTurnstileWithUA 用指定的 UA profile 求解 turnstile(会话级 UA 一致性)。
// 求解链:
//   1. obscura(Servo 内核 headless 浏览器,CDP 端口,真实鼠标/键盘输入)
//   2. HTTP solver API(TURNSTILE_API_URL,默认关闭)
//
// chaser-oxide(CGO)、playwright/CloakBrowser 与 camoufox 回退均已移除:
// chaser 的 Rust FFI 会 SIGSEGV;Servo 内核已完整走通 Turnstile(CF demo
// 拿到 token),作为唯一浏览器求解路径。
func SolveTurnstileWithUA(siteKey, proxy, email string, ua UAProfile) (string, error) {
	// 1. obscura(Servo 内核,唯一浏览器求解路径)
	tok, err := solveTurnstileObscura(siteKey, proxy, email, ua)
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
func solveTurnstileObscura(siteKey, proxy, email string, ua UAProfile) (string, error) {
	// 准备代理(obscura 支持 http:// 和 socks5://)
	browserProxy := maybeRelayProxy(proxy)
	// hy2 上游会话是活动驱动的("no recent network activity" 即断),存活窗口
	// 只有几秒——单次预热打活了上游,内核 boot 的 10-30s 里又死掉。改为
	// solve 全程后台保活:每 3s 打一次 x.ai,持续激活上游会话,直到 solve 返回。
	stopWarm := make(chan struct{})
	go keepRelayAlive(browserProxy, stopWarm)
	defer close(stopWarm)

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
	signupURL := "https://accounts.x.ai/sign-up?redirect=grok-com"
	if err := client.Navigate(signupURL); err != nil {
		return "", fmt.Errorf("obscura navigate %s: %w", signupURL, err)
	}
	// React 水合需要时间;轮询到页面就绪(最多 20s)。x.ai 注册是两步流:
	// 先点「使用邮箱注册」进入表单,然后才有 email input。
	emailSel := ""
	for i := 0; i < 20; i++ {
		time.Sleep(1 * time.Second)
		pageState, _ := client.Evaluate(`(function(){
			var ip = document.querySelector('input[type=email], input[name=email], input[autocomplete=email]');
			var blocked = /Blocked due to|abusive traffic/i.test(document.body && document.body.innerText || '');
			return JSON.stringify({email: !!ip, blocked: blocked,
				text: (document.body && document.body.innerText || '').slice(0, 80)});
		})()`)
		if strings.Contains(pageState, `"blocked":true`) {
			return "", fmt.Errorf("proxy IP blocked by x.ai (abusive traffic patterns) — need a clean IP")
		}
		if strings.Contains(pageState, `"email":true`) {
			emailSel = "input[type=email], input[name=email], input[autocomplete=email]"
			break
		}
	}
	if emailSel == "" {
		// 第二步:页面在方式选择页——用真实鼠标点「使用邮箱注册」。
		pickJS := `(function(){
			var els = document.querySelectorAll('button, a, [role=button], div');
			for (var i=0;i<els.length;i++){
				var t = (els[i].textContent||'').replace(/\s+/g,'');
				var r = els[i].getBoundingClientRect();
				if (r.width > 0 && (t === '使用邮箱注册' || t === 'Sign up with email' || /signupwithemail/i.test(els[i].id||'') || /signupwithemail/i.test(els[i].className||''))) {
					els[i].scrollIntoView({block:'center'});
					var rc = els[i].getBoundingClientRect();
					return JSON.stringify({ok:true, x: rc.x + rc.width/2, y: rc.y + rc.height/2});
				}
			}
			return JSON.stringify({ok:false});
		})()`
		pick, _ := client.Evaluate(pickJS)
		if !strings.Contains(pick, `"ok":true`) {
			txt, _ := client.Evaluate(`(document.body && document.body.innerText || '').slice(0, 150)`)
			return "", fmt.Errorf("email-signup entry not found, page=%q", txt)
		}
		var pv struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
		}
		if err := json.Unmarshal([]byte(pick), &pv); err != nil {
			return "", fmt.Errorf("parse entry rect: %w", err)
		}
		fmt.Printf("[ts] obscura human click '使用邮箱注册' (%.0f, %.0f)\n", pv.X, pv.Y)
		if err := client.HumanClick(pv.X, pv.Y); err != nil {
			return "", fmt.Errorf("human click email entry: %w", err)
		}
		// 诊断:真实点击 3s 无效时,JS click 对照(区分事件合成缺口 vs 选择器错)。
		time.Sleep(3 * time.Second)
		chk, _ := client.Evaluate(`!!document.querySelector('input[type=email]')`)
		if chk != "true" {
			jsr, _ := client.Evaluate(`(function(){
				var els = document.querySelectorAll('button, a, [role=button], div');
				for (var i=0;i<els.length;i++){
					var t = (els[i].textContent||'').replace(/\s+/g,'');
					if ((t === '使用邮箱注册' || t === 'Sign up with email') && els[i].getBoundingClientRect().width > 0) {
						els[i].click();
						return 'js-clicked';
					}
				}
				return 'not-found';
			})()`)
			fmt.Printf("[ts] diag: real click ineffective, fallback %s\n", jsr)
		}
		// 等表单出现(最多 15s)。
		for j := 0; j < 15; j++ {
			time.Sleep(1 * time.Second)
			has, _ := client.Evaluate(`!!document.querySelector('input[type=email], input[name=email], input[autocomplete=email]')`)
			if has == "true" {
				emailSel = "input[type=email], input[name=email], input[autocomplete=email]"
				break
			}
		}
	}
	if emailSel == "" {
		txt, _ := client.Evaluate(`(document.body && document.body.innerText || '').slice(0, 150)`)
		return "", fmt.Errorf("email input still missing after entry click, page=%q", txt)
	}

	// 导航后 JS 覆盖:navigator.userAgentData / hardwareConcurrency / deviceMemory
	client.Evaluate(buildNavOverrideScript(ua))

	// ── 真实用户流:填邮箱(人形键盘) → 点提交(人形鼠标) ──
	// x.ai 的 turnstile 是 execution:'execute' 模式:表单提交触发 widget
	// execute → challenge 跑完 → token 写进页面自己的 input[name=cf-turnstile-response]。
	// 页面自己的 widget 已由 React render(我们再 render 会报参数变更错),不碰它。
	// 邮箱必须与注册 POST 的地址严格一致(enroller 的 handle.Email),
	// 浏览器里填的和 curlcffi 发码的若不同,x.ai 必然识破。
	if strings.TrimSpace(email) == "" {
		return "", fmt.Errorf("real-flow solve requires the enrolled email address")
	}
	fmt.Printf("[ts] obscura typing email %s (human keyboard)\n", email)
	if err := client.HumanTypeInto(email, emailSel); err != nil {
		return "", fmt.Errorf("human type email: %w", err)
	}
	time.Sleep(800 * time.Millisecond)

	// 提交按钮:文本匹配(多语言,随出口 locale 变化)的可见可点元素,
	// 从上往下第一个;选择器含 role=button(x.ai 用过非 button 标签)。
	clickedJS := `(function(){
		var btns = document.querySelectorAll('button[type=submit], form button, button, [role=button]');
		for (var i=0;i<btns.length;i++){
			var t = (btns[i].textContent||'').toLowerCase().replace(/\s+/g,'');
			var r = btns[i].getBoundingClientRect();
			if (r.width > 0 && (
				t.includes('continue') || t.includes('继续') ||
				t.includes('signup') || t.includes('注册') ||
				t.includes('next') || t.includes('下一步'))) {
				btns[i].scrollIntoView({block:'center'});
				var rc = btns[i].getBoundingClientRect();
				return JSON.stringify({ok:true, x: rc.x + rc.width/2, y: rc.y + rc.height/2, text: (btns[i].textContent||'').trim().slice(0,20)});
			}
		}
		return JSON.stringify({ok:false});
	})()`
	clickTarget, _ := client.Evaluate(clickedJS)
	if !strings.Contains(clickTarget, `"ok":true`) {
		// dump 一下当前可点元素,便于下次匹配迭代。
		dump, _ := client.Evaluate(`(function(){
			var out=[];document.querySelectorAll('button,[role=button]').forEach(function(b){var r=b.getBoundingClientRect();if(r.width>0)out.push((b.textContent||'').trim().slice(0,16));});return JSON.stringify(out.slice(0,8));
		})()`)
		return "", fmt.Errorf("submit button not found (clickables=%s)", dump)
	}
	var ct struct {
		X    float64 `json:"x"`
		Y    float64 `json:"y"`
		Text string  `json:"text"`
	}
	if err := json.Unmarshal([]byte(clickTarget), &ct); err != nil {
		return "", fmt.Errorf("parse button rect: %w", err)
	}
	fmt.Printf("[ts] obscura human click submit '%s' (%.0f, %.0f)\n", ct.Text, ct.X, ct.Y)
	if err := client.HumanClick(ct.X, ct.Y); err != nil {
		return "", fmt.Errorf("human click submit: %w", err)
	}

	// 轮询页面自己的 response input(真实流产物),总窗口 50s。
	for i := 0; i < 100; i++ {
		r, _ := client.Evaluate(`(function(){
			var el = document.querySelector('input[name=cf-turnstile-response]');
			return (el && el.value) || '';
		})()`)
		if len(r) > 20 {
			fmt.Printf("[ts] obscura token: %s...\n", r[:20])
			return r, nil
		}
		// execute 后 widget 可能弹可见挑战(managed 模式):点 widget 中心辅助。
		if i == 30 || i == 60 {
			if x, y, err := client.ElementCenter(`[id^=cf-chl-widget]`, 0); err == nil && x > 1 {
				fmt.Printf("[ts] obscura human click widget (%.0f, %.0f)\n", x, y)
				_ = client.HumanClick(x, y)
			}
		}
		time.Sleep(500 * time.Millisecond)
	}

	return "", fmt.Errorf("turnstile: timeout after 50s (obscura real-flow)")
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

// keepRelayAlive 在 solve 期间持续经代理打 x.ai,把 hy2 上游会话钉在活跃态。
// 每次往返失败直接跳过(上游可能瞬断),循环本身不受影响;stop 关闭即退出。
func keepRelayAlive(proxy string, stop <-chan struct{}) {
	if strings.TrimSpace(proxy) == "" {
		return
	}
	pu, err := url.Parse(proxy)
	if err != nil {
		return
	}
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	okCount, failCount := 0, 0
	for {
		select {
		case <-stop:
			fmt.Printf("[ts] relay keepalive done (ok=%d fail=%d)\n", okCount, failCount)
			return
		case <-ticker.C:
			// 禁用连接复用:每个 tick 新建 transport 并带 Connection: close,
			// 强制走真实的 CONNECT+新拨号路径——与内核的连接方式同构。
			// 复用连接会让 keepalive 变成假活(11/11 ok 而内核全死的实证)。
			fresh := &http.Client{
				Timeout: 8 * time.Second,
				Transport: &http.Transport{
					Proxy:               http.ProxyURL(pu),
					DisableKeepAlives:   true,
					MaxIdleConns:        0,
					IdleConnTimeout:     time.Millisecond,
				},
			}
			req, _ := http.NewRequest(http.MethodHead, "https://accounts.x.ai/sign-up", nil)
			req.Close = true
			resp, err := fresh.Do(req)
			if err == nil {
				resp.Body.Close()
				okCount++
			} else {
				failCount++
			}
		}
	}
}
