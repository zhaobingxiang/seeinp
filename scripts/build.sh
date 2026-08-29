#!/usr/bin/env bash
# seeinp 版本构建脚本：注入五段数字版本号（如 1.0.26.0829.01：大版本.大版本.年.月日.当日序号）
# 用法:
#   scripts/build.sh <version>              构建全部（seeinpm/seeinps linux-amd64）
#   scripts/build.sh <version> seeinps      只构建 seeinps
#   scripts/build.sh <version> seeinpm      只构建 seeinpm
#   ARCH=arm64 scripts/build.sh <version>   指定 GOARCH（默认 amd64，产物名带架构后缀）
#   WIN=1 scripts/build.sh <version>        额外产出本地 Windows 测试二进制（test/e2e/runtime/*.exe）
set -e
cd "$(dirname "$0")/.."

VERSION=${1:?用法: scripts/build.sh <version> 如 1.0.26.0829.01}
echo "$VERSION" | grep -Eq '^[0-9]{1,4}(\.[0-9]{1,4}){4}$' || { echo "版本号须为五段数字: $VERSION"; exit 1; }
TARGETS=${2:-all}
GOARCH=${ARCH:-amd64}
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS="-X github.com/seeinp/seeinp/internal/version.Version=${VERSION} -X github.com/seeinp/seeinp/internal/version.Banner=seeinp-version:${VERSION} -X github.com/seeinp/seeinp/internal/version.Commit=${COMMIT} -X github.com/seeinp/seeinp/internal/version.BuildTime=${BUILD_TIME}"

build_frontend() {
  echo ">> 构建前端并嵌入二进制（web-pm / web-ps）"
  (cd web-pm && rm -rf dist && npx vite build --emptyOutDir=false >/dev/null)
  (cd web-ps && rm -rf dist && npx vite build --emptyOutDir=false >/dev/null)
  rm -rf internal/webui/pm/files internal/webui/ps/files
  mkdir -p internal/webui/pm/files internal/webui/ps/files
  cp -r web-pm/dist/. internal/webui/pm/files/
  cp -r web-ps/dist/. internal/webui/ps/files/
}

build() {
  local out="$1-${GOARCH}"
  [ "$GOARCH" = "amd64" ] && out="$1"
  echo ">> GOOS=linux GOARCH=$GOARCH go build -ldflags '$LDFLAGS' -o $out ./cmd/$2"
  GOOS=linux GOARCH=$GOARCH CGO_ENABLED=0 go build -ldflags "$LDFLAGS" -o "$out" "./cmd/$2"
}

build_frontend

case "$TARGETS" in
  all)     build seeinpm-linux-amd64 seeinpm; build seeinps-linux-amd64 seeinps ;;
  seeinpm) build seeinpm-linux-amd64 seeinpm ;;
  seeinps) build seeinps-linux-amd64 seeinps ;;
  *) echo "未知目标: $TARGETS"; exit 1 ;;
esac

if [ "$WIN" = "1" ]; then
  echo ">> 构建本地 Windows 测试二进制"
  GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$LDFLAGS" -o test/e2e/runtime/seeinpm.exe ./cmd/seeinpm
  GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$LDFLAGS" -o test/e2e/runtime/seeinps.exe ./cmd/seeinps
fi

echo ">> done version=$VERSION commit=$COMMIT"
