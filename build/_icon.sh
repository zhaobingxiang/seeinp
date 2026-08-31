#!/usr/bin/env bash
# 共享：Windows 程序图标嵌入（生成 PE 资源 .syso，go build 时自动链接）
# 被 build/build.sh 与 build/build-deployer.sh source 使用。
# 图标源：ico/<程序>.ico（由 img/<程序>.png 转换，见 build/convert-icons.py）
# 依赖：go-winres（缺失时自动安装到 $GOPATH/bin）

ICON_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/ico"
PNG_ICONS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/build/icons"

ensure_go_winres() {
  if ! command -v go-winres >/dev/null 2>&1; then
    echo ">> 未找到 go-winres，正在安装..."
    GOOS=windows GOARCH=amd64 go install github.com/tc-hib/go-winres@latest
  fi
}

# name_to_ico <程序名> -> 输出 ico 基名（不带扩展名）；部署工具产品名为 seeinps-tools
name_to_ico() {
  case "$1" in
    seeinpm) echo seeinpm ;;
    seeinps) echo seeinps ;;
    seeinps-deployer) echo seeinps-tools ;;
    seeinpc) echo seeinpc ;;
    *) echo "$1" ;;
  esac
}

# product_name <程序名> -> 输出 Windows 版本资源里的产品名
product_name() {
  case "$1" in
    seeinps-deployer) echo "seeinps 部署工具" ;;
    *) echo "seeinp $1" ;;
  esac
}

# gen_winsyso <程序名> <版本号> <输出 .syso 绝对/相对路径>
# 生成对应图标的 PE 资源 .syso；五段版本自动截取前四段写入 Windows 版本资源
gen_winsyso() {
  local name="$1" version="$2" out="$3"
  local ico
  ico=$(name_to_ico "$name")
  ensure_go_winres
  local tmp tmp_win v4 desc json_files=""
  tmp=$(mktemp -d)
  tmp_win=$(cygpath -w "$tmp")
  v4=$(echo "$version" | awk -F. '{print $1"."$2"."$3"."$4}')
  desc=$(product_name "$name")
  # 多尺寸 PNG：按 go-winres 命名约定 <base><size>.png；源在 build/icons/
  local sizes=(16 32 64 128 256)
  local json_files=""
  for s in "${sizes[@]}"; do
    local f="${ico}${s}.png"
    if [ -f "$PNG_ICONS_DIR/$f" ]; then
      cp "$PNG_ICONS_DIR/$f" "$tmp/$f"
      json_files="${json_files}\"$f\", "
    fi
  done
  json_files="${json_files%, }"
  cat > "$tmp/winres.json" <<EOF
{
  "RT_GROUP_ICON": {
    "APP": { "0000": [${json_files}] }
  },
  "RT_VERSION": {
    "#1": {
      "0000": {
        "fixed": {
          "file_version": "$v4",
          "product_version": "$v4"
        },
        "info": {
          "0409": {
            "FileDescription": "$desc",
            "ProductName": "$desc",
            "OriginalFilename": "$name.exe",
            "FileVersion": "$v4",
            "ProductVersion": "$v4"
          }
        }
      }
    }
  }
}
EOF
  echo ">> go-winres: $name <- build/icons/${ico}*.png -> $out"
  GOOS=windows GOARCH=amd64 go-winres make --no-suffix --in "$tmp_win/winres.json" --out "$out"
  # 注意：不清理 $tmp 临时目录（执行环境 safe-delete 拦截删除，残留系统临时目录无碍）
}
