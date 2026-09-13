// ts-diag: x.ai Turnstile widget DOM 诊断。
// 走 solveTurnstileObscura 同款流程，然后 dump widget 容器/iframe/shadow 状态。
package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"grok-free-register/grok/pkg/engine"
	"grok-free-register/grok/pkg/obscura"
)

func main() {
	proxy := flag.String("proxy", "", "proxy")
	flag.Parse()

	browserProxy := engine.MaybeRelayProxy(*proxy)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, port, err := obscura.StartObscuraCmdWithOpts(ctx, browserProxy, "", 9222, false)
	if err != nil {
		fmt.Printf("start: %v\n", err)
		return
	}
	defer cmd.Process.Kill()

	client, err := obscura.NewClient(fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		fmt.Printf("cdp: %v\n", err)
		return
	}
	defer client.Close()

	netCh := client.CaptureNetworkRequests()
	go func() {
		for req := range netCh {
			if len(req.URL) > 0 && (len(req.URL) < 100 || true) {
				fmt.Printf("[net] %s %s\n", req.Method, req.URL[:min(110, len(req.URL))])
			}
		}
	}()

	ua := engine.RandomUAProfile()
	if kua, err := client.GetUserAgent(); err == nil && len(kua) > 20 {
		ua = engine.ProfileFromKernelUA(kua, ua)
		fmt.Printf("[diag] kernel UA: %s\n", kua)
	}
	client.InjectUAProfile(ua.UserAgent, ua.CHUA, ua.CHUAPlatform)

	if err := client.Navigate("https://accounts.x.ai/sign-up?redirect=grok-com"); err != nil {
		fmt.Printf("nav: %v\n", err)
		return
	}
	time.Sleep(4 * time.Second)
	client.Evaluate(engine.BuildNavOverrideScript(ua))
	client.Evaluate(`(() => { var s=document.createElement('script'); s.src='https://challenges.cloudflare.com/turnstile/v0/api.js'; s.async=true; document.head.appendChild(s); })()`)

	ready := false
	for i := 0; i < 90; i++ {
		r, _ := client.Evaluate("typeof turnstile !== 'undefined' ? 'yes' : 'no'")
		if r == "yes" { ready = true; break }
		time.Sleep(500 * time.Millisecond)
	}
	fmt.Printf("[diag] turnstile ready: %v\n", ready)
	if !ready { return }

	client.Evaluate(`(() => { try { turnstile.render(document.body, { sitekey: '0x4AAAAAAAhr9JGVDZbrZOo0', callback: function(t){ window.__ts_token=t; }, 'error-callback': function(e){ window.__ts_err=e; } }); } catch(e) { window.__ts_err='render:'+e.message; } })()`)
	time.Sleep(8 * time.Second)

	// 页面文本:看 React 水合状态。
	pageD, _ := client.Evaluate(`JSON.stringify({title: document.title, text: (document.body.innerText||'').slice(0, 300), url: location.href})`)
	fmt.Printf("[diag] page: %s\n", pageD)

	// 表单结构:email input / 提交按钮。
	time.Sleep(2 * time.Second)
	formD, _ := client.Evaluate(`(function(){
  var inputs = Array.from(document.querySelectorAll('input')).map(function(i){ return {type: i.type, name: i.name||i.id, placeholder: (i.placeholder||'').slice(0,30), visible: i.getBoundingClientRect().width > 0}; });
  var btns = Array.from(document.querySelectorAll('button, [role=button]')).map(function(b){ return {text: (b.textContent||'').trim().slice(0,30), type: b.type||'', visible: b.getBoundingClientRect().width > 0}; }).filter(function(b){ return b.visible; });
  return JSON.stringify({inputs: inputs.slice(0,8), buttons: btns.slice(0,8)});
})()`)
	fmt.Printf("[diag] form: %s\n", formD)

	// fetch 探测:网络层直测 challenges.cloudflare.com。
	client.Evaluate(`(() => { window.__f = 'pending'; fetch('https://challenges.cloudflare.com/cdn-cgi/trace', {mode:'cors'}).then(function(r){ return r.text(); }).then(function(t){ window.__f = t.slice(0, 120); }).catch(function(e){ window.__f = 'ERR:' + e.message; }); })()`)
	time.Sleep(8 * time.Second)
	fState, _ := client.Evaluate(`window.__f || 'unset'`)
	fmt.Printf("[diag] fetch-probe: %s\n", fState)

	// CSP 探测:内核里直接建 challenges.cloudflare.com iframe,看是否被 CSP 阻止。
	client.Evaluate(`(() => { window.__cf = 'pending'; var f = document.createElement('iframe'); f.src = 'https://challenges.cloudflare.com/cdn-cgi/trace'; f.onload = function(){ window.__cf = 'loaded'; }; f.onerror = function(){ window.__cf = 'error'; }; document.body.appendChild(f); })()`)
	time.Sleep(6 * time.Second)
	cfState, _ := client.Evaluate(`JSON.stringify({cf: window.__cf, frames: Array.from(document.querySelectorAll('iframe')).map(function(f){ return (f.src||'').slice(0,60); }).slice(0,6)})`)
	fmt.Printf("[diag] csp-probe: %s\n", cfState)

	// 对照实验:x.ai origin + demo 测试 sitekey——区分页面环境 vs sitekey/origin。
	client.Evaluate(`(() => { try { window.__t2=''; turnstile.render(document.body, { sitekey: '1x00000000000000000000AA', callback: function(t){ window.__t2=t; }, 'error-callback': function(e){ window.__t2e=e; } }); } catch(e) { window.__t2e='render:'+e.message; } })()`)
	time.Sleep(10 * time.Second)
	d2, _ := client.Evaluate(`JSON.stringify({t2: (window.__t2||'').slice(0,30), t2e: window.__t2e||'', inputs: document.querySelectorAll('input[name=cf-turnstile-response]').length})`)
	fmt.Printf("[diag] demo-key-on-xai: %s\n", d2)

	dump, _ := client.Evaluate(`(function(){
  var out = {token: window.__ts_token||'', err: window.__ts_err||'', widgets: []};
  var hosts = document.querySelectorAll('[class*=cf-turnstile], [id^=cf-chl-widget], div[style*="position: absolute"], iframe');
  hosts.forEach(function(el){
    var r = el.getBoundingClientRect();
    out.widgets.push({
      tag: el.tagName, id: el.id, cls: (el.className||'').toString().slice(0,60),
      rect: [Math.round(r.x), Math.round(r.y), Math.round(r.width), Math.round(r.height)],
      src: (el.src||'').slice(0,80),
      children: el.childElementCount,
      html: el.outerHTML.slice(0, 200),
    });
  });
  out.bodyChildren = document.body.childElementCount;
  out.bodyHTMLTail = document.body.innerHTML.slice(-600);
  return JSON.stringify(out);
})()`)
	fmt.Printf("[diag] dump: %s\n", dump)
}
