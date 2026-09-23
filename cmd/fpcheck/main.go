// Package main: 指纹一致性自检 —— 对照用户给的检测维度清单。
//
//	覆盖:UA↔navigator↔ClientHints 同源、navigator.webdriver、window.chrome、
//	hardwareConcurrency、Worker 内 navigator 一致性、WebGL vendor/renderer、
//	plugins/languages、Notification.permission。
//
//	go run ./cmd/fpcheck -proxy http://127.0.0.1:17890
package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"grok-free-register/grok/pkg/obscura"
)

func main() {
	proxy := flag.String("proxy", "http://127.0.0.1:17890", "proxy URL")
	url := flag.String("url", "https://example.com", "probe page")
	port := flag.Int("port", 9251, "CDP port")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, cdpPort, err := obscura.StartObscuraCmdWithOpts(ctx, *proxy, "", *port, false)
	if err != nil {
		fmt.Println("start:", err)
		return
	}
	defer cmd.Process.Kill()
	client, err := obscura.NewClient(fmt.Sprintf("127.0.0.1:%d", cdpPort))
	if err != nil {
		fmt.Println("client:", err)
		return
	}
	defer client.Close()

	if err := client.Navigate(*url); err != nil {
		fmt.Println("nav:", err)
		return
	}
	time.Sleep(4 * time.Second)
	client.Evaluate(`(function(){ try { return globalThis.__obscura_probe_ran !== undefined; } catch(e) { return 'err'; } })()`)

	kernelUA, _ := client.GetUserAgent()
	fmt.Printf("kernel UA header: %s\n\n", kernelUA)

	// 主线程信号。
	main := `(function(){
		var out = {};
		out.userAgent = navigator.userAgent;
		out.platform = navigator.platform;
		out.hardwareConcurrency = navigator.hardwareConcurrency;
		out.deviceMemory = navigator.deviceMemory;
		out.webdriver = navigator.webdriver;
		out.chrome = typeof window.chrome !== 'undefined';
		out.chromeKeys = (typeof window.chrome !== 'undefined') ? Object.keys(window.chrome).join(',') : '';
		out.languages = (navigator.languages||[]).join(',');
		out.language = navigator.language;
		out.plugins = navigator.plugins ? navigator.plugins.length : -1;
		try { out.notification = Notification.permission; } catch(e) { out.notification = 'throw:'+e.message; }
		out.uadBrands = (navigator.userAgentData && navigator.userAgentData.brands) ? navigator.userAgentData.brands.map(function(b){return b.brand+'|'+b.version;}).join(',') : 'undefined';
		out.uadPlatform = navigator.userAgentData ? navigator.userAgentData.platform : 'undefined';
		out.uadMobile = navigator.userAgentData ? navigator.userAgentData.mobile : 'undefined';
		// WebGL
		try {
			var c = document.createElement('canvas');
			var gl = c.getContext('webgl') || c.getContext('experimental-webgl');
			if (gl) {
				var dbg = gl.getExtension('WEBGL_debug_renderer_info');
				out.glVendor = gl.getParameter(gl.VENDOR);
				out.glRenderer = gl.getParameter(gl.RENDERER);
				if (dbg) {
					out.glUnmaskedVendor = gl.getParameter(dbg.UNMASKED_VENDOR_WEBGL);
					out.glUnmaskedRenderer = gl.getParameter(dbg.UNMASKED_RENDERER_WEBGL);
				}
			} else { out.glVendor = 'no-webgl'; }
		} catch(e) { out.glVendor = 'throw:'+e.message; }
		return JSON.stringify(out);
	})()`
	mr, _ := client.Evaluate(main)
	fmt.Println("── main thread ──")
	printKV(mr)

	// Worker 一致性:Blob Worker 上报自己的 navigator(挂全局轮询读取)。
	client.Send("Runtime.enable", nil)
	client.Evaluate(`(function(){
		window.__wk = 'pending';
		try {
			var src = "postMessage({ua:navigator.userAgent,hc:navigator.hardwareConcurrency,plat:navigator.platform,uad:(navigator.userAgentData&&navigator.userAgentData.platform)||'undefined',hasUAD:typeof navigator.userAgentData})";
			var w = new Worker(URL.createObjectURL(new Blob([src], {type:'text/javascript'})));
			w.onmessage = function(e){ window.__wk = JSON.stringify(e.data); };
			setTimeout(function(){ if(window.__wk==='pending') window.__wk='worker-timeout'; }, 5000);
		} catch(e) { window.__wk = 'throw:'+e.message; }
	})()`)
	time.Sleep(6 * time.Second)
	wr, _ := client.Evaluate(`window.__wk || 'miss'`)
	fmt.Println("── worker ──")
	fmt.Println(wr)

	// 一致性判定:UA 是否与内核头一致;Worker UA 是否与主线程一致。
	var mo map[string]interface{}
	fmt.Sscanf(mr, "%s", &mr)
	_ = mo
	fmt.Println("\n── coherence checks ──")
	fmt.Printf("UA header == navigator.userAgent : %v\n", strings.Contains(mr, kernelUA))
}

func printKV(js string) {
	// 粗暴 but 足够:按 "key":"value" 打表。
	s := strings.Trim(js, `"`)
	s = strings.ReplaceAll(s, `\"`, `"`)
	s = strings.ReplaceAll(s, `\n`, "\n")
	for _, line := range strings.Split(s, `","`) {
		fmt.Println(strings.TrimPrefix(strings.TrimSuffix(line, `"`), `"`))
	}
}
