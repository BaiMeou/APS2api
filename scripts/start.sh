#!/bin/sh
# Vertex AI Proxy 启动脚本（Linux / Termux / ARM 设备）
# 缺配置时从示例初始化，然后启动程序。
set -e
cd "$(dirname "$0")"

mkdir -p config

if [ ! -f config/config.json ] && [ -f config/config.example.json ]; then
  cp config/config.example.json config/config.json
  echo "[init] 已生成 config/config.json"
fi

if [ ! -f config/api_keys.txt ]; then
  [ -f config/api_keys.example.txt ] && cp config/api_keys.example.txt config/api_keys.txt
  echo "[!] 已生成 config/api_keys.txt —— 请编辑它，填入你的 sk- 密钥后再用客户端访问。"
fi

chmod +x ./vertex-proxy 2>/dev/null || true
echo "[*] 启动中…  管理面板: http://127.0.0.1:2156/admin/"
exec ./vertex-proxy
