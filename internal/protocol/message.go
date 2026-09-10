package protocol

import (
	"time"
)

// Message types
const (
	TypeHello          = "HELLO"
	TypeHelloResp      = "HELLO_RESP"
	TypeRegister       = "REGISTER"
	TypeRegisterResp   = "REGISTER_RESP"
	TypeHeartbeat      = "HEARTBEAT"
	TypeHeartbeatResp  = "HEARTBEAT_RESP"
	TypeAllocPort      = "ALLOC_PORT"
	TypeAllocPortResp  = "ALLOC_PORT_RESP"
	TypeReleasePort    = "RELEASE_PORT"
	TypeReleasePortResp = "RELEASE_PORT_RESP"
	TypeProxyReady     = "PROXY_READY"
	TypeProxyClosed    = "PROXY_CLOSED"
	TypeConfigPush     = "CONFIG_PUSH"
	TypeConfigPushResp = "CONFIG_PUSH_RESP"
	TypeSessionRevoke  = "SESSION_REVOKE"
	TypeProxyRevoke    = "PROXY_REVOKE"
	// 日志混合架构（2026-08-29）：审计日志 B 端上报 A 端（AUDIT_SYNC）；
	// A 端按需拉取 B 端运行日志（LOG_LIST_REQ/RESP、LOG_CONTENT_REQ/RESP）
	TypeAuditSync        = "AUDIT_SYNC"
	TypeLogListReq       = "LOG_LIST_REQ"
	TypeLogListResp      = "LOG_LIST_RESP"
	TypeLogContentReq    = "LOG_CONTENT_REQ"
	TypeLogContentResp   = "LOG_CONTENT_RESP"
	// A 端在线查询/修改 B 端运行日志级别（LOGGING_GET_REQ/RESP、LOGGING_SET_REQ/RESP）：
	// 此前只能登录 B 端管理台查看与修改级别，与"A 端集中管理"的定位不一致。
	TypeLoggingGetReq  = "LOGGING_GET_REQ"
	TypeLoggingGetResp = "LOGGING_GET_RESP"
	TypeLoggingSetReq  = "LOGGING_SET_REQ"
	TypeLoggingSetResp = "LOGGING_SET_RESP"
	// 版本管理（2026-08-29）：A 端推送升级包（UPGRADE_PUSH 通知 + 0x05 数据流直传二进制），
	// B 端校验通过后换二进制重启，成功与否由重连后的 HELLO 版本判定，异常经 UPGRADE_REPORT 上报
	TypeUpgradePush     = "UPGRADE_PUSH"
	TypeUpgradePushResp = "UPGRADE_PUSH_RESP"
	TypeUpgradeReport   = "UPGRADE_REPORT"
	// B 端自主升级（2026-08-30）：B 端查询 A 端可用版本列表（VERSION_LIST），
	// 并按版本号请求拉取升级包（VERSION_PULL 应答包元信息，随后 A 端经 0x05 数据流直传）
	TypeVersionListReq  = "VERSION_LIST_REQ"
	TypeVersionListResp = "VERSION_LIST_RESP"
	TypeVersionPullReq  = "VERSION_PULL_REQ"
	TypeVersionPullResp = "VERSION_PULL_RESP"
	// 周期流量配额（2026-09）：A 端把用户配额状态推给 B 端缓存，供 seeinps web 页面展示进度/重置日期/超额告警。
	// 常态随心跳响应携带；超额/重置等状态跃迁时经 QUOTA_STATUS 主动推送一次。
	TypeQuotaStatus = "QUOTA_STATUS"
)

// Stream types for data channel
const (
	StreamTypeTCP     byte = 0x01
	StreamTypeUDP     byte = 0x02
	StreamTypeHTTP    byte = 0x03
	StreamTypeOps     byte = 0x04
	StreamTypeUpgrade byte = 0x05
)

