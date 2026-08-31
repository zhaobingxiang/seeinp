# seeinp API 规范

| 项 | 内容 |
|---|---|
| 文档版本 | v1.0 |
| 状态 | 评审稿 |
| 关联文档 | 01-需求文档.md、02-通信协议规范.md、04-数据存储规范.md |

---

## 1. 通用规范

### 1.1 端口与同源

- **seeinpm**：B端 Web 页面、全部 REST API、WebSocket 统一监听 **999 端口**（同源，无跨域）；API 路径前缀 `/api/v1`；
- **seeinps**：B端与 API 同源监听 **65443**，路径前缀 `/api/v1`；
- seeinps 部署工具调用的校验接口：seeinpm `/api/v1/auth/verify-code`（HTTPS 建议，见 §2.2）。

### 1.2 统一响应壳

所有 REST 响应（含错误）：

```json
{ "code": 0, "message": "ok", "data": { } }
```

| 字段 | 说明 |
|---|---|
| code | 0 成功；非 0 见错误码表（§1.5） |
| message | 人类可读信息（成功为 "ok"） |
| data | 业务数据，成功时存在 |

分页响应壳：

```json
{ "code": 0, "message": "ok",
  "data": { "list": [], "page": 1, "pageSize": 20, "total": 128 } }
```

### 1.3 鉴权

| 项 | 说明 |
|---|---|
| Token 类型 | JWT：`access_token`（默认 30min）+ `refresh_token`（默认 24h） |
| 传递 | `Authorization: Bearer <access_token>` |
| 刷新 | `POST /api/v1/auth/refresh`，refresh_token 在 body；刷新后旧 refresh 失效（旋转） |
| 吊销 | 修改密码/重置授权码 → 服务端吊销该用户全部会话 |
| 脚本调用 | 支持创建 **API Token**（长期，可限定只读/范围，可吊销），用于 CI/自动化 |
| 对象级鉴权 | seeinps 用户仅能访问自己的代理/统计/日志（IDOR 防护，服务端逐请求校验归属） |

### 1.4 列表接口约定

- 分页：`?page=1&pageSize=20`（pageSize ≤ 100）；
- 筛选：`?status=online&type=tcp&group=groupA&keyword=xx`；
- 排序：`?sort=created_at&order=desc`（白名单字段，防止注入）；
- 时间区间：`?start=2026-08-01T00:00:00Z&end=2026-08-05T00:00:00Z`（ISO8601 UTC）。

### 1.5 错误码分段

| 段 | 含义 | 示例 |
|---|---|---|
| 1xxx | 鉴权/会话 | 1001 登录失败、1002 凭证不匹配、1003 会话过期、1004 账号锁定、1005 授权码已使用、1006 无权访问 |
| 2xxx | 参数错误 | 2001 参数缺失、2002 格式错误、2003 端口非法、2004 密码强度不足 |
| 3xxx | 资源冲突 | 3001 用户名已存在、3002 端口冲突、3003 端口池耗尽、3004 超出限制区间、3005 代理数上限 |
| 4xxx | 资源不存在 | 4001 用户不存在、4002 代理不存在 |
| 5xxx | 服务端错误 | 5000 内部错误（详情记服务端日志，不外泄） |
| 6xxx | 限流 | 6001 请求过于频繁、6002 登录尝试过多 |

---

## 2. seeinpm API 清单

### 2.1 认证与平台管理员

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | /api/v1/auth/login | 平台管理员登录（账号+密码）→ `{access_token, refresh_token, mustInit:false}`；首次启动引导创建管理员：`POST /api/v1/auth/init`（仅当管理员表为空时可用） |
| POST | /api/v1/auth/refresh | 刷新令牌（旋转） |
| POST | /api/v1/auth/logout | 登出，吊销当前 refresh |
| POST | /api/v1/auth/change-password | 修改管理员密码（旧密码+新密码，新密码强度校验） |
| POST | /api/v1/auth/verify-code | **部署工具专用**：校验授权码+用户名。请求 `{username, authCode}`；响应 `{code:0, data:{tlsFingerprint, serverAddr, serverPort, userConfig}}` / `code:1002 AUTH_CODE_IN_USE`（含 onlineSince）/ `code:1003` 用户已禁用 |

### 2.2 seeinps 用户管理

> 已实现条目按用户名（username）作为资源标识（`:id` 槽位传 username），与现有 DELETE /api/v1/users 行为一致。

