# seeinp（时遇）

轻量级内网穿透与反向代理平台。通过 TLS 长连接 + yamux 多路复用，让内网服务无需公网 IP 即可被安全访问，并提供端口池管理、用户分组、版本分发与在线升级能力。

## 核心特性

- **三层架构**：`seeinpm`（中央管理平台）· `seeinps`（目标内网服务端代理）· `seeinpc`（Windows 客户端 / VPN）
- **主动拨出**：`seeinps` 部署在目标内网，主动向 `seeinpm` 建立 TLS 长连接，无需目标机公网 IP 或端口映射
- **多路复用**：控制信令与代理数据共线传输（yamux），一条连接承载全部流量，协议为「类型 + 长度前缀 + JSON」
- **代理类型**：TCP 转发、OPS HTTP 代理（Basic Auth 认证 + ACL 目标网段白名单）、UDP 映射（yamux 通道分帧传输 UDP 数据报）
- **端口池**：动态分配对外映射端口，支持按用户限定端口范围与配额，离线端口保留、代理删除后回收
- **用户管理**：用户分组、有效期、启用/禁用、授权码轮换，JWT 鉴权 + bcrypt 口令存储
- **登录防护**：图形验证码（SVG）+ 连续失败锁定，抵御口令暴力破解
- **版本分发**：版本包管理（上传/删除/下发），`seeinps` / `seeinpc` 支持在线自动升级，SHA256 校验
- **一键部署**：`seeinps-deployer` Windows 部署工具，支持远程 Linux 目标机直连下载安装，带进度条与 sha256 校验
- **单二进制分发**：Vue 3 + Element Plus 前端经 `go:embed` 嵌入，无需单独部署前端

## 架构

```
                 TLS 长连接（信令 + 数据，yamux 多路复用）
  ┌──────────┐   ┌─────────────────────────────────────┐   ┌──────────────┐
  │  seeinpc │──▶│              seeinpm                │◀──│    seeinps   │
  │ Windows  │   │  中央管理平台（Web + API + 端口池）   │   │ 目标内网服务端 │
  │ 客户端VPN │   └─────────────────────────────────────┘   └──────────────┘
  └──────────┘                                             （部署于内网，主动拨出）
```

- **seeinpm**：中央管理平台，提供 B 端管理界面与 API，管理用户/代理/端口池/版本，并对外提供映射端口监听
- **seeinps**：部署在需要暴露服务的网络内，向 `seeinpm` 主动拨出并注册代理，将公网连接转发到本机服务
- **seeinpc**：Windows 客户端，提供「运维 HTTP 代理 → 虚拟网卡 VPN」两种工作模式（WireGuard 隧道），并支持在线升级

### 端口约定

| 程序 | 端口 | 用途 |
|------|------|------|
| seeinpm | 90 | Web + API（B 端管理界面，同源） |
| seeinpm | 99 | 信令 / 控制通道（TCP + TLS） |
| seeinpm | 20000-30000 | 端口池（对外映射端口，TCP + UDP） |
| seeinps | 65443 | B 端 Web + API（同源） |

## 目录结构

```
├── cmd/                    # Go 主程序入口
│   ├── seeinpm/            #   中央管理平台
│   ├── seeinps/            #   目标内网服务端代理
│   └── seeinps-deployer/   #   Windows 一键部署工具
├── seeinpc/                # Windows 客户端（Wails + WireGuard）
├── internal/               # 内部共享库（auth/config/guard/logx/mux/portpool/protocol/store/...）
├── internal/webui/         # 前端构建产物（go:embed 嵌入）
├── web-pm/                 # seeinpm 前端（Vue 3 + Element Plus）
├── web-ps/                 # seeinps 前端（Vue 3 + Element Plus）
├── build/                  # 构建脚本（build.sh / build-deployer.sh / genhash）
├── conf/                   # 配置模板（随发布产物分发）
├── docs/                   # 项目文档
├── test/                   # e2e / integration / protocol 测试
├── release/                # 发布产物（构建脚本输出，git 忽略）
├── img/                    # 各端图标
└── go.mod                  # module github.com/seeinp/seeinp
```

## 技术栈

| 层 | 技术 |
|----|------|
| 后端 | Go 1.26、yamux（多路复用）、x/crypto（TLS/SSH）、SQLite（modernc.org/sqlite，含自动列迁移） |
| 前端 | Vue 3 + Element Plus + Vite，`go:embed` 嵌入单二进制 |
| 客户端 | Wails（seeinpc UI）、WireGuard + wintun（虚拟网卡）、Inno Setup（安装包） |
| 部署 | systemd 服务管理、SSH/SFTP（远程部署） |

## 快速开始

### 构建

```bash
# 构建全部发布产物（seeinpm linux-amd64 + seeinps 三平台），自动构建并嵌入前端
bash build/build.sh <版本号>

# 仅构建指定程序
bash build/build.sh <版本号> seeinps
bash build/build.sh <版本号> seeinpm

# seeinps-deployer（Windows）单独构建
bash build/build-deployer.sh <版本号>
```

版本号为五段数字（如 `1.0.26.0901.01`），经 ldflags 注入二进制（`seeinp-version:<版本>` 标识），是版本管理与在线升级链路的识别依据。

产物输出到 `release/<程序>/<版本>/<系统-架构>/`，二进制文件名即程序名，并附 `conf/<程序>.toml` 配置模板。

### 配置

各程序使用 TOML 配置文件（模板见 `conf/`）：

```toml
# seeinps 配置要点
[server]
server_addr = "seeinpm 主机:99"      # 指向 seeinpm 信令端口
skip_verify = true                   # 自签证书场景

[local]
bend_addr = ":65443"                 # B 端管理页监听地址
```

### 部署

- **seeinpm**：部署于中心服务器，提供管理界面与映射端口
- **seeinps**：部署于目标内网，通过 `seeinps-deployer` 一键部署（支持远程 Linux 目标机直连下载），或手动放置二进制 + 配置后以 systemd 运行
- **seeinpc**：Windows 客户端，安装后通过托盘管理，支持在线升级

## 开发

```bash
go build ./...     # 编译检查（只进缓存，不落盘）
go test ./...      # 单元测试
```

- 提交规范：Conventional Commits
- 行为变更需同步更新 `docs/` 下对应文档（需求/协议/API/存储）
- 前端改动后经 `build/build.sh` 构建嵌入二进制，无需单独部署前端

## 许可

保留所有权利。内部项目，未经授权不得分发。
