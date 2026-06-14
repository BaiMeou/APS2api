# Vertex AI Proxy

免费使用 Google Gemini 模型的代理工具。把 **OpenAI 兼容的 API 请求**翻译成对 Google 匿名端点的调用——你的客户端以为在调 OpenAI，实际上用的是 Gemini。

**单文件程序，不需要装任何东西，解压就能跑。** 支持 Windows、Linux、Android 手机。

## 能做什么

- 兼容 OpenAI 的聊天接口（流式/非流式、工具调用、多模态、图片输入）
- 文生图 / 图片编辑
- 语音合成（TTS）
- Token 计数
- Gemini 原生端点透传
- 内置 TLS 指纹伪装，通过 Google 匿名端点校验
- 内置 reCAPTCHA token 自动获取
- 代理节点池（内嵌 sing-box，应对网络限制）
- 管理面板（浏览器里可视化管理密钥、模型、代理、设置）

## 三步上手

**1. 解压**，把压缩包解压到任意位置。

**2. 启动**：
- Windows：双击 `启动.bat`
- Linux：`chmod +x start.sh vertex-proxy && ./start.sh`
- Android(Termux)：`sh start.sh`

**3. 填密钥**：编辑 `config/api_keys.txt`，写一行你自己的密钥（格式 `名称:sk-你的密钥:备注`），重启程序。

客户端的 API Key 填你写的那个 `sk-...`，API 地址填 `http://你的地址:2156/v1`，就能用了。

> 首次启动时会自动打印一个管理员密码，用它登录 `http://你的地址:2156/admin/` 管理面板。

**完整的分平台部署教程**（包括开机自启、代理配置、手机部署、常见问题解答）见 **[部署指南](部署指南.md)**。

## 自己编译（可选）

如果你想从源码编译：

```bash
go build -o vertex-proxy ./cmd/vproxy
```

交叉编译（比如在 Mac/Windows 上编译 Linux 版本）：
```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o vertex-proxy ./cmd/vproxy
```

## 配置

大部分设置可以在管理面板的「设置」页直接改，不用编辑文件。如果需要手动改，配置文件是 `config/config.json`：

| 选项 | 默认 | 说明 |
|------|------|------|
| `port_api` | 2156 | 监听端口 |
| `admin_password` | 自动生成 | 管理面板密码 |
| `max_retries` | 2 | 失败重试次数 |
| `proxy_url` | 空 | 出站代理地址 |
| `parallel_pool_enabled` | true | 并发竞速节点池 |

模型名加 `fake-` 或 `假流式-` 前缀可以把非流式模型伪装成流式输出。

详细配置说明见 [部署指南](部署指南.md#配置怎么改)。

## License

[PolyForm Noncommercial License 1.0.0](LICENSE) — 本项目面向非商业用途（个人、公益、教育、研究等）。商业使用不在授权范围内。
