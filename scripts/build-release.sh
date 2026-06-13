#!/usr/bin/env bash
# 交叉编译三平台并打包为开箱即用的发布压缩包。
# 用法: bash scripts/build-release.sh [版本号]   例如: bash scripts/build-release.sh v1.0.2
#
# 产物在 dist/ 下：
#   vertex-proxy-windows-amd64.zip   (Windows x64)
#   vertex-proxy-linux-amd64.zip     (Linux x86_64)
#   vertex-proxy-android-arm64.zip   (Android/Termux 及 Linux ARM64)
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:-dev}"
OUT="dist"
LDFLAGS="-s -w -X main.version=${VERSION}"

rm -rf "$OUT"
mkdir -p "$OUT"

# 平台清单： GOOS GOARCH 二进制名 压缩包名 附带的启动脚本…
build() {
  local goos="$1" goarch="$2" bin="$3" pkg="$4"; shift 4
  local stage="$OUT/$pkg"
  echo "==> 编译 $goos/$goarch"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags="$LDFLAGS" -o "$stage/$bin" ./cmd/vproxy

  mkdir -p "$stage/config"
  cp config/config.example.json   "$stage/config/"
  cp config/api_keys.example.txt  "$stage/config/"
  cp config/models.json           "$stage/config/"
  cp 部署指南.md                   "$stage/"
  # 附带的启动脚本/服务文件（按平台传入）
  for f in "$@"; do cp "$f" "$stage/"; done

  (cd "$OUT" && zip -rq "$pkg.zip" "$pkg" && rm -rf "$pkg")
  echo "    -> $OUT/$pkg.zip"
}

build windows amd64 vertex-proxy.exe vertex-proxy-windows-amd64 scripts/启动.bat
build linux   amd64 vertex-proxy     vertex-proxy-linux-amd64   scripts/start.sh scripts/vertex-proxy.service
build linux   arm64 vertex-proxy     vertex-proxy-android-arm64 scripts/start.sh scripts/vertex-proxy.service

echo "完成。产物："
ls -1 "$OUT"
