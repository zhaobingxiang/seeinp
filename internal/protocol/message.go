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
)

// Stream types for data channel
const (
	StreamTypeTCP     byte = 0x01
	StreamTypeUDP     byte = 0x02
	StreamTypeHTTP    byte = 0x03
	StreamTypeOps     byte = 0x04
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
	CodePortConflict        = 3001
	CodePortPoolExhausted   = 3002
	CodePortOutOfRange      = 3003
	CodeProxyLimit          = 3004
	CodeBandwidthExceeded   = 3005
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
