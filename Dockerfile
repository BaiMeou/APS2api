# ================= Stage 1: 编译阶段 =================
# 使用与项目 go.mod 匹配的 Go 版本镜像
FROM golang:1.26-alpine AS builder

# 安装基本构建依赖
RUN apk add --no-cache git gcc musl-dev

WORKDIR /build

# 复制依赖配置并预先下载依赖
COPY go.mod ./
RUN go mod download

# 复制整库代码
COPY . .

# 编译静态二进制文件，去除符号表以压缩体积
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o vproxy ./cmd/vproxy

# ================= Stage 2: 运行阶段 =================
FROM alpine:3.20

# 安装 CA 证书、时区数据以及运行 entrypoint.sh 必需的 Bash 
RUN apk add --no-cache ca-certificates tzdata bash

WORKDIR /app

# 从 builder 阶段复制可执行文件
COPY --from=builder /build/vproxy /app/vproxy

# 将示例配置文件复制到运行环境的备用区
COPY config/config.example.json /app/config.example.json
COPY config/api_keys.example.txt /app/api_keys.example.txt
COPY config/models.json /app/models.json

# 复制并配置入口脚本
COPY entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh

# 通过环境变量固定配置文件路径
ENV VPROXY_CONFIG=/app/config/config.json
ENV VPROXY_API_KEYS=/app/config/api_keys.txt
ENV VPROXY_MODELS=/app/config/models.json

# 声明默认服务端口
EXPOSE 2156

ENTRYPOINT ["/app/entrypoint.sh"]