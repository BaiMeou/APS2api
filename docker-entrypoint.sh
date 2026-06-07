#!/bin/sh
set -e

mkdir -p /app/config /app/logs /app/errors /app/bin
chown -R vproxy:vproxy /app/config /app/logs /app/errors /app/bin 2>/dev/null || true

# 首次挂载空卷时，把镜像内置的默认配置复制到持久化目录。
# 后续升级时不覆盖用户已有文件；新版本新增的配置项由应用启动时自动合并。
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

if [ "$(id -u)" = "0" ]; then
  exec gosu vproxy "$@"
fi

exec "$@"
