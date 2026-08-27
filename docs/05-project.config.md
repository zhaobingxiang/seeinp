# seeinp Project Config（项目技术栈与开发规范）

| 项 | 内容 |
|---|---|
| 文档版本 | v1.0 |
| 状态 | 评审稿 |
| 关联文档 | 01-需求文档.md、02-通信协议规范.md、03-API规范.md、04-数据存储规范.md |

---

## 1. 技术栈

### 1.1 后端（三端统一）

| 项 | 选型 | 说明 |
|---|---|---|
| 语言 | **Go**（≥1.22） | 三端 seeinpm/seeinps/seeinpc 统一；参考 frp 的架构与 mux 设计 |
| 网络 | `net/http` + 自研控制协议 | 控制通道：TCP + TLS + smux/yamux 多路复用（协议见 02 文档） |
| 数据库 | `modernc.org/sqlite`（纯 Go，零 CGO） | 便于交叉编译；见 04 文档 |
| 配置 | TOML（`github.com/BurntSushi/toml` 或 `pelletier/go-toml/v2`） | 热重载友好（SIGHUP + B端按钮） |
| 日志 | `log/slog` + 自研轮转 | JSON 单行格式；按大小/日期轮转（需求文档 §4.4） |
| JWT | `golang-jwt/jwt/v5` | access + refresh 双令牌 |
| 密码哈希 | `golang.org/x/crypto/bcrypt` | 管理密码/代理密码/授权码哈希 |
| 密码学 | `crypto/rand`（授权码）、DPAPI（Windows 本地加密，`syscall`/`golang.org/x/sys/windows`） | seeinpc 本地配置密码加密 |
| WebSocket | `github.com/gorilla/websocket` | 实时日志/状态流 |

### 1.2 前端（B端，seeinpm/seeinps）

| 项 | 选型 |
|---|---|
| 框架 | Vue 3 + TypeScript + Vite |
| 组件库 | Element Plus（或 Ant Design Vue） |
| 状态 | Pinia |
| 图表 | ECharts |
| 实时 | WebSocket（原生 + 封装） |
| 构建产物 | 三端共用一套组件/API SDK，静态文件内嵌进 Go 二进制（`go:embed`），单文件分发 |

### 1.3 客户端 seeinpc（Go 重构）

| 项 | 选型 | 说明 |
|---|---|---|
| GUI | Wails v2（Web 前端 + Go 后端）或 Fyne | 推荐 Wails：与 B端共用设计系统；打包单 exe |
| 虚拟网卡 | `golang.zx2c4.com/wintun`（官方绑定） | 替代原 wintun.dll 手动管理，随包内嵌驱动 |
| 网络栈 | sing-tun / gVisor netstack（`gvisor.dev/gvisor/pkg/tcpip`） | 替代 tun2socks.exe 外部进程，内置 SOCKS5 出口 + HTTP CONNECT 认证中继（等价原 ChainedProxy） |
| 配置加密 | Windows DPAPI（`CryptProtectData`） | 替代原 XOR 弱加密 |
| 提权 | manifest requireAdministrator + 自检 | 虚拟网卡需要管理员权限 |

### 1.4 部署工具（Windows）

- Go + Wails（与 seeinpc 共用外壳）；SSH 用 `golang.org/x/crypto/ssh`（密码/密钥）；
- 远程文件传输用 `github.com/pkg/sftp`；systemd unit 生成由模板完成；
- 架构探测：`uname -m` + Go `runtime.GOARCH` 映射（amd64/arm64）。

---

## 2. 目录结构

### 2.1 项目根目录（仓库级，用户指定层级）

