package curlcffi

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

const defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// BrowserEngine 是浏览器引擎的 Go 实现，用 chromedp 替代 Python 的
// SeleniumBase/undetected-chromedriver，用于处理 Cloudflare 质询。
type BrowserEngine struct {
	headless    bool
	proxy       string
	timeout     time.Duration
	fingerprint string
	chromePath  string

	allocCtx    context.Context
	allocCancel context.CancelFunc
	taskCtx     context.Context
	taskCancel  context.CancelFunc

	mu sync.Mutex
}

// NewBrowserEngine 创建一个新的 BrowserEngine。
func NewBrowserEngine(headless bool, proxy string, timeout int, fingerprint string, chromePath string) *BrowserEngine {
	cp := chromePath
	if cp == "" {
		cp = findChromePath()
	}
	return &BrowserEngine{
		headless:    headless,
		proxy:       proxy,
		timeout:     time.Duration(timeout) * time.Second,
		fingerprint: fingerprint,
		chromePath:  cp,
	}
}

// Get 导航到指定 URL，首次调用时懒初始化浏览器。
func (be *BrowserEngine) Get(url string) error {
	if err := be.ensureBrowser(); err != nil {
		return err
	}

	timeout := be.timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	ctx, cancel := context.WithTimeout(be.taskCtx, timeout)
	defer cancel()

	err := chromedp.Run(ctx,
		network.Enable(),
		chromedp.Navigate(url),
		chromedp.Sleep(2*time.Second),
	)
	if err != nil {
		return NewBrowserError(fmt.Sprintf("failed to navigate to %s: %v", url, err))
	}
	return nil
}

// WaitForCloudflare 等待 Cloudflare 5 秒盾/JS 质询完成。
// 轮询检测页面是否包含 CF 标记，最多等待 60 秒。
// 返回 true 如果 CF 质询通过（或页面无 CF 质询）。
func (be *BrowserEngine) WaitForCloudflare() bool {
	if be.taskCtx == nil {
		return false
	}

	maxWait := 60 * time.Second
	ctx, cancel := context.WithTimeout(be.taskCtx, maxWait)
	defer cancel()

	jsDetect := `(function(){
		var html = (document.documentElement && document.documentElement.outerHTML) || '';
		html = html.toLowerCase();
		var markers = ["just a moment", "cf-browser-verification", "challenge platform", "turnstile", "captcha"];
		for (var i = 0; i < markers.length; i++) {
			if (html.indexOf(markers[i]) !== -1) return markers[i];
		}
		return "";
	})()`

	var passed bool
	err := chromedp.Run(ctx,
		chromedp.Sleep(2*time.Second),
		chromedp.ActionFunc(func(ctx context.Context) error {
			deadline := time.Now().Add(maxWait)
			for time.Now().Before(deadline) {
				var marker string
				e := chromedp.Evaluate(jsDetect, &marker).Do(ctx)
				if e != nil {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(2 * time.Second):
					}
					continue
				}

				if marker == "" {
					passed = true
					return nil
				}

				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(2 * time.Second):
				}
			}
			return nil
		}),
	)

	return err == nil && passed
}

// GetCookies 获取浏览器 cookies，返回 cookie name→value 映射。
func (be *BrowserEngine) GetCookies() map[string]string {
	if be.taskCtx == nil {
		return map[string]string{}
	}

	ctx, cancel := context.WithTimeout(be.taskCtx, 10*time.Second)
	defer cancel()

	cookies, err := network.GetCookies().Do(ctx)
	if err != nil {
		return map[string]string{}
	}

	result := make(map[string]string, len(cookies))
	for _, c := range cookies {
		result[c.Name] = c.Value
	}
	return result
}

// GetHeaders 返回默认 headers（User-Agent 等）。
// 如果浏览器已启动，从浏览器执行 JS 获取 navigator.userAgent。
func (be *BrowserEngine) GetHeaders() map[string]string {
	headers := map[string]string{
		"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
		"Accept-Language":           "en-US,en;q=0.5",
		"Accept-Encoding":           "gzip, deflate, br",
		"Connection":                "keep-alive",
		"Upgrade-Insecure-Requests": "1",
		"Sec-Fetch-Dest":            "document",
		"Sec-Fetch-Mode":            "navigate",
		"Sec-Fetch-Site":            "none",
		"Sec-Fetch-User":            "?1",
	}

	ua := be.fetchUserAgent()
	if ua == "" {
		ua = defaultUserAgent
	}
	headers["User-Agent"] = ua

	return headers
}

// Close 关闭浏览器上下文并释放资源。
func (be *BrowserEngine) Close() {
	be.mu.Lock()
	defer be.mu.Unlock()
	if be.taskCancel != nil {
		be.taskCancel()
		be.taskCancel = nil
		be.taskCtx = nil
	}
	if be.allocCancel != nil {
		be.allocCancel()
		be.allocCancel = nil
		be.allocCtx = nil
	}
}

func (be *BrowserEngine) allocOpts() []chromedp.ExecAllocatorOption {
	opts := []chromedp.ExecAllocatorOption{
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.DisableGPU,
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("enable-automation", false),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("single-process", true),
		chromedp.Flag("no-zygote", true),
		chromedp.WindowSize(1280, 720),
		chromedp.UserAgent(defaultUserAgent),
	}
	if be.headless {
		opts = append(opts, chromedp.Headless)
	} else {
		opts = append(opts, chromedp.Flag("headless", false))
	}
	if be.chromePath != "" {
		opts = append(opts, chromedp.ExecPath(be.chromePath))
	}
	if be.proxy != "" {
		opts = append(opts, chromedp.ProxyServer(be.proxy))
	}
	return opts
}

func (be *BrowserEngine) ensureBrowser() error {
	be.mu.Lock()
	defer be.mu.Unlock()
	if be.allocCtx != nil {
		return nil
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), be.allocOpts()...)
	taskCtx, taskCancel := chromedp.NewContext(allocCtx)

	be.allocCtx = allocCtx
	be.allocCancel = allocCancel
	be.taskCtx = taskCtx
	be.taskCancel = taskCancel
	return nil
}

func (be *BrowserEngine) fetchUserAgent() string {
	if be.taskCtx == nil {
		return ""
	}

	ctx, cancel := context.WithTimeout(be.taskCtx, 5*time.Second)
	defer cancel()

	var ua string
	err := chromedp.Run(ctx, chromedp.Evaluate(`navigator.userAgent`, &ua))
	if err != nil {
		return ""
	}
	return ua
}

// findChromePath 查找系统中的 Chromium/Chrome 二进制文件路径。
// 检查 PATH 中的 chromium/chrome，以及 ~/.cloakbrowser/ 目录。
func findChromePath() string {
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}

	home, _ := os.UserHomeDir()
	if home != "" {
		cloakBrowser := filepath.Join(home, ".cloakbrowser")
		if entries, err := os.ReadDir(cloakBrowser); err == nil {
			for _, e := range entries {
				if strings.Contains(strings.ToLower(e.Name()), "chrom") {
					p := filepath.Join(cloakBrowser, e.Name(), "chrome")
					if _, err := os.Stat(p); err == nil {
						return p
					}
				}
			}
		}
	}

	return ""
}
