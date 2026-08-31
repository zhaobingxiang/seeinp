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

### 2.1 项目根目录（仓库级，实际结构）

```
seeinp/
├── build/                      # 构建脚本与构建期工具
│   ├── build.sh               #   版本构建脚本：发布产物 → release/（自动构建前端嵌入、注入版本号）
│   ├── build-deployer.sh      #   seeinps-deployer Windows 交叉构建脚本（bash）
│   └── genhash/                #   bcrypt 密码哈希小工具（go run ./build/genhash <密码>）
├── cmd/                        # 主程序入口（Go）
│   ├── seeinpm/                #   中心端
│   ├── seeinps/                #   服务端
│   └── seeinps-deployer/       #   部署工具（Windows，内嵌 Web 前端 web-deployer）
├── internal/                   # 内部共享库
│   ├── auth/                   #   JWT/bcrypt/DPAPI
│   ├── config/                 #   TOML 配置加载
│   ├── gzhttp/                 #   gzip HTTP 中间件
│   ├── logx/                   #   分级+轮转+JSON 日志
│   ├── mux/                    #   yamux 多路复用封装
│   ├── portpool/               #   端口池管理
│   ├── protocol/               #   控制协议编解码、消息定义（02 文档）
│   ├── store/                  #   SQLite 封装（迁移/聚合）
│   ├── tlsutil/                #   TLS/指纹管理
│   ├── tslog/                  #   结构化日志
│   ├── version/                #   版本信息（ldflags 注入）
│   └── webui/                  #   内嵌前端（go:embed，构建产物）
├── web-pm/                     # seeinpm 管理端前端（Vue3 + Element Plus + Vite）
├── web-ps/                     # seeinps B 端前端（Vue3 + Element Plus + Vite）
├── conf/                       # 配置模板（seeinpm.toml / seeinps.toml）
├── docs/                       # 文档体系（本文档所在）
├── img/                        # 各端图标 PNG（seeinpm/seeinps/seeinpc/seeinps-tools）
├── ico/                        # 图标 ICO（需要时由 img 转换生成，当前为空）
├── release/                    # 发布成果物（构建输出，git 忽略）；二进制名 = 程序名，版本/架构由目录体现
│   ├── seeinpm/<版本>/linux-amd64/
│   │   ├── seeinpm             #   二进制
│   │   └── conf/seeinpm.toml   #   配置模板
│   ├── seeinps/<版本>/{linux-amd64,windows-amd64}/
│   │   ├── seeinps(.exe)       #   二进制（Windows 带 .exe）
│   │   └── conf/seeinps.toml   #   配置模板
│   ├── seeinpc/<版本>/         #   客户端（待开发）
│   └── seeinps-deployer/<版本>/
│       └── seeinps-deployer.exe #  部署工具（Windows）
├── test/                       # 测试
│   ├── e2e/                    #   e2e 测试脚本 + runtime（本地测试二进制与运行数据）
│   ├── integration/            #   Go 集成测试
│   ├── protocol/               #   协议单测
│   └── legacy-scripts/         #   历史开发/调试脚本归档
├── go.mod
├── go.sum
└── README.md
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
> seeinpm 版本管理上传的安装包落盘于 `data/releases/{endpoint}/{版本}/{系统-架构}/`，与仓库 `release/` 目录结构一致；B 端经 `/api/v1/ps-release/download` 接口下载，版本元数据存于 `data/seeinpm.db` 的 `versions` 表（见 04 文档 §3.7）。
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

## 3. 常用命令

```bash
# 构建发布产物（linux，自动构建前端嵌入、注入版本号，输出 release/<程序>/<版本>/<系统-架构>/）
bash build/build.sh 1.0.26.0830.14              # seeinpm + seeinps
bash build/build.sh 1.0.26.0830.14 seeinps      # 仅 seeinps
ARCH=arm64 bash build/build.sh 1.0.26.0830.14   # 指定 GOARCH（默认 amd64）
WIN=1 bash build/build.sh 1.0.26.0830.14        # 额外产出 Windows 测试二进制 → test/e2e/runtime/

# 构建 seeinps-deployer（Windows PowerShell，发布版本号沿用 seeinps 当前版本）
bash build/build-deployer.sh 1.0.26.0830.14

# 测试
go test ./...                                   # Go 单测/集成
python test/e2e/test_upgrade_e2e.py             # e2e（依赖 test/e2e/runtime 下的测试二进制）

# 前端开发
cd web-pm && npm run dev                        # seeinpm 管理端 Vite dev server
cd web-ps && npm run dev                        # seeinps B 端

# 维护
go run ./build/genhash <密码>                   # 生成 bcrypt 密码哈希（配置文件用）
```

交叉编译要点（手动构建时参照 build/build.sh 的输出布局）：

```bash
# seeinps linux-arm64
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build \
  -ldflags "-X github.com/seeinp/seeinp/internal/version.Version=<版本> -X github.com/seeinp/seeinp/internal/version.Banner=seeinp-version:<版本>" \
  -o release/seeinps/<版本>/linux-arm64/seeinps ./cmd/seeinps
```

> `CGO_ENABLED=0` 为默认（modernc.org/sqlite 纯 Go 实现）；版本号经 `-ldflags` 注入 `internal/version`，二进制内带有 `seeinp-version:<版本>` 标识（升级包自动识别版本依赖该标识，务必经 build/build.sh 构建发布产物）。

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
