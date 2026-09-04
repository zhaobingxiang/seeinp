package main

// Entry 一条运维 VPN 入口（对应一张卡片）
type Entry struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Server        string   `json:"server"`                  // 代理服务器（seeinpm A 端），卡片不展示
	OpsID         int      `json:"opsId"`                   // 代理ID（运维端口）
	ProxyUser     string   `json:"proxyUser"`               // 运维代理账号
	ProxyPass     string   `json:"proxyPass,omitempty"`     // DPAPI 加密后的 base64 密文
	ACL           []string `json:"acl"`                     // 目标网段（来自 seeinps ACL，可编辑）
	ProbeIntranet bool     `json:"probeIntranet,omitempty"` // 勾选后探测内网目标（默认仅探测代理可用性）
	ProbeTarget   string   `json:"probeTarget,omitempty"`   // 内网探测目标（ip:port）
	SourcePS      string   `json:"sourcePs,omitempty"`      // 来源 seeinps 管理端地址
	Manual        bool     `json:"manual,omitempty"`        // 手动添加（非拉取生成）
}

// Settings 全局设置
type Settings struct {
	ProbeIntervalSec int    `json:"probeIntervalSec"` // 探测周期（秒）
	MinimizeToTray   bool   `json:"minimizeToTray"`   // 关闭最小化到托盘
	LogLevel         string `json:"logLevel"`         // 日志级别 debug/info/warn/error
}

// Config 持久化配置（安装目录 conf/config.json）
type Config struct {
	Settings        Settings `json:"settings"`
	Entries         []*Entry `json:"entries"`
	IgnoredVersions []string `json:"ignoredVersions,omitempty"` // 跳过的升级版本（服务端不记录）
}

func defaultSettings() Settings {
	return Settings{
		ProbeIntervalSec: 30,
		MinimizeToTray:   true,
		LogLevel:         "info",
	}
}

// CardView 前端卡片视图（含运行态）
type CardView struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Server        string   `json:"server"`
	OpsID         int      `json:"opsId"`
	ProxyUser     string   `json:"proxyUser"`
	HasPwd        bool     `json:"hasPwd"`
	ACL           []string `json:"acl"`
	ProbeTarget   string   `json:"probeTarget"`
	ProbeIntranet bool     `json:"probeIntranet"`
	SourcePS      string   `json:"sourcePs"`
	Manual        bool     `json:"manual"`
	Status        string   `json:"status"` // running/connecting/ready/waitpwd/fail
	Probe         string   `json:"probe"`  // ok/407/403/unreachable/unknown/dialfail
	ProbeMsg      string   `json:"probeMsg"`
	RTT           string   `json:"rtt"`
	LastProbe     string   `json:"lastProbe"`
}

// StateView 前端全量状态
type StateView struct {
	Cards       []CardView `json:"cards"`
	Settings    Settings   `json:"settings"`
	RunningID   string     `json:"runningId"`
	RunningName string     `json:"runningName"`
	ConnCount   int64      `json:"connCount"`
	Version     string     `json:"version"`
	LogLevel    string     `json:"logLevel"`
	Admin       bool       `json:"admin"`
	DriverOK    bool       `json:"driverOk"`
	Probing     bool       `json:"probing"`
	LogPath     string     `json:"logPath"`
}

// OpResult 通用操作结果
type OpResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// AddSeeinpsResult 「添加 seeinps 运维」结果（含验证码交互）
type AddSeeinpsResult struct {
	OpResult
	NeedCaptcha bool   `json:"needCaptcha"`
	CaptchaID   string `json:"captchaId"`
	CaptchaB64  string `json:"captchaB64"`
	Added       int    `json:"added"`
}
