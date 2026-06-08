#!/bin/sh
set -e

# 创建必要目录
mkdir -p /app/config /app/logs /app/errors /app/bin

# 尝试设置权限（在 proot 中可能失败，但不影响运行）
chown -R vproxy:vproxy /app/config /app/logs /app/errors /app/bin 2>/dev/null || true

# 首次挂载空卷时，把镜像内置的默认配置复制到持久化目录
if [ ! -f /app/config/config.json ]; then
  if [ -f /app/config.default/config.example.json ]; then
    cp /app/config.default/config.example.json /app/config/config.json
  elif [ -f /app/config.default/config.json ]; then
    cp /app/config.default/config.json /app/config/config.json
  fi
fi

if [ ! -f /app/config/models.json ] && [ -f /app/config.default/models.json ]; then
  cp /app/config.default/models.json /app/config/models.json
fi

if [ ! -f /app/config/api_keys.txt ]; then
  if [ -f /app/config.default/api_keys.example.txt ]; then
    cp /app/config.default/api_keys.example.txt /app/config/api_keys.txt
  elif [ -f /app/config.default/api_keys.txt ]; then
    cp /app/config.default/api_keys.txt /app/config/api_keys.txt
  fi
fi

chown -R vproxy:vproxy /app/config 2>/dev/null || true

# 切换到非 root 用户运行（如果 gosu 可用）
if command -v gosu >/dev/null 2>&1 && [ "$(id -u)" = "0" ]; then
  exec gosu vproxy "$@"
else
  # 没有 gosu 或不是 root，直接执行
  exec "$@"
fi