// Error codes
const (
	CodeOK                  = 0
	CodeInternal            = 1000
	CodeAuthFailed          = 1001
	CodeAuthCodeInUse       = 1002
	CodeSessionRevoked      = 1003
	CodeVersionMismatch     = 1004
	CodeRateLimited         = 1005
	CodeProxyDisabled       = 1006
	CodeBadRequest          = 2000
	CodeUnsupportedType     = 2001
	CodePortConflict         = 3001
	CodePortPoolExhausted    = 3002
	CodePortOutOfRange       = 3003
	CodeProxyLimit           = 3004
	CodeBandwidthExceeded    = 3005 // 预留：带宽超限走令牌桶节流，不产生拒绝码
	CodeTrafficQuotaExceeded = 3006 // 周期总流量达上限：ALLOC 拒绝新建（web-ui 除外）
)

// Message is the common envelope for all control messages
type Message struct {
	Type    string      `json:"type"`
	ID      string      `json:"id"`
	Version string      `json:"version,omitempty"`
	Ts      int64       `json:"ts"`
	Code    *int        `json:"code,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}

// HelloData is the payload for HELLO message
type HelloData struct {
	Version string `json:"version"`
	Arch    string `json:"arch"`
	OS      string `json:"os"`
	Build   string `json:"build,omitempty"`
}

// HelloRespData is the payload for HELLO_RESP message
type HelloRespData struct {
	ServerVersion string   `json:"serverVersion"`
	Caps          []string `json:"caps"`
}

// RegisterData is the payload for REGISTER message
type RegisterData struct {
	Username string `json:"username"`
	AuthHash string `json:"authHash"`
}

// RegisterRespData is the payload for REGISTER_RESP message
type RegisterRespData struct {
	SessionID string `json:"sessionId,omitempty"`
}

// AllocPortData is the payload for ALLOC_PORT message
type AllocPortData struct {
	ProxyID string `json:"proxyId"`
	Type    string `json:"type"` // tcp, udp, ops_http, ops_socks
	Port    *int   `json:"port,omitempty"` // requested port, nil for random
}

// AllocPortRespData is the payload for ALLOC_PORT_RESP message
type AllocPortRespData struct {
	ProxyID string `json:"proxyId"`
	Port    int    `json:"port"`
}

// QuotaStatusData 是 A 端同步给 B 端的周期流量配额快照（心跳响应携带 / QUOTA_STATUS 推送）。
// 字节为 in+out 合计；Limit=0 或 Enabled=false 表示不限。
type QuotaStatusData struct {
	Enabled     bool   `json:"enabled"`
	Period      string `json:"period,omitempty"`
	Used        int64  `json:"used"`
	Limit       int64  `json:"limit"`
	PeriodStart int64  `json:"periodStart"`
	PeriodEnd   int64  `json:"periodEnd"`
	Exceeded    bool   `json:"exceeded"`
}

// ReleasePortData is the payload for RELEASE_PORT message
type ReleasePortData struct {
	ProxyID string `json:"proxyId"`
	Port    int    `json:"port"`
}

// ProxyRevokeData is the payload for PROXY_REVOKE message (seeinpm -> seeinps push).
// Sent when a proxy is disabled on seeinpm: seeinps must clear the allocated
// forward port and retry allocation later (rejected with 1006 while disabled).
type ProxyRevokeData struct {
	ProxyID string `json:"proxyId"`
	Reason  string `json:"reason"` // proxy_disabled
}

// AuditSyncItem 是 B 端（seeinps）上报的一条本地审计记录。
// LocalID 为 B 端 local_audit_logs.id：链路中断时记录会留在本地，重连后补传，
// A 端以 (source, ext_id) 唯一索引做幂等去重，因此补传可安全重放。
type AuditSyncItem struct {
	LocalID   int64  `json:"localId,omitempty"`
	Username  string `json:"username"`
	Action    string `json:"action"`
	Target    string `json:"target"`
	Detail    string `json:"detail"`
	CreatedAt int64  `json:"createdAt"`
}

// AuditSyncData 是 AUDIT_SYNC 的载荷（B 端 -> A 端批量上报）
type AuditSyncData struct {
	Items []AuditSyncItem `json:"items"`
}

// LogFileInfo 是日志文件元信息（名称/大小/修改时间）
type LogFileInfo struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
}

// LogListReqData 是 LOG_LIST_REQ 的载荷（A 端 -> B 端，请求文件列表）
type LogListReqData struct{}

// LogListRespData 是 LOG_LIST_RESP 的载荷（B 端 -> A 端，返回文件列表）
type LogListRespData struct {
	Files []LogFileInfo `json:"files"`
}

// LogContentReqData 是 LOG_CONTENT_REQ 的载荷（A 端 -> B 端，请求文件尾部内容）
// Download=true 时返回整文件（供 A 端以附件形式提供下载）；Keyword 非空时由 B 端过滤，
// 避免"只搜到已拉取窗口"的不一致体验。
type LogContentReqData struct {
	File     string `json:"file"`
	Lines    int    `json:"lines"`
	Keyword  string `json:"keyword,omitempty"`
	Download bool   `json:"download,omitempty"`
}

// LogContentRespData 是 LOG_CONTENT_RESP 的载荷（B 端 -> A 端，返回文件内容）
type LogContentRespData struct {
	File    string `json:"file"`
	Content string `json:"content"`
}

// LoggingSetReqData 是 LOGGING_SET_REQ 的载荷（A 端 -> B 端，在线修改日志级别）
type LoggingSetReqData struct {
	Level string `json:"level"`
}

// LoggingSetRespData 是 LOGGING_SET_RESP 的载荷（B 端 -> A 端，返回生效后的级别）
type LoggingSetRespData struct {
	Level string `json:"level"`
}

// LoggingGetReqData 是 LOGGING_GET_REQ 的载荷（A 端 -> B 端，查询当前日志级别）
type LoggingGetReqData struct{}

// LoggingGetRespData 是 LOGGING_GET_RESP 的载荷（B 端 -> A 端）
type LoggingGetRespData struct {
	Level      string `json:"level"`
	Path       string `json:"path"`
	MaxSize    int    `json:"maxSize"`
	MaxBackups int    `json:"maxBackups"`
}

// VersionListItem 是 VERSION_LIST_RESP 中的一条版本摘要（不含文件）
type VersionListItem struct {
	ID        int64  `json:"id"`
	Version   string `json:"version"`
	Note      string `json:"note"`
	FileSize  int64  `json:"fileSize"`
	CreatedAt int64  `json:"createdAt"`
}

// VersionListRespData 是 VERSION_LIST_RESP 的载荷（A 端 -> B 端，按 B 端平台过滤后的可用版本）
type VersionListRespData struct {
	Items []VersionListItem `json:"items"`
}

// VersionListReqData 是 VERSION_LIST_REQ 的载荷（B 端 -> A 端，按 B 端平台过滤）
type VersionListReqData struct{}

// VersionPullReqData 是 VERSION_PULL_REQ 的载荷（B 端 -> A 端，请求拉取指定版本）
type VersionPullReqData struct {
	Version string `json:"version"`
}

// UpgradePushData 是 UPGRADE_PUSH 的载荷（A 端 -> B 端，通知即将推送升级包）
type UpgradePushData struct {
	ReleaseID int64  `json:"releaseId"`
	Version   string `json:"version"`
	GoOS      string `json:"goos"`
	GoArch    string `json:"goarch"`
	Sha256    string `json:"sha256"`
	Size      int64  `json:"size"`
}

// UpgradeReportData 是 UPGRADE_REPORT 的载荷（B 端 -> A 端，上报升级包接收结果；
// stage=downloaded 表示校验通过即将换二进制重启，stage=failed 表示校验失败）
type UpgradeReportData struct {
	Version string `json:"version,omitempty"`
	Stage   string `json:"stage"`
	Error   string `json:"error,omitempty"`
}

// StreamHeader is the header at the beginning of each data stream
type StreamHeader struct {
	StreamType byte   `json:"-"`
	ProxyID    string `json:"-"`
}

// NewMessage creates a new message with ID and timestamp
func NewMessage(msgType string, data interface{}) *Message {
	return &Message{
		Type: msgType,
		ID:   GenerateID(),
		Ts:   time.Now().Unix(),
		Data: data,
	}
}

// NewResponse creates a response message
func NewResponse(reqMsg *Message, code int, data interface{}) *Message {
	return &Message{
		Type: reqMsg.Type + "_RESP",
		ID:   reqMsg.ID,
		Ts:   time.Now().Unix(),
		Code: &code,
		Data: data,
	}
}