| 方法 | 路径 | 说明 | 状态 |
|---|---|---|---|
| GET | /api/v1/users | 用户列表 `{id, username, remark, status, online, expireDate?, expired, maxPorts, portRanges, usedPorts}`（expireDate 为 YYYY-MM-DD，空=永久；usedPorts 为当前活跃分配数） | 已实现 |
| POST | /api/v1/users | 创建用户 `{username, remark?, expireDate?, maxPorts?, portRanges?}` → 自动生成授权码，响应含 `authCode`（**仅此一次明文返回**）。可选限制：expireDate 有效期（YYYY-MM-DD，到期断开+拒绝注册）、maxPorts 端口数量配额（0=不限）、portRanges 用户端口池（须在总池内且总跨度 ≤ maxPorts） | 已实现 |
| PUT | /api/v1/users/:id | 修改用户限制 `{expireDate?, maxPorts?, portRanges?}`；配置收紧导致现有分配违规（池外/超配额）时自动踢线重连，seeinps 30s 内按新约束重新分配端口，响应 `data.kicked` 标记 | 已实现 |
| POST | /api/v1/users/:id/disable | 禁用用户；请求体 `{disconnectNow?: bool}`，`true` 时立即断开已建立的全部连接（推送 SESSION_REVOKE + 释放端口）；禁用后 REGISTER/verify-code 拒绝（1003） | 已实现 |
| POST | /api/v1/users/:id/enable | 启用用户；seeinps 重连后自动恢复 | 已实现 |
| POST | /api/v1/users/:id/reset-code | 重置授权码：旧码立即失效、在线实例被断开（SESSION_REVOKE）；响应 `data.authCode` 新授权码**仅此一次明文返回**。seeinps 侧进程保持运行，在其管理页输入新授权码重绑后自动恢复（见 §3.1 /auth/rebind） | 已实现 |
| DELETE | /api/v1/users?username= | 删除用户（级联：释放端口、断开会话） | 已实现 |
| GET | /api/v1/users/:id | 用户详情（授权码不返回明文，仅 `authCodeUpdatedAt`） | 二期 |
| PATCH | /api/v1/users/:id | 修改备注/分组（备注/端口限制已由 PUT 覆盖） | 二期 |
| GET | /api/v1/users/:id/proxies | 该用户全部代理（含配置、状态） | 二期 |
| GET | /api/v1/users/:id/stats | 该用户流量/连接统计（时间区间） | 二期 |
| GET/POST/PATCH/DELETE | /api/v1/groups | 用户分组管理 | 二期 |

> **有效期/配额执行语义**（2026-08-29 实现）：
> - 有效期到期：PM 30s 定时器断开该用户控制连接与全部代理（kick reason=`user_expired`）；REGISTER 拒绝（1003, reason=`user_expired`）转 seeinps 低速重试；有效期改回今天或以后后 seeinps 自动重连恢复，代理端口保持稳定（历史端口复用）。
> - 端口数量配额：仅限制**新分配**（代理重连复用端口不受限）。超配额的 ALLOC_PORT 被拒（3002），seeinps 创建代理时返回「端口数量已达上限，请联系管理员」；释放端口后 resync 自动恢复。
> - 用户端口池：见 §3.4 users 表 `port_ranges`；分配只能落在用户池 ∩ 总池。

### 2.3 端口池

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | /api/v1/port-pool | 端口池配置 + 使用情况（已分配/空闲/保留，含色块数据） |
| PUT | /api/v1/port-pool | 调整端口池（追加/移除区间、排除端口；冲突检测，缩容时列出受影响端口） |
| GET | /api/v1/port-pool/allocations | 全部端口分配明细（port → 用户/代理） |

### 2.4 代理总览（平台管理员视角）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | /api/v1/proxies | 全局代理列表（分组/状态/类型/用户筛选） |
| GET | /api/v1/proxies/:id | 代理详情（配置 + 状态 + 连接数） |
| GET | /api/v1/proxies/:id/connections | 连接历史（分页，含断开时间、来源 IP、流量） |
| GET | /api/v1/proxies/:id/traffic | 流量时序（minute/hour/day 粒度） |
| POST | /api/v1/proxies/:id/disconnect | 强制断开该代理全部连接 |

#### 2.4.1 当前已实现子集【已实现】