```
seeinp/
├── cmd/                        # 三端主程序入口（Go）
│   ├── seeinpm/                #   中心端
│   ├── seeinps/                #   服务端
│   ├── seeinpc/                #   客户端（GUI）
│   └── seeinps-deployer/       #   部署工具（Windows）
├── internal/                   # 内部共享库
│   ├── protocol/               #   控制协议编解码、消息定义（02 文档）
│   ├── mux/                    #   多路复用封装
│   ├── tlsutil/                #   TLS/指纹管理
│   ├── logx/                   #   分级+轮转+JSON 日志
│   ├── store/                  #   SQLite 封装（迁移/聚合）
│   ├── auth/                   #   JWT/bcrypt/DPAPI
│   └── version/                #   版本信息（ldflags 注入）
├── web/                        # 前端源码（Vue3，三端共用组件与 SDK）
│   ├── src/
│   │   ├── components/         #   共用组件库（含 UpgradeReminder 升级提醒弹窗组件，seeinps 仪表板/seeinpc 启动时复用）
│   │   ├── views/              #   pm/、ps/ 各自页面（pm 端含「版本管理」页：上传/历史/按端 Tab；ps 端含升级提醒横幅）
│   │   └── api/                #   生成的 API SDK（封装 /api/v1/version/* 接口）
├── dist/                       # 部署成果物（构建输出，按程序分目录）
│   ├── seeinpm/                #   seeinpm-<version>-<os>-<arch>/
│   ├── seeinps/                #   各架构子目录
│   └── seeinpc/                #   客户端 exe
├── release/                    # 用户发布成果物（各架构 + 用户说明）
│   ├── seeinpm-linux-amd64/    #   含 用户说明.html / 用户说明.docx
│   ├── seeinps-linux-amd64/
│   ├── seeinps-linux-arm64/
│   ├── seeinps-windows-amd64/
│   ├── seeinpc-windows-amd64/
│   └── seeinps-deployer-windows-amd64/
├── driver/                     # 驱动（wintun.dll 等，随构建打包）
├── test/                       # 功能测试脚本（协议/端到端/冒烟）
│   ├── protocol/
│   ├── e2e/
│   └── smoke/
├── docs/                       # 文档体系（本文档所在）
├── scripts/                    # 构建/发布/维护脚本（CI 用）
├── go.mod
└── Makefile                    # 常用命令入口（见 §3）
```

### 2.2 程序安装目录（每个程序运行时层级，用户指定层级）

```
<安装目录>/seeinpm  (或 seeinps / seeinpc)
├── bin/                        # 主程序（可执行文件）
├── conf/                       # 配置文件（seeinpm.toml / seeinps.toml / config.json）
├── logs/                       # 日志（运行/访问/审计 分目录或文件名后缀）
├── resource/                   # 驱动与依赖（seeinpc: wintun 驱动；seeinps: 模板/静态资源）
├── doc/                        # 文档（本程序说明）
├── data/                       # 数据（seeinpm.db / seeinps.db / backup/）
│   └── releases/               #   seeinpm 专属：版本管理上传的安装包
│       ├── seeinpm/            #     seeinpm 端版本包
│       ├── seeinps/            #     seeinps 端版本包（linux-amd64/arm64、windows-amd64）
│       └── seeinpc/            #     seeinpc 端版本包（windows-amd64）
└── script/                     # 维护脚本（reset_password、backup、check 等）
```

> 首次运行自动生成上述目录结构与配置文件模板（需求文档 UX 要求）。
> seeinpm 版本管理上传的安装包落盘于 `data/releases/{endpoint}/`，通过 `/releases/{endpoint}/<file>` 路径对外提供下载（静态文件服务）；版本元数据存于 `data/seeinpm.db` 的 `versions` 表（见 04 文档 §3.7）。
> seeinpc 本地配置文件 `conf/config.json` 在原有多 VPN profile 基础上，新增 `ignoredVersions` 字段（字符串数组）用于持久化「该版本不再提醒」状态（见 04 文档 §8）。

### 2.3 升级程序与备份机制（seeinps / seeinpc）

seeinps / seeinpc 各自安装目录在 §2.2 通用结构基础上，新增升级相关文件（升级流程见需求文档 §4.5.1）：

**seeinps 安装目录新增**：

