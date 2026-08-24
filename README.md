<div align="center">

# 🚀 grok-register_for-go

**纯 Go 实现的 Grok 账号注册引擎 + 多协议代理中继 + Turnstile 求解**

一站式解决方案:抓取代理 → 中继转换 → 求解验证码 → 注册账号 → 推送 SSO

[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Platform](https://img.shields.io/badge/Platform-Linux%20%7C%20macOS%20%7C%20Docker-lightgrey)]()
[![Build](https://img.shields.io/github/actions/workflow/status/xiaocongyu66/grok-register_for-go/release.yml?branch=main&label=Build)](https://github.com/xiaocongyu66/grok-register_for-go/actions)

</div>

---

## ✨ 核心特性

### 🎯 注册引擎
- **协议化注册** — 用 curl_cffi(TLS 指纹伪装)直接发 HTTP 请求,不走重量级浏览器自动化
- **多账号并发** — 多 worker 并行注册,每个 worker 独立代理
- **SSO 智能提取** — 自动区分登录票据与真正 session SSO,只保存可用 token
- **分批保存** — 按时间戳分批输出 `sso.txt` / `grok2api.txt`,便于增量导入

### 🌐 多协议代理中继(minirelay)
用 sing-box 底层包实现,支持 9 种协议转本地 HTTP 代理:

| 协议 | 说明 |
|------|------|
| **hysteria2 / hy2** | QUIC + Salamander 混淆 + 端口跳跃(mport) |
| **vless** | Reality + XTLS-Vision |
| **trojan** | TLS + WebSocket |
| **vmess** | AEAD + WebSocket |
| **shadowsocks (ss)** | AEAD 加密 |
| **tuic** | QUIC 协议 |
| **shadowtls** | TLS 伪装 |
| **socks5 / http** | 直连代理 |

### 🛡️ 代理池健康检查
- **启动验证** — 所有节点 TCP/UDP 连通性测试,不通的降级(不删除)
- **自动恢复** — 每 2 分钟重测不健康节点,恢复后自动启用
- **自动换死节点** — 运行时节点失败自动切换,保留可恢复性

### 🔐 SSO 提取与兑换
- **JWT payload 校验** — 区分登录票据(含 `config`)与真正 SSO(含 `session_id`)
- **success_url 解析** — 从票据 payload 提取 `config.success_url`,用内层 JWT 兑换
- **Cookie jar 修复** — 手动存 Set-Cookie + 手动跟随重定向,解决跨域 cookie 丢失

### 🚀 grok2api 直推(可选)
注册成功后自动推送到 [grok2api](https://github.com/chenyme/grok2api) 服务器:
- 401 自动重试(admin token 过期)
- `syncFailed` 检查(检测无效 token)
- SSE 完整解析
- 支持自动分配代理节点

### 🤖 Turnstile 求解
- 内置 Turnstile 验证码求解器
- 浏览器自动化(Chromium)处理 Cloudflare 质询
- 求解结果缓存复用

---

## 📦 项目结构

```
├── cmd/                    # 入口
│   ├── grok/              # 主命令(注册/抓取/初始化)
│   └── minirelay/         # 独立中继服务
├── pkg/
│   ├── engine/            # 注册引擎核心
│   │   ├── xai_client.go  # XAI 客户端(SSO 提取/注册流程)
│   │   ├── pipeline.go    # 注册流水线
│   │   ├── engine.go      # 引擎主循环 + 代理池
│   │   ├── grok2api_pusher.go  # grok2api 直推
│   │   ├── browser_pool.go     # 浏览器池(turnstile)
│   │   └── turnstile.go   # Turnstile 求解
│   ├── curlcffi/          # TLS 指纹伪装 HTTP 客户端
│   ├── minirelay/         # 多协议代理中继
│   │   ├── quic_outbound.go    # hysteria2/tuic
│   │   ├── vless_outbound.go   # vless
│   │   ├── vmess_trojan.go     # vmess/trojan
│   │   └── ss_outbound.go      # shadowsocks
│   └── cli/               # 代理抓取 + 测试
│       ├── proxy_scraper.go    # clash.yml/订阅抓取
│       └── relay_test_nodes.go # 中继节点测试
├── static/                # 静态资源
├── go.mod
└── go.sum
```

---

## 🚀 快速开始

### 方式一:下载预编译二进制(推荐)

从 [Releases](https://github.com/xiaocongyu66/grok-register_for-go/releases) 下载对应平台的二进制:

```bash
# Linux amd64 示例
curl -L -o grok https://github.com/xiaocongyu66/grok-register_for-go/releases/latest/download/grok-linux-amd64
chmod +x grok
./grok version
```

支持平台:
- `linux-amd64` / `linux-arm64`
- `linux-386` / `linux-arm`
- `darwin-amd64` / `darwin-arm64`(macOS)

### 方式二:源码编译

```bash
git clone https://github.com/xiaocongyu66/grok-register_for-go.git
cd grok-register_for-go

# 需要 -tags with_utls(hy2/vless 等 utls 支持)
go build -tags with_utls -o grok ./cmd/grok

# 检测/安装运行环境(Chrome + Xvfb)
./grok init --install
```

### 3. 配置

```bash
cp .env.example .env
```

`.env` 关键配置:

```bash
# 邮箱模式(moemail 需 API Key)
EMAIL_MODE=moemail
MOEMAIL_API=https://your-moemail-instance.com
MOEMAIL_API_KEY=mk_xxx
MOEMAIL_DOMAIN=your-domain.com

# 注册并发
TARGET=100
GO_REGISTER_WORKERS=3

# 代理文件
PROXY_POOL_FILE=代理.txt

# 可选:grok2api 直推
GROK2API_PUSH_ENABLED=1
GROK2API_BASE_URL=http://your-grok2api:18000
GROK2API_ADMIN_USER=admin
GROK2API_ADMIN_PASS=your-password
GROK2API_PROVIDER=grok_web
```

### 4. 抓取代理(可选)

```bash
./grok scrape --timeout 30
```

### 5. 注册账号

```bash
./grok register --target 50 --workers 3
```

---

## 📤 输出格式

注册成功后输出到 `keys/`:

| 文件 | 格式 | 用途 |
|------|------|------|
| `sso.txt` | `email:sso` | 主输出,导入 grok2api 的 Web 账号 |
| `YYYYMMDDHHMM-sso.txt` | `email:sso` | 分批输出 |
| `YYYYMMDDHHMM-grok2api.txt` | 纯 JWT | 分批输出(纯 SSO token) |

### 导入 grok2api

```bash
curl -X POST http://grok2api:18000/api/admin/v1/accounts/web/import \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -F "files=@keys/sso.txt"
```

---

## 🔧 高级配置

### grok2api 直推

| 环境变量 | 说明 | 默认 |
|---------|------|------|
| `GROK2API_PUSH_ENABLED` | 启用推送 | 0 |
| `GROK2API_BASE_URL` | grok2api 地址 | - |
| `GROK2API_ADMIN_TOKEN` | accessToken(优先) | - |
| `GROK2API_ADMIN_USER/PASS` | 用户名密码 | - |
| `GROK2API_PROVIDER` | 推送目标 | grok_web |
| `GROK2API_PROXY_POOL_FILE` | 代理文件(一并导入) | - |
| `GROK2API_PROXY_ASSIGN` | 自动分配代理节点 | 0 |

### 独立 minirelay

```bash
./minirelay --listen 127.0.0.1:19300 --upstream hysteria2://...
```

---

## 🏗️ 架构

```
┌─────────────────────────────────────────────────────────┐
│                    注册引擎 (engine)                     │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌─────────┐ │
│  │ ProxyPool│  │ Turnstile│  │  XaiClient│  │ Pusher  │ │
│  │ 健康检查  │  │  求解器   │  │ SSO 提取  │  │ grok2api│ │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘  └────┬────┘ │
│       ▼              ▼              ▼              ▼      │
│  ┌─────────────────────────────────────────────────┐    │
│  │              curlcffi (TLS 指纹伪装)             │    │
│  └─────────────────────┬───────────────────────────┘    │
│                        │                                 │
│  ┌─────────────────────▼───────────────────────────┐    │
│  │              minirelay (代理中继)                │    │
│  │  hy2 / vless / trojan / ss / vmess / tuic / ss  │    │
│  └─────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────┘
```

---

## 📋 命令参考

```bash
# 初始化环境
grok init [--install]               # 检测/安装 Chrome + Xvfb

# 抓取代理
grok scrape [--timeout T]            # clash.yml + 订阅 + 测活

# 注册账号
grok register [--target N] [-w W]   # 注册 N 个账号,W 并发

# 版本
grok version
```

---

## ⚠️ 注意事项

- **仅用于学习和研究** — 请遵守 Grok 官方服务条款和当地法律法规
- **无硬编码密钥** — 所有凭据通过环境变量读取(`.env` 被 gitignore)
- **代理隐私** — 代理文件不入库,敏感信息不提交
- **SSO 时效** — Web SSO 不可续期,失效需重新注册(或转成 Build OAuth)

---

## 📄 License

MIT License - 详见 [LICENSE](LICENSE)

---

<div align="center">

**如果这个项目对你有帮助,欢迎 ⭐ Star**

</div>