代理标识为 `(username, proxyId)`（seeinps 用户名 + seeinps 上配置的代理 ID），列表显示名为 `username.proxyId`。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | /api/v1/proxies | 所有连接过的代理列表。每项：`{username, proxyId, name, type, status(1启用/0禁用), online, port(在线时公网端口), lastOnlineAt, lastOfflineAt, bytesIn, bytesOut, rateIn, rateOut}`。流量为累计字节（入站=外部→内网，出站=内网→外部，seeinpm 转发处统计），rate 为在线代理 5s 采样速率（字节/秒，离线为 0） |
| GET | /api/v1/proxies/{username}/{proxyId}/sessions | 该代理最近 10 次连接记录：`[{onlineAt, offlineAt(在线中为空), remoteAddr}]`；历史每代理最多保留 50 条 |
| POST | /api/v1/proxies/{username}/{proxyId}/disable | 禁用代理：立即停公网监听并断开活动连接、标记离线，端口仍预留给该代理（不释放回池，启用量自动恢复原端口）；在线 seeinps 收到 `PROXY_REVOKE` 后转入 30s 低速重试（被拒 1006）。被禁用代理不参与 7 天自动清理 |
| POST | /api/v1/proxies/{username}/{proxyId}/enable | 启用代理；seeinps 周期重试 `ALLOC_PORT` 后自动上线（≤30s） |

自动清理：每小时检查一次，删除离线超 7 天且未禁用的代理记录及其连接历史，并将其预留端口放回公共池；代理重新连接（ALLOC_PORT）时自动重建记录。

### 2.5 统计与仪表板

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | /api/v1/stats/overview | 仪表板聚合：在线 seeinps 数、代理数（按状态）、实时带宽、今日流量、端口池水位、系统健康（CPU/内存/磁盘） |
| GET | /api/v1/stats/traffic | 全局流量趋势（时序，按区间与粒度） |
| GET | /api/v1/stats/online-users | 在线 seeinps 列表（含心跳延迟、版本） |

### 2.6 审计与日志

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | /api/v1/audit-logs | 审计日志（操作人/时间/来源 IP/操作/详情，分页筛选） |
| GET | /api/v1/audit-logs/export | 导出（CSV/JSON） |
| GET | /api/v1/logs | 本机运行日志文件列表 + 下载（`?file=xxx&lines=500` 尾部读取） |
| GET | /api/v1/logs/seeinps/:username | 某 seeinps 的运行日志（经控制通道拉取/推送，B端统一入口） |
| WS | /api/v1/logs/stream | 实时日志流（WebSocket，见 §4） |

### 2.7 版本与升级（2026-08-29 实现，v1 仅 seeinps 远程升级）

| 方法 | 路径 | 说明 | 状态 |
|---|---|---|---|
| GET | /api/v1/versions | 版本包列表。`?endpoint=seeinps`（默认 seeinps）。响应 `data: [{id, endpoint, version, fileName, fileSize, sha256, note, releasedBy, createdAt}]`，按时间倒序 | 已实现 |
| POST | /api/v1/versions | 上传版本包（管理员鉴权）。`multipart/form-data`：`endpoint=seeinps`、`version`（五段数字版本号 `1.0.26.0829.01`：大版本.大版本.年.月日.当日序号）、`goos`/`goarch`（目标平台，缺省 linux/amd64；白名单 linux/windows/darwin × amd64/arm64/arm/386）、`note`（更新说明）、`file`（seeinps 二进制，≤200MB）。服务端落盘 `data/releases/seeinps/<version>/<goos>-<goarch>/` 并计算 SHA256。同端同版本号**同平台**重复返回 409。审计 `version_upload` | 已实现 |
| DELETE | /api/v1/versions/{id} | 删除版本记录及安装包文件。审计 `version_delete` | 已实现 |
| GET | /api/v1/clients | 在线节点列表，新增字段 `version`（HELLO 上报）、`goos`/`goarch`（节点平台）、`upgradable`（与节点同平台的最新包比较）、`upgrading`（升级进行中标记） | 已实现 |
| POST | /api/v1/clients/{username}/upgrade | 一键升级。body `{versionId}`；**升级包平台必须与节点平台（goos/goarch）完全一致，否则 400**。流程：UPGRADE_PUSH 预检（PM 与 PS 双重平台校验）→ 0x05 数据流直传二进制 → B 端校验后 rename+exec 自替换重启。低版本推送即回滚。审计 `client_upgrade`。响应后前端轮询 /api/v1/clients 观察版本变化 | 已实现 |
| GET | /api/v1/version/check | 升级检查（三端通用，预留给 seeinpc） | 未实现 |

