#!/usr/bin/env bash
# seeinps-deployer 构建脚本：构建前端并产出单文件 Windows 可执行程序（交叉编译）。
# 发布时版本号沿用 seeinps 当前的五位版本号（见 release/seeinps/ 下最新版本目录）。
# 用法:
#   bash build/build-deployer.sh                     默认开发版本 1.0.0.0001.01
#   bash build/build-deployer.sh 1.0.26.0830.14      指定五段数字版本号（发布用，与 seeinps 版本一致）
# 产物输出到 release/seeinps-deployer/<版本>/seeinps-deployer.exe
set -e
cd "$(dirname "$0")/.."
source "$(dirname "$0")/_icon.sh"

VERSION=${1:-1.0.0.0001.01}
echo "$VERSION" | grep -Eq '^[0-9]{1,4}(\.[0-9]{1,4}){4}$' || { echo "版本号须为五段数字，如 1.0.26.0830.14"; exit 1; }

FRONT=cmd/seeinps-deployer/web-deployer
OUT_DIR=release/seeinps-deployer/$VERSION
OUT=$OUT_DIR/seeinps-deployer.exe
mkdir -p "$OUT_DIR"

echo ">> 构建 seeinps-deployer 前端（web-deployer -> web/build）"
( cd "$FRONT" && { [ -d node_modules ] || npm install; } && npm run build )

echo ">> 嵌入 Windows 图标资源（go-winres）"
gen_winsyso seeinps-deployer "$VERSION" cmd/seeinps-deployer/winres_windows_amd64.syso

echo ">> 构建 Windows amd64 二进制（version=$VERSION）"
# -H windowsgui：GUI 子系统启动，不弹出控制台窗口（提权、开浏览器由程序自动完成）
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-H windowsgui -X main.appVersion=$VERSION" -o "$OUT" ./cmd/seeinps-deployer

# 注意：.syso 残留于 cmd/seeinps-deployer/（已被 .gitignore 忽略）
# 随发布包附带图标 PNG（部署工具产品名 seeinps-tools）
cp "img/seeinps-tools.png" "$OUT_DIR/seeinps-tools.png"

SIZE=$(ls -lh "$OUT" | awk '{print $5}')
echo "<< 完成: $OUT ($SIZE, version=$VERSION)"