| 文件 / 目录 | 用途 |
|---|---|
| `seeinps-updater`（Linux）/ `seeinps-updater.exe`（Windows） | 升级程序二进制；主程序检测到新版本并经用户确认后启动，负责备份当前版本、替换二进制与资源、校验完整性、失败回滚 |
| `backup/` | 升级前备份目录；每次升级前完整备份当前版本到此目录下子目录 `<version>-<ts>/`（如 `backup/1.2.0-20260805/`） |

**seeinpc 安装目录新增**：

| 文件 / 目录 | 用途 |
|---|---|
| `seeinpc-updater.exe` | 升级程序二进制；职责同 seeinps-updater |
| `backup/` | 升级前备份目录；结构同 seeinps |
| `seeinpc.exe.manifest` | 程序清单（XML），声明 `requireAdministrator`，使程序启动即由系统触发 UAC（虚拟网卡需要管理员权限，见需求文档 §9.3「权限」行双重保障①） |

**updater 二进制命名规则**：
- seeinps：`seeinps-updater`（Linux）/ `seeinps-updater.exe`（Windows）
- seeinpc：`seeinpc-updater.exe`（仅 Windows）
- 与主程序同前缀，便于识别归属；构建产物随主程序一并输出到 `dist/<程序>/` 与 `release/<程序>-<os>-<arch>/`。

**备份策略**：
- 每次升级前完整备份当前版本（二进制 + `resource/` + `conf/`）到 `backup/<endpoint>-<oldversion>-<ts>/`，如 `backup/seeinps-1.2.0-20260805-143000/`；
- 保留最近 3 个备份，超出自动清理最旧；
- 备份目录与主程序同盘，避免跨盘替换失败。

**回滚机制**：
- updater 任一步失败（下载中断 / SHA256 校验失败 / 签名校验失败 / 替换文件失败 / 完整性校验失败）自动恢复最近一次备份；
- 回滚后启动主程序，主程序照常启动并在 B端 / 弹窗提示「升级失败已回滚」；
- 回滚操作本身失败（极端情况）：保留备份目录，写入 ERROR 日志，由运维手动恢复。

**updater 与主程序通信**：
- 通过退出码传递结果：`0` = 成功；非 `0` = 失败原因（见下表，主程序据此展示对应提示）；
- 日志文件：`logs/updater_YYYYMMDD.log`，记录每一步操作与失败详情，便于排查；
- 主程序启动 updater 后立即退出（释放文件锁），updater 完成替换后启动主程序并退出，主程序读取 updater 退出码决定提示「升级成功」或「升级失败已回滚」。

| 退出码 | 含义 |
|---|---|
| 0 | 升级成功 |
| 1 | 下载失败（网络中断 / 写入失败） |
| 2 | SHA256 校验失败 |
| 3 | 代码签名校验失败 |
| 4 | 备份失败（磁盘空间不足 / 权限不足） |
| 5 | 替换文件失败（文件占用 / 权限不足） |
| 6 | 完整性校验失败（替换后文件不可执行 / 版本号错误） |
| 7 | 回滚失败（备份恢复失败，需人工介入） |

---

## 3. 常用命令（Makefile）

```makefile
# 基础构建
make build                # 构建三端当前平台二进制 → dist/<name>/
make build-pm             # 仅 seeinpm
make build-ps             # 仅 seeinps
make build-pc             # 仅 seeinpc（GUI）
make build-deployer       # 仅部署工具（Windows）

# 交叉编译（release 全量产物）
make release              # 产出各架构 → release/（含用户说明生成）
make release-linux        # linux amd64 + arm64（seeinpm/seeinps）
make release-windows      # windows amd64（seeinps/seeinpc/deployer）

# 运行（开发）
make run-pm               # 本地起 seeinpm（默认 conf 模板）
make run-ps               # 本地起 seeinps
make run-pc               # 起 seeinpc

# 测试
make test                 # go test ./...
make test-protocol        # 协议单测/集成（test/protocol）
make test-e2e             # 端到端（test/e2e，docker-compose 一键环境）
make test-smoke           # 冒烟脚本

# 前端
make web-dev              # Vite dev server
make web-build            # 前端构建 → go:embed 打包进二进制

# 维护
make lint                 # golangci-lint
make fmt                  # gofmt
make docs                 # 校验文档交叉引用（脚本）
```

