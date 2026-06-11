#!/bin/sh
set -e

# 创建必要目录
mkdir -p /app/config /app/logs /app/errors /app/bin

# 首次挂载空卷时，从 default-config 复制非敏感默认配置
if [ ! -f /app/config/config.json ]; then
  if [ -f /app/default-config/config.example.json ]; then
    cp /app/default-config/config.example.json /app/config/config.json
    echo "[entrypoint] 已从 default-config/config.example.json 生成 config.json"
  fi
fi

if [ ! -f /app/config/models.json ] && [ -f /app/default-config/models.json ]; then
  cp /app/default-config/models.json /app/config/models.json
  echo "[entrypoint] 已从 default-config/models.json 生成 models.json"
fi

if [ ! -f /app/config/api_keys.txt ]; then
  if [ -f /app/default-config/api_keys.example.txt ]; then
    cp /app/default-config/api_keys.example.txt /app/config/api_keys.txt
    echo "[entrypoint] 已从 default-config/api_keys.example.txt 生成 api_keys.txt"
  fi
fi

# 权限设置：仅在 root 下执行 chown，非 root 跳过
if [ "$(id -u)" = "0" ]; then
  # proot-distro 下 chown 可能失败，静默容错
  chown -R vproxy:vproxy /app/config /app/logs /app/errors /app/bin 2>/dev/null || \
    echo "[entrypoint] 警告: chown 失败（proot 环境下可忽略）" >&2

  if command -v gosu >/dev/null 2>&1; then
    exec gosu vproxy "$@"
  fi
fi

# 非 root 或没有 gosu，直接执行
exec "$@"