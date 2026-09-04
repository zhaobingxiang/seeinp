# seeinpc 构建脚本（需在仓库根目录 seeinp/ 下执行，或由本脚本自动切换）
# 用法：.\seeinpc\build.ps1 [版本号]
# 说明：版本号遵循五段规范 大.小.年.月日.当日序号，如 1.0.26.0904.1；
#       不传参时自动生成（基于当前日期）。禁止裸 go build 作为发布产物。

param(
    [string]$Version = ""
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

if (-not $Version) {
    $now = Get-Date
    $Version = "1.0." + $now.ToString("yy.MMdd") + ".1"
}
$commit = (git rev-parse --short HEAD 2>$null)
if (-not $commit) { $commit = "unknown" }
$buildTime = (Get-Date).ToString("yyyy-MM-dd HH:mm:ss")

Write-Host "==> building seeinpc $Version (commit=$commit)"

# 1. 生成 Windows 资源（管理员清单 + 图标 + 版本信息）
Push-Location seeinpc
& go-winres make --in winres.json
Pop-Location

# 2. 编译（windowsgui 子系统，无控制台窗口）
#    注意：Wails 强制要求 -tags production（裸 go build 会弹窗退出）
$ldflags = "-H windowsgui -s -w" +
    " -X github.com/seeinp/seeinp/internal/version.Version=$Version" +
    " -X github.com/seeinp/seeinp/internal/version.Commit=$commit" +
    " -X 'github.com/seeinp/seeinp/internal/version.BuildTime=$buildTime'" +
    " -X github.com/seeinp/seeinp/internal/version.Banner=seeinp-version:$Version"

$env:CGO_ENABLED = "0"
go build -trimpath -tags production -ldflags $ldflags -o release\seeinpc\seeinpc.exe .\seeinpc
if ($LASTEXITCODE -ne 0) { throw "go build failed" }

# 3. 随包资源：驱动（安装器会释放到 resource/ 并做哈希校验）
New-Item -ItemType Directory -Force -Path release\seeinpc | Out-Null
Copy-Item seeinpc\resource\wintun.dll release\seeinpc\wintun.dll -Force

Write-Host "==> done: release\seeinpc\seeinpc.exe"
