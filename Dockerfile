# ─────────────────────────────────────────────────────────────────────────────
#  Vertex AI Proxy — Dockerfile
#  Build:  docker build -t vertex-proxy .
#  Run:    docker compose up -d
# ─────────────────────────────────────────────────────────────────────────────

# ── Stage 1: dependency builder ───────────────────────────────────────────────
FROM python:3.12-slim AS builder

WORKDIR /build

COPY requirements.txt .
RUN pip install --no-cache-dir --prefix=/install -r requirements.txt


# ── Stage 2: runtime image ────────────────────────────────────────────────────
FROM python:3.12-slim AS runtime

LABEL org.opencontainers.image.title="Vertex AI Proxy"
LABEL org.opencontainers.image.description="Anonymous Vertex AI proxy with OpenAI-compatible API"

# 运行时系统依赖
RUN apt-get update \
    -o Acquire::Retries=5 \
    && apt-get install -y --no-install-recommends \
         ca-certificates curl gosu \
    && rm -rf /var/lib/apt/lists/*

# 创建非 root 用户
RUN groupadd -r vproxy && useradd -r -g vproxy -d /app -s /sbin/nologin vproxy

WORKDIR /app

# 从 builder 复制已安装的 Python 包
COPY --from=builder /install /usr/local

# 复制应用代码
COPY --chown=vproxy:vproxy . .
RUN mkdir -p /app/config.default \
    && cp -a /app/config/. /app/config.default/ 2>/dev/null || true
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

# 运行时可写目录
RUN mkdir -p /app/bin /app/logs /app/errors /app/config \
    && chown -R vproxy:vproxy /app/bin /app/logs /app/errors /app/config \
    && chmod +x /usr/local/bin/docker-entrypoint.sh

VOLUME ["/app/config"]

EXPOSE 2156

HEALTHCHECK --interval=30s --timeout=10s --start-period=15s --retries=3 \
    CMD curl -sf http://localhost:2156/health || exit 1

ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["python", "main.py"]