交叉编译要点（示例）：

```bash
# seeinps linux-arm64
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o dist/seeinps/seeinps-linux-arm64 ./cmd/seeinps
# seeinpm linux-amd64
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o dist/seeinpm/seeinpm-linux-amd64 ./cmd/seeinpm
# seeinpc windows-amd64
GOOS=windows GOARCH=amd64 go build -o dist/seeinpc/seeinpc.exe ./cmd/seeinpc
```

> `CGO_ENABLED=0` 为默认（现代c.org/sqlite 纯 Go 实现支持）；版本号经 `-ldflags "-X internal/version.Version=$(VERSION)"` 注入。

---

## 4. 开发规范

### 4.1 Go 代码规范

| 项 | 规则 |
|---|---|
| 版本 | Go ≥1.22；go.mod 单一 module（`github.com/seeinp/seeinp`） |
| 格式 | `gofmt` 强制；`golangci-lint`（goimports、errcheck、ineffassign、govet、staticcheck）CI 必过 |
| 错误处理 | 显式 err 传播，禁止吞错；边界错误包装上下文（`fmt.Errorf("...: %w")`） |
| 并发 | 优先 goroutine + channel；共享状态用 `sync.Mutex`/`atomic`；禁止无界 goroutine（用 worker pool/semaphore） |
| 上下文 | 所有 IO 操作带 `context.Context`（含超时），保证优雅关闭（需求文档 §11） |
| 敏感信息 | 日志/错误信息不输出密码、授权码、Token；配置文件敏感项支持环境变量注入 |
| 协议 | 消息结构定义统一在 `internal/protocol`，禁止各端复制粘贴（避免协议漂移） |

### 4.2 提交规范（Conventional Commits）

```
feat:     新功能
fix:      缺陷修复
docs:     文档
refactor: 重构（不改变行为）
test:     测试
chore:    构建/工具/依赖
perf:     性能
security: 安全修复

示例：feat(protocol): add ALLOC_PORT message for ops proxy
```

- 分支模型：`main`（可发布）→ `feature/*`（功能分支）→ PR 合并；`release/<version>` 打标签；
- PR 必须：通过 lint + test、描述变更点、关联文档更新（改协议必须同步 02 文档）。

### 4.3 协议变更流程（防止文档与实现漂移）

1. 改 `internal/protocol` 消息定义；
2. 同步更新 `docs/02-通信协议规范.md`（消息/错误码/时序）；
3. 若影响 API，同步 `docs/03-API规范.md`；
4. 兼容性检查：新消息向后兼容、未知类型返回 `2001`（协议规范 §11）。

### 4.4 版本规范（SemVer）

- `x.y.z`：x=API/协议破坏性变更，y=功能，z=修复；
- 协议版本（HELLO 中）独立于程序版本，仅协议不兼容时递增主版本；
- 发布产物命名：`<程序>-<version>-<os>-<arch>[.exe]`（如 `seeinps-1.2.0-linux-arm64`）。

---

## 5. 文件位置与层级说明（对照速查）

| 内容 | 位置 |
|---|---|
| 三端可执行文件 | `dist/<程序>/`（构建产物）、程序安装目录 `bin/` |
| 配置文件 | 程序安装目录 `conf/`（见 04 文档：`data/` 为数据库） |
| 日志 | 程序安装目录 `logs/`（运行/访问/审计） |
| 驱动 | 仓库 `driver/` → 构建进 `resource/`（seeinpc 运行时解压目录） |
| 文档 | 仓库 `docs/`（本文档体系）；程序安装目录 `doc/`（随包说明） |
| 数据 | 程序安装目录 `data/`（SQLite + backup/） |
| 维护脚本 | 程序安装目录 `script/`；仓库构建脚本 `scripts/` |
| 发布成果物 | `release/<程序>-<os>-<arch>/`，含 `用户说明.html` 与 `用户说明.docx`（同内容双格式） |
| 测试 | `test/`（protocol / e2e / smoke） |
