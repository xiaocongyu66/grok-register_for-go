package engine

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	playwright "github.com/mxschmitt/playwright-go"
)

// SolveTurnstile dispatches to an HTTP solver API when TURNSTILE_API_URL is set,
// otherwise falls back to the embedded playwright browser solver.
// Set TURNSTILE_BROWSER_FALLBACK=1 to allow browser fallback after API failure.
func SolveTurnstile(siteKey, proxy string) (string, error) {
	if apiURL := strings.TrimSpace(envFirst("TURNSTILE_API_URL")); apiURL != "" {
		tok, err := SolveTurnstileViaAPI(siteKey)
		if err == nil {
			return tok, nil
		}
		fmt.Printf("[ts] API solver failed: %v\n", err)
		if !envBool("TURNSTILE_BROWSER_FALLBACK", false) {
			return "", fmt.Errorf("turnstile api: %w (browser fallback disabled)", err)
		}
		fmt.Println("[ts] falling back to browser solver")
	}
	return solveTurnstileBrowser(siteKey, proxy)
}

// solveTurnstileBrowser uses playwright-go + CloakBrowser (via xvfb) to get a Turnstile token.
// Strategy:
// - Browser loads real x.ai page via proxy (socks5)
// - challenges.cloudflare.com requests are intercepted and fetched via curlcffi (direct, Chrome131 TLS)
// - Turnstile widget is injected and rendered on the real page
func solveTurnstileBrowser(siteKey, proxy string) (string, error) {
	chromePath := findChromePath()
	if chromePath == "" {
		return "", fmt.Errorf("chrome/chromium not found")
	}

	// Chrome 不支持带认证的 socks5 代理，需要先转成无认证的本地中继
	browserProxy := maybeRelayProxy(proxy)

	ensureXvfb()

	// 复用 browser 实例（按代理缓存），避免每次求解都启动/关闭 Chrome。
	// 只创建/关闭 context 和 page，browser 常驻。
	browser, err := sharedBrowserPool.getBrowser(browserProxy, chromePath)
	if err != nil {
		return "", err
	}

	context, err := browser.NewContext()
	if err != nil {
		return "", fmt.Errorf("context: %w", err)
	}
	defer context.Close()
	context.AddInitScript(playwright.Script{
		Content: playwright.String("Object.defineProperty(navigator,'webdriver',{get:()=>undefined});"),
	})

	page, err := context.NewPage()
	if err != nil {
		return "", fmt.Errorf("page: %w", err)
	}
	defer page.Close()
	page.SetViewportSize(800, 600)

	// 直接导航到 x.ai signup 页，让浏览器通过代理直接加载 turnstile。
	// 路由拦截 + curlcffi 方案在代理不稳定时反而更慢（session 复用导致状态混乱），
	// 直接让浏览器加载更可靠（验证可用：[ts-poll 0] token acquired）。
	_, err = page.Goto(SignupURLGrok, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(45000),
	})
	if err != nil {
		return "", fmt.Errorf("navigate: %w", err)
	}

	// Wait for page load
	time.Sleep(3 * time.Second)

	// Inject turnstile JS (if not already present)
	page.Evaluate(`() => {
		if (typeof turnstile === 'undefined') {
			var s = document.createElement('script');
			s.src = 'https://challenges.cloudflare.com/turnstile/v0/api.js';
			s.async = true;
			document.head.appendChild(s);
		}
	}`)

	// Wait for turnstile global to be ready (up to 30s)
	ready := false
	for i := 0; i < 60; i++ {
		v, _ := page.Evaluate("() => typeof turnstile !== 'undefined' ? 'yes' : 'no'")
		if s, ok := v.(string); ok && s == "yes" {
			ready = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !ready {
		return "", fmt.Errorf("turnstile: api.js failed to load after 15s")
	}
	fmt.Println("[ts] turnstile API ready")

	// Render turnstile widget (with retry)
	renderResult, err := page.Evaluate(fmt.Sprintf(`() => {
		if (typeof turnstile === 'undefined') return 'no-turnstile';
		var existing = document.getElementById('cf-ts');
		if (existing) return 'already';
		var d = document.createElement('div');
		d.id = 'cf-ts';
		d.style.cssText = 'position:fixed;top:10px;left:10px;z-index:99999;width:300px;height:70px';
		document.body.appendChild(d);
		window.__ts_token = '';
		window.__ts_err = '';
		try {
			turnstile.render(d, {
				sitekey: '%s',
				callback: function(t) { window.__ts_token = t; },
				'error-callback': function(e) { window.__ts_err = String(e); }
			});
			return 'rendered';
		} catch(e) { window.__ts_err = e.message; return 'error:' + e.message; }
	}`, siteKey))
	if err != nil {
		fmt.Printf("[ts] render eval err: %v\n", err)
	} else {
		fmt.Printf("[ts] render result: %v\n", renderResult)
	}

	// Reset widget to clear any stale state
	page.Evaluate(`() => { try { if (window.turnstile && typeof turnstile.reset === 'function') turnstile.reset(); } catch(e) {} }`)
	time.Sleep(1 * time.Second)

	// Poll for token with active checkbox clicking (up to ~50 seconds)
	// Strategy: multi-source token check + shadow DOM checkbox click + screen coord spoof
	for i := 0; i < 50; i++ {
		// 1. Check token from multiple sources (input value, getResponse API, callback var)
		tokenVal, _ := page.Evaluate(`() => {
			try {
				var byInput = String((document.querySelector('input[name="cf-turnstile-response"]') || {}).value || '').trim();
				if (byInput) return byInput;
				if (window.turnstile && typeof turnstile.getResponse === 'function') {
					var r = String(turnstile.getResponse() || '').trim();
					if (r) return r;
				}
				return window.__ts_token || '';
			} catch(e) { return ''; }
		}`)
		if t, ok := tokenVal.(string); ok && len(t) >= 80 {
			fmt.Printf("[ts] token acquired (len=%d)\n", len(t))
			return t, nil
		}

		// 2. Check for fatal errors
		errVal, _ := page.Evaluate("() => window.__ts_err || ''")
		if e, ok := errVal.(string); ok && e != "" {
			if !strings.Contains(e, "110200") && !strings.Contains(e, "300010") &&
				!strings.Contains(e, "300030") && !strings.Contains(e, "300031") &&
				!strings.Contains(e, "600010") {
				return "", fmt.Errorf("turnstile error: %s", e)
			}
		}

		// 3. Spoof screen coordinates + click checkbox in challenge iframe
		clickTurnstileCheckbox(page)

		// 4. Debug output every 10 iterations
		if i%10 == 0 {
			widget, _ := page.Evaluate("() => { var w=document.getElementById('cf-ts'); return w ? w.innerHTML.substring(0,80) : 'none'; }")
			ts, _ := page.Evaluate("() => typeof turnstile !== 'undefined' ? 'yes' : 'no'")
			fmt.Printf("[ts-poll %d] ts=%v widget=%v\n", i, ts, widget)
		}
		time.Sleep(1 * time.Second)
	}

	return "", fmt.Errorf("turnstile: timeout")
}

// clickTurnstileCheckbox spoofs screen coordinates and clicks the checkbox
// inside the Cloudflare challenge iframe (piercing shadow DOM if present).
func clickTurnstileCheckbox(page playwright.Page) {
	// Spoof screenX/screenY on the main page to avoid headless detection
	page.Evaluate(`() => {
		try {
			window.dtp = 1;
			function getRandomInt(min, max) { return Math.floor(Math.random() * (max - min + 1)) + min; }
			var sx = getRandomInt(800, 1200);
			var sy = getRandomInt(400, 700);
			Object.defineProperty(MouseEvent.prototype, 'screenX', { value: sx, configurable: true });
			Object.defineProperty(MouseEvent.prototype, 'screenY', { value: sy, configurable: true });
		} catch(e) {}
	}`)

	// Find the challenge iframe frame and click the checkbox inside it
	clickScript := `() => {
		try {
			// Method 1: direct checkbox in document
			var cb = document.querySelector('input[type=checkbox]');
			if (cb) { cb.click(); return 'checkbox'; }
			// Method 2: body shadow root (Turnstile nests checkbox in body's shadow DOM)
			var body = document.querySelector('body');
			if (body && body.shadowRoot) {
				var input = body.shadowRoot.querySelector('input');
				if (input) { input.click(); return 'body-shadow-input'; }
			}
			// Method 3: any clickable input/button
			var inputs = document.querySelectorAll('input, button');
			for (var i = 0; i < inputs.length; i++) {
				var el = inputs[i];
				if (el.type === 'checkbox' || el.type === 'button' || el.tagName === 'BUTTON') {
					el.click();
					return 'generic-' + el.tagName;
				}
			}
			return 'no-target';
		} catch(e) { return 'err:' + e.message; }
	}`

	for _, frame := range page.Frames() {
		url := frame.URL()
		if !strings.Contains(url, "challenges.cloudflare.com") {
			continue
		}
		// Spoof screen coords in the iframe too
		frame.Evaluate(`() => {
			try {
				function getRandomInt(min, max) { return Math.floor(Math.random() * (max - min + 1)) + min; }
				var sx = getRandomInt(800, 1200);
				var sy = getRandomInt(400, 700);
				Object.defineProperty(MouseEvent.prototype, 'screenX', { value: sx, configurable: true });
				Object.defineProperty(MouseEvent.prototype, 'screenY', { value: sy, configurable: true });
			} catch(e) {}
		}`)
		frame.Evaluate(clickScript)
	}

	// Fallback: click at the checkbox position
	// Widget div is at top:10, left:10, 300x70. Checkbox is ~x=40, y=45 (left side of widget).
	mouse := page.Mouse()
	mouse.Click(40, 45)
}

func ensureXvfb() {
	if os.Getenv("DISPLAY") == "" {
		os.Setenv("DISPLAY", ":2")
	}
	cmd := exec.Command("xdpyinfo", "-display", ":2")
	if err := cmd.Run(); err == nil {
		return
	}
	os.Remove("/tmp/.X2-lock")
	os.Remove("/tmp/.X11-unix/X2")
	exec.Command("setsid", "Xvfb", ":2", "-screen", "0", "1280x720x24").Start()
	time.Sleep(2 * time.Second)
}

func findChromePath() string {
	// 1. 环境变量优先
	for _, key := range []string{"SOLVER_CHROME_PATH", "CHROME_BIN", "CHROME_PATH"} {
		if p := os.Getenv(key); p != "" {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	home, _ := os.UserHomeDir()
	// 2. cloakbrowser 完整 chrome（优先，offscreen 模式 Turnstile 通过率高）
	if entries, err := os.ReadDir(home + "/.cloakbrowser"); err == nil {
		for _, e := range entries {
			if strings.Contains(strings.ToLower(e.Name()), "chrom") {
				p := home + "/.cloakbrowser/" + e.Name() + "/chrome"
				if _, err := os.Stat(p); err == nil {
					return p
				}
			}
		}
	}
	// 3. playwright 完整 chrome
	pwChrome := home + "/.cache/ms-playwright/chromium-1234/chrome-linux/chrome"
	if _, err := os.Stat(pwChrome); err == nil {
		return pwChrome
	}
	// 4. headless_shell（最轻量，但 Turnstile 通过率低，只在没完整 chrome 时用）
	headlessShell := home + "/.cache/ms-playwright/chromium_headless_shell-1234/chrome-linux/headless_shell"
	if _, err := os.Stat(headlessShell); err == nil {
		return headlessShell
	}
	// 5. 系统路径
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// --- crypto helpers ---

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

func secureRandRead(b []byte) {
	rand.Read(b)
}

var _ = regexp.MustCompile
var _ = filepath.Join
