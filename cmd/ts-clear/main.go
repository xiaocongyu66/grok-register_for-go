// Package main: cf_clearance 获取探针 —— B 方案验证。
//
//	理论:GET 全放行、POST 被逐请求挑战。把一个顶层表单 POST(真正的页面导航)
//	打向已知被挑战的 /mp/track/?verbose=1,CF 将挑战页作为顶层文档返回,
//	内核交互式解掉后 cf_clearance 落进 cookie jar,业务 POST 随之解锁。
//
//	流程:
//	  1. serve + 导航 sign-up 页(暖身,GET 放行)
//	  2. JS 构造 <form method=POST> 提交到 /mp/track/?verbose=1&_=<ts>
//	  3. 等挑战自动解开 + 落地
//	  4. GetAllCookies 查 cf_clearance;再用 fetch POST 验证是否解锁
//
//	go run ./cmd/ts-clear -proxy http://127.0.0.1:17890
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"grok-free-register/grok/pkg/obscura"
)

func main() {
	proxy := flag.String("proxy", "http://127.0.0.1:17890", "proxy URL")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, cdpPort, err := obscura.StartObscuraCmdWithOpts(ctx, *proxy, "", 9233, false)
	if err != nil {
		fmt.Println("start:", err)
		return
	}
	defer func() {
		_ = cmd.Process.Kill()
	}()
	client, err := obscura.NewClient(fmt.Sprintf("127.0.0.1:%d", cdpPort))
	if err != nil {
		fmt.Println("client:", err)
		return
	}
	defer client.Close()

	// 暖身:GET 放行,拿到正常页面。
	if err := client.Navigate("https://accounts.x.ai/sign-up?redirect=grok-com"); err != nil {
		fmt.Println("nav:", err)
		return
	}
	time.Sleep(4 * time.Second)
	txt, _ := client.GetText()
	fmt.Printf("[clear] warm page: %.80q\n", txt)

	// 顶层表单 POST → 期待 CF 挑战页(顶层文档,内核可交互解)。
	submitForm := `(function(){
		var f = document.createElement('form');
		f.method = 'POST';
		f.action = 'https://accounts.x.ai/mp/track/?verbose=1&ip=1&_=' + Date.now();
		f.style.display = 'none';
		document.body.appendChild(f);
		f.submit();
		return 'submitted';
	})()`
	r, err := client.Evaluate(submitForm)
	fmt.Printf("[clear] form submit: %v %q\n", r, err)

	// 等导航+挑战解开。阶段式报告。
	for _, wait := range []int{6, 8, 10} {
		time.Sleep(time.Duration(wait) * time.Second)
		st, _ := client.Evaluate(`JSON.stringify({url: location.href.slice(0,80), title: document.title,
			text: (document.body.innerText||'').replace(/\s+/g,' ').slice(0,100)})`)
		fmt.Printf("[clear] t+%ds: %s\n", wait, st)
	}

	// Cookie 检查:cf_clearance 在不在。
	cookies, _ := client.GetAllCookies()
	fmt.Printf("[clear] cookies: %s\n", cookies)

	// 解锁验证:回 sign-up 页用 fetch POST 埋点,看是否还是 403。
	if err := client.Navigate("https://accounts.x.ai/sign-up?redirect=grok-com"); err != nil {
		fmt.Println("re-nav:", err)
		return
	}
	time.Sleep(4 * time.Second)
	client.Evaluate(`(function(){
		window.__mp = 'pending';
		fetch('/mp/track/?verbose=1&ip=1&_=' + Date.now(), {method:'POST', credentials:'include'})
			.then(function(resp){ return resp.text().then(function(t){
				window.__mp = 'HTTP ' + resp.status + ' ct=' + (resp.headers.get('content-type')||'') + ' head=' + t.slice(0, 30);
			});})
			.catch(function(e){ window.__mp = 'ERR:' + e.message; });
	})()`)
	time.Sleep(6 * time.Second)
	mp, _ := client.Evaluate(`window.__mp || 'miss'`)
	fmt.Printf("[clear] mp/track after clearance: %s\n", mp)

	// 最终 Cookie 状态(带 Set-Cookie 与否的差异)。
	cookies2, _ := client.GetAllCookies()
	fmt.Printf("[clear] cookies-final: %s\n", cookies2)
	_ = os.Stdout.Sync()
}
