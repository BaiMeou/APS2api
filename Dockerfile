# ─────────────────────────────────────────────────────────────────────────────
#  Vertex AI Proxy — Dockerfile (proot-distro 兼容版)
# ─────────────────────────────────────────────────────────────────────────────

# ── Stage 1: dependency builder ───────────────────────────────────────────────
FROM python:3.12-slim AS builder

# 使用绝对路径，避免 WORKDIR 的 bug
RUN mkdir -p /tmp/install /tmp/build
COPY requirements.txt /tmp/build/
RUN pip install --no-cache-dir --target=/tmp/install -r /tmp/build/requirements.txt

# ── Stage 2: runtime image ────────────────────────────────────────────────────
FROM python:3.12-slim AS runtime

LABEL org.opencontainers.image.title="Vertex AI Proxy"
LABEL org.opencontainers.image.description="Anonymous Vertex AI proxy with OpenAI-compatible API"

# 运行时系统依赖 - 添加 gosu
RUN apt-get update \
    -o Acquire::Retries=5 \
    && apt-get install -y --no-install-recommends \
         ca-certificates curl gosu \
    && rm -rf /var/lib/apt/lists/*

# 创建非 root 用户
RUN groupadd -r vproxy && useradd -r -g vproxy -d /app -s /sbin/nologin vproxy

# 创建应用目录
RUN mkdir -p /app /app/bin /app/logs /app/errors /app/config

WORKDIR /app

# 从 builder 复制已安装的 Python 包（修正路径）
COPY --from=builder /tmp/install /usr/local/lib/python3.12/site-packages/

# 复制应用代码
COPY . /app/
COPY docker-entrypoint.sh /usr/local/bin/

# 设置权限和可执行文件
# 备份非敏感默认配置（用于空卷首次启动初始化）
# 注意：不备份 config.json（含用户真实凭据），只备份示例文件
RUN mkdir -p /app/default-config \
    && cp /app/config/config.example.json /app/config/api_keys.example.txt /app/config/models.json /app/default-config/ 2>/dev/null || true

RUN chown -R vproxy:vproxy /app \
    && chmod +x /usr/local/bin/docker-entrypoint.sh

# 添加 Python 路径（确保能找到安装的包）
ENV PYTHONPATH=/usr/local/lib/python3.12/site-packages:${PYTHONPATH}

# 设置环境变量
ENV PYTHONUNBUFFERED=1

VOLUME ["/app/config"]
EXPOSE 2156

# 简化 healthcheck（proot-distro 会忽略但保留）
HEALTHCHECK --interval=30s --timeout=10s --start-period=15s --retries=3 \
    CMD curl -sf http://localhost:2156/health || exit 1

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["python", "-u", "main.py"]