#### 2.7.1 seeinps B 端自主升级（2026-08-30 实现）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | /api/v1/self-upgrade/status | 自主升级状态 `{stage: idle\|pulling\|receiving\|applying\|failed, version, error, totalBytes, doneBytes}` |
| POST | /api/v1/self-upgrade | 自上传升级（multipart: `version` + `file`，平台须与本节点一致；≤200MB）。后台应用，状态轮询见上 |
| GET | /api/v1/pm-versions | 向 seeinpm 查询本平台可用版本列表（控制通道 VERSION_LIST），返回 `[{id, version, note, fileSize, createdAt}]` |
| POST | /api/v1/pm-upgrade | 从 seeinpm 拉取指定版本升级（body `{version}`），经 VERSION_PULL + 0x05 流直传，完成判定轮询 /auth/status 的 version |

> **升级语义**：升级期间该节点全部代理短暂断开，节点换二进制后自动重连并按历史端口复用恢复映射；
> 允许推送相同或更低版本（手动回滚）；PM 侧不对节点做自动回滚（v1 手动：推送旧包，或节点上用 `seeinps.old` 恢复）。
 ops_http 代理的使用方式：外网 seeinpc 以 `seeinpm公网:opsId` 为 HTTP 代理，携带 `Proxy-Authorization: Basic <proxyUsername:proxyPassword>`；未认证返回 407，目标不在 ACL 网段返回 403，支持 CONNECT 隧道与绝对 URI 转发（协议 §8.4）。

**二期规划**：

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | /api/v1/proxies/:id/disable \| /enable | 启停 |
| POST | /api/v1/proxies/:id/restart | 重启该代理 |
| GET | /api/v1/proxies/:id/connections | 连接历史（含认证失败记录） |
| GET | /api/v1/proxies/:id/traffic | 流量时序 |
| GET | /api/v1/proxies/export | 导出配置 JSON |
| POST | /api/v1/proxies/import | 导入配置（校验后应用） |
| — | — | `type: udp`（UDP 映射）与 `type: ops_socks`（SOCKS5 运维代理）暂未实现，请求返回错误码 2002 |

### 3.3 系统与日志

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | /api/v1/system/health | CPU/内存/磁盘/文件描述符（或句柄） |
| GET | /api/v1/system/status | 与 seeinpm 连接状态（在线/重连中/离线）、心跳延迟、重连历史 |
| POST | /api/v1/system/reload | 配置热重载 |
| GET | /api/v1/logs | 本地运行/访问/审计日志（文件列表、尾部读取、下载） |
| WS | /api/v1/logs/stream | 本地实时日志流 |
| GET | /api/v1/audit-logs | 本地操作审计（B端操作） |

---

## 4. WebSocket 实时流

### 4.1 通用约定

- 端点：`/api/v1/logs/stream`（认证方式：URL query `?token=<access_token>` 或首帧 `{"type":"auth","token":"..."}`）；
- 帧格式：JSON 文本帧 `{ "type": "log"|"status"|"stats"|"ping", "data": {...} }`；
- 服务端每 30s 发 `ping`，客户端 60s 内无响应判定断开，客户端自动重连（指数退避）。

### 4.2 帧类型

| type | 说明 |
|---|---|
| log | 日志行 `{level, ts, component, message, proxyId?}` |
| status | 连接/代理状态变更（seeinpm 推送全部 seeinps 状态；seeinps 推送自身状态） |
| stats | 实时带宽/连接数快照（默认 2s 一次，可订阅指定 proxyId） |
| ping / pong | 保活 |

---

## 5. OpenAPI 与版本管理

- 每个程序提供 `GET /api/v1/openapi.json`（OpenAPI 3.0 规范），前端与外部集成据此生成 SDK；
- 接口演进：新增字段向后兼容；破坏性变更提升 API 主版本（`/api/v2`）；
- 所有写操作（POST/PATCH/DELETE）在审计日志记录：操作人、时间、来源 IP、请求摘要（脱敏）。

---

## 6. API 与内部协议的关系

| 场景 | 走 API | 走控制协议（999） |
|---|---|---|
| 平台管理员管理用户/端口池/全局视图 | ✔ | — |
| seeinps 用户管理本地代理 | ✔（65443） | — |
| seeinps 申请/释放对外端口 | — | ✔（ALLOC_PORT / RELEASE_PORT） |
| seeinpm 推送配置/吊销会话 | — | ✔（CONFIG_PUSH / SESSION_REVOKE） |
| seeinpm B端查看 seeinps 日志 | ✔（B端聚合入口） | ✔（底层拉取/推送） |
| 部署工具校验授权码 | ✔（verify-code，HTTPS） | —（注册在部署完成后的 seeinps 启动时发生） |
