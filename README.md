# Vertex AI Proxy

Vertex AI Proxy 是一个面向自用部署的 Vertex/Gemini 代理服务，提供 OpenAI 兼容接口和简单的管理后台。你可以通过管理后台维护 API Keys、模型列表、订阅节点和出站代理配置，然后用 OpenAI SDK 或兼容客户端访问本服务。

默认监听端口为 `2156`。

## 功能特性

- OpenAI 兼容接口：`/v1/chat/completions`、`/v1/models` 等。
- Gemini 兼容接口：`/v1beta/models/...`。
- 管理后台：`/admin`。
- API Key 认证，密钥可在管理后台维护。
- 支持出站代理，可通过 `.env` 的 `PROXY_URL` 或管理后台配置。
- 支持订阅节点导入、节点选择和节点池。
- Docker Compose 一键部署。
- 模板配置与真实运行配置分离，便于安全分发源码。

## 快速开始：Docker Compose

Docker Compose 是推荐部署方式。

```powershell
cp .env.example .env
docker compose up -d
docker compose logs -f
```

如需出站代理，编辑 `.env`：

```env
PROXY_URL=socks5://127.0.0.1:7890
```

如需修改宿主机访问端口，编辑 `.env`：

```env
PORT=2156
```

`PORT` 只影响 Docker 的宿主机端口映射。容器内应用默认仍监听 `2156`。

###安卓部署

使用termux，安装proot-distro
进入vertex目录
proot-distro build .
proot-distro run vertex
即可运行

## 首次启动

首次启动时，程序会自动初始化本地运行配置：

- 从 `config/config.example.json` 生成 `config/config.json`。
- 从 `config/api_keys.example.txt` 生成 `config/api_keys.txt`。
- 如果没有管理员密码，会自动生成并打印到日志。

查看日志：

```powershell
docker compose logs -f
```

管理后台地址：

```text
http://localhost:2156/admin
```

登录后可以维护：

- API Keys
- 模型列表和别名
- 订阅节点
- 当前出站代理
- 常用运行设置

## API 使用

OpenAI 兼容 Base URL：

```text
http://localhost:2156/v1
```

请求需要使用管理后台添加的 API Key：

```powershell
curl http://localhost:2156/v1/chat/completions `
  -H "Content-Type: application/json" `
  -H "Authorization: Bearer sk-your-api-key" `
  -d '{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"Hello"}]}'
```

模型名称以管理后台中的模型列表为准。

## 配置文件说明

项目有意区分部署配置、应用设置、敏感数据和运行状态，不建议把所有内容都塞进 `.env`。

| 文件 | 用途 | 是否应提交 |
| --- | --- | --- |
| `.env.example` | 环境变量模板 | 是 |
| `.env` | 本地部署环境变量，例如 `PORT`、`PROXY_URL` | 否 |
| `config/config.example.json` | 应用设置模板 | 是 |
| `config/config.json` | 真实应用设置，管理后台会写入 | 否 |
| `config/api_keys.example.txt` | API Key 文件模板 | 是 |
| `config/api_keys.txt` | 真实 API Key 列表，管理后台会写入 | 否 |
| `config/models.json` | 模型列表和别名 | 是 |
| `config/nodes.json` | 订阅节点运行态数据 | 否 |
| `config/node_health.json` | 节点健康状态 | 否 |

### `.env` 加载优先级

Docker Compose 会通过 `env_file: .env` 注入环境变量。

非 Docker 运行时，程序也会读取项目根目录的 `.env`，但不会覆盖已经存在的系统环境变量。优先级为：

```text
系统环境变量 > .env > config/config.json > 程序默认值
```

注意：`PORT` 目前用于 Docker Compose 的宿主机端口映射。非 Docker 运行时如果要修改应用监听端口，请在管理后台或 `config/config.json` 中修改 `port_api`。

## 本地运行：非 Docker

本地运行更适合开发和调试。不要把依赖直接安装到全局 Python，建议使用虚拟环境。

推荐 Python 版本与 Dockerfile 保持一致：Python 3.12。

### Windows PowerShell

```powershell
python -m venv .venv
.\.venv\Scripts\Activate.ps1
python -m pip install --upgrade pip
pip install -r requirements.txt
python main.py
```

如果 PowerShell 阻止激活脚本，可以在当前终端会话中临时放宽策略：

```powershell
Set-ExecutionPolicy -Scope Process -ExecutionPolicy Bypass
.\.venv\Scripts\Activate.ps1
```

### Linux / macOS

```bash
python3 -m venv .venv
source .venv/bin/activate
python -m pip install --upgrade pip
pip install -r requirements.txt
python main.py
```

退出虚拟环境：

```bash
deactivate
```

非 Docker 运行时也会读取项目根目录 `.env`，但真实系统环境变量优先。

## 持久化与备份

Docker 部署时，`./config` 会挂载到容器内的 `/app/config`：

```yaml
volumes:
  - ./config:/app/config
```

管理后台中的修改会写入本地 `config/`，容器重启后不会丢失。

如果要备份一个正在运行的实例，可以备份 `config/` 目录。但要注意：其中可能包含真实 API Key、订阅地址、节点信息和管理员密码。

如果要分发源码或打包这一版代码，建议使用 `git archive`，不要直接压缩整个项目目录。这样不会把 `.env`、真实 key、运行态配置、`.git/` 和 Python 缓存打进去。

示例：

```powershell
git archive --format=zip --output="..\vertex-proxy-source.zip" --prefix="vertex-proxy/" HEAD
```

## 常见问题

### 忘记管理员密码怎么办？

如果使用 `ADMIN_PASSWORD` 环境变量设置管理员密码，修改 `.env` 或系统环境变量后重启服务。

如果是首次启动自动生成的密码，可以查看或修改 `config/config.json` 里的 `admin_password` 字段，然后重启或重新登录。

### 修改端口为什么没生效？

Docker 部署时，`.env` 里的 `PORT` 控制宿主机访问端口，例如 `${PORT:-2156}:2156`。

应用内部监听端口来自 `config/config.json` 的 `port_api`。通常不需要修改容器内部端口。

非 Docker 运行时，`PORT` 不会覆盖 `port_api`。请在管理后台或 `config/config.json` 中修改 `port_api`。

### API 返回 401 怎么办？

先登录管理后台，在 API Keys 页面添加一个 key。请求时使用：

```text
Authorization: Bearer <你的 API Key>
```

### 为什么不把所有配置都放进 `.env`？

`.env` 适合部署层变量，例如 `PORT`、`PROXY_URL`、`ADMIN_PASSWORD`。

API Key 列表、模型列表、订阅节点、节点健康状态和管理后台动态设置是结构化数据或运行态数据，放在独立文件中更清楚，也更适合安全读写和备份。
