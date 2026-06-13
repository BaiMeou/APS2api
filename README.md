# Vertex AI Proxy

将 **OpenAI 兼容 API** 和 **Gemini 原生端点** 翻译为对 Google Cloud Console 匿名 batchGraphql 端点的调用，实现免 API Key 使用 Gemini 系列模型。

## 特性

- OpenAI `/v1/chat/completions` 全功能兼容（流式/非流式、工具调用、多模态、reasoning_effort）
- 图像生成/编辑 `/v1/images/generations`
- TTS `/v1/audio/speech`
- Token 计数 `/v1/tokens/count`
- Gemini 原生 `:generateContent` / `:streamGenerateContent`
- TLS 指纹伪装（Chrome profile），通过匿名端点校验
- 内置 reCAPTCHA Enterprise token 获取
- 代理节点池（多出站代理轮转）
- 管理面板（密钥/模型/代理/设置 可视化管理）

## 架构

```
客户端 (OpenAI SDK / curl)
    │
    ▼
┌───────────────────────────────────────────────┐
│  Vertex AI Proxy                              │
│  ┌─────────┐  ┌───────────┐  ┌────────────┐  │
│  │ API 层  │→ │ Transform │→ │ Vertex 层  │  │
│  │(路由/鉴 │  │(OAI↔Gemini│  │(TLS/请求/  │  │
│  │ 权/限流)│  │ 参数转换) │  │ 流式解析)  │  │
│  └─────────┘  └───────────┘  └────────────┘  │
└───────────────────────────────────────────────┘
    │
    ▼
Google batchGraphql (匿名, TLS 指纹校验)
```

## 快速开始

### 编译

```bash
go build -o vertex-proxy ./cmd/vproxy
```

交叉编译（Linux）:
```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o vertex-proxy-linux ./cmd/vproxy
```

### 配置

复制示例配置：
```bash
cp config/config.example.json config/config.json
cp config/api_keys.example.txt config/api_keys.txt
```

编辑 `config/config.json` 设置端口、密码等；`config/api_keys.txt` 每行一个 API 密钥（格式 `name:key:description`）。

### 运行

```bash
./vertex-proxy
```

管理面板访问 `http://localhost:<port>/admin/`。

> Windows / Linux / Android(Termux) 的分平台部署、开机自启、代理配置等详细步骤见 **[部署指南.md](部署指南.md)**；预编译压缩包见 [Releases](../../releases)。

### 代理节点

如需多出站代理（应对 IP 限制），在管理面板「代理节点」页添加代理地址（支持 http/socks5），程序会在请求间轮转使用。

## 配置项

| 字段 | 默认 | 说明 |
|------|------|------|
| `port_api` | 2156 | 监听端口 |
| `admin_password` | 自动生成 | 管理面板密码（留空首启自动生成并写回） |
| `max_retries` | 2 | 上游请求失败重试次数 |
| `proxy_url` | "" | 单一出站代理（http/socks5） |
| `anti429_enabled` | false | 是否插入 anti-429 提示 |
| `force_no_stream` | false | 强制所有请求走非流式 |
| `parallel_pool_enabled` | true | 并发竞速节点池（节点在管理面板「代理与节点」页配置） |

> 假流式：在模型名前加 `fake-` 或 `假流式-` 前缀即可（先完整生成再切片推送），无需配置项。完整字段说明见 [部署指南.md](部署指南.md#六配置说明)。

## License

MIT
