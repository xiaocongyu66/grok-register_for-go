# Obscura 增强方案

## 来源参考
- **obscura** (h4ckf0r0day/obscura) — Rust headless 浏览器,V8 via deno_core,CDP 端口
- **browser_oxide** (yfedoseev/browser_oxide) — iframe srcdoc 脚本执行,postMessage 双向通信
- **stygian** (greysquirr3l/stygian) — 指纹生成,CDP 硬化,canvas/audio 噪声,行为模拟,MCP,代理轮换
- **fingerprint-rs** (grahambrooks/fingerprint-rs) — 文档指纹(Jaccard 相似度,不是浏览器指纹)

## 要移植的能力

### 1. iframe 脚本执行 (from browser_oxide)
- 修复 `_IframeDocument.write()`: 解析 `<script>` 标签并执行(inline + external fetch)
- CDP Runtime.evaluate 后调用 `advance_frames()` 投递 pending frame messages
- **文件**: `crates/obscura-js/js/bootstrap.js` (已修改), `crates/obscura-cdp/src/domains/runtime.rs` (已修改)

### 2. iframe postMessage 双向通信 (from browser_oxide)
- 给 child realm 安装 `addEventListener`/`dispatchEvent`/`__deliverMessage`
- parent→child: `cw.postMessage()` 通过 `__deliverMessage` 投递
- child→parent: `parent.postMessage()` 通过 `dispatchEvent(MessageEvent)` 投递
- `event.source === iframe.contentWindow` 身份保持
- **文件**: `crates/obscura-js/js/bootstrap.js` (_IframeWindow 区域)

### 3. 指纹生成 (from stygian)
- `Fingerprint` struct: screen/timezone/language/hardwareConcurrency/deviceMemory/WebGL vendor+renderer
- `Fingerprint::random()` — 随机生成一致的指纹
- `Fingerprint::from_device_profile()` — 从设备 profile 生成
- `injection_script()` — 生成 JS 注入脚本(addScriptToEvaluateOnNewDocument)
- Canvas noise: 确定性 per-session 像素扰动
- Audio noise: AudioBuffer/AnalyserNode/OfflineAudioContext 噪声
- **文件**: 新建 `crates/obscura-browser/src/fingerprint.rs`

### 4. CDP 硬化 (from stygian)
- 删除 Playwright/Puppeteer 绑定残余
- 清理 `Error.prototype.stack` 中的 CDP frame URLs
- 硬化 `console.debug` 防止 getter-trap 检测
- `Navigator.prototype.webdriver` 属性描述符匹配 Chrome 原生格式
- 清理 `cdc_`/`_cdc_`/`domAutomation` 等自动化痕迹
- **文件**: 新建 `crates/obscura-browser/src/cdp_hardening.rs`

### 5. 行为模拟 (from stygian)
- `MouseSimulator` — 贝塞尔曲线鼠标轨迹(距离感知)
- `TypingSimulator` — 变速打字 + 自然停顿
- `InteractionSimulator` — 随机滚动和微移动
- **文件**: 新建 `crates/obscura-browser/src/behavior.rs`

### 6. MCP 接口 (from stygian)
- JSON-RPC 2.0 stdin/stdout MCP server
- 工具: `browser_acquire`, `browser_navigate`, `browser_click`, `browser_evaluate`, `browser_screenshot`
- 代理工具: `proxy_add`, `proxy_rotate`, `proxy_health`
- 指纹工具: `fingerprint_generate`, `fingerprint_inject`
- **文件**: 新建 `crates/obscura-mcp/`

### 7. 网络抓包接口
- CDP `Network.requestWillBeSent` / `Network.responseReceived` 事件捕获
- 导出 HAR 格式
- WebSocket 消息捕获
- **文件**: `crates/obscura-cdp/src/domains/network.rs` (扩展)

### 8. 多实例隔离接口
- 每个 worker 启动独立 obscura 进程(不同端口)
- 独立指纹(seed-based)
- 独立代理
- 独立 storage_dir
- Go 客户端管理多个实例的生命周期
- **文件**: `pkg/obscura/client.go` (扩展 Pool 模式)

### 9. WS 接口
- CDP WebSocket 已有(ws://127.0.0.1:9222/devtools/browser)
- 额外: 暴露 WebSocket API 给外部客户端(实时推送页面事件)
- **文件**: `crates/obscura-cdp/src/server.rs` (扩展)

## 实施顺序
1. iframe 脚本执行 (已修改,待编译)
2. CDP evaluate 后 pump frames (已修改,待编译)
3. iframe postMessage (移植 browser_oxide 的 dom_bootstrap.js)
4. 指纹生成 (移植 stygian fingerprint.rs)
5. CDP 硬化 (移植 stygian cdp_hardening.rs)
6. 多实例隔离 (Go 客户端 Pool)
7. MCP 接口 (移植 stygian MCP 结构)
8. 网络抓包 (CDP Network domain 扩展)
9. 行为模拟 (移植 stygian behavior.rs)
10. WS 接口 (CDP server 扩展)
