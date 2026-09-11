package main

// Entry 一条运维 VPN 入口（对应一张卡片）
type Entry struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Server        string   `json:"server"`                  // 指定的代理服务器地址（来自设置中的服务器池；空=按池顺序回退，非空=只连该台不回退），卡片不展示
	OpsID         int      `json:"opsId"`                   // 代理ID（运维端口）
	ProxyUser     string   `json:"proxyUser"`               // 运维代理账号
	ProxyPass     string   `json:"proxyPass,omitempty"`     // DPAPI 加密后的 base64 密文
	ACL           []string `json:"acl"`                     // 目标网段（来自 seeinps ACL，可编辑）
	ProbeIntranet bool     `json:"probeIntranet,omitempty"` // 勾选后探测内网目标（默认仅探测代理可用性）
	ProbeTarget   string   `json:"probeTarget,omitempty"`   // 内网探测目标（ip:port）
	SourcePS      string   `json:"sourcePs,omitempty"`      // 来源 seeinps 管理端地址
	Manual        bool     `json:"manual,omitempty"`        // 手动添加（非拉取生成）
}

// ServerConf 代理服务器（seeinpm A 端）配置：在设置页统一管理，卡片按下拉分配
type ServerConf struct {
	Name string `json:"name"` // 展示名（主服务器/备用服务器/自定义）
	Addr string `json:"addr"` // 域名或 IP（不含端口；连接端口即运维代理ID）
}

// Settings 全局设置
type Settings struct {
	ProbeIntervalSec int          `json:"probeIntervalSec"` // 探测周期（秒）
	MinimizeToTray   bool         `json:"minimizeToTray"`   // 关闭最小化到托盘
	LogLevel         string       `json:"logLevel"`         // 日志级别 debug/info/warn/error
	Servers          []ServerConf `json:"servers"`          // 代理服务器池（按顺序回退；空则用内置默认）
}

// defaultServers 内置代理服务器池（主/备），设置中被清空时兜底
func defaultServers() []ServerConf {
	return []ServerConf{
		{Name: "主服务器", Addr: "see.timemsee.cn"},
		{Name: "备用服务器", Addr: "see.timesee.cn"},
	}
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
		Servers:          defaultServers(),
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

// FetchOpsItem 登录 seeinps 后拉取到的单个运维 HTTP 代理（待用户确认生成卡片）
type FetchOpsItem struct {
	OpsID     int      `json:"opsId"`
	ProxyUser string   `json:"proxyUser"`
	ACL       []string `json:"acl"`      // 服务端 ACL（已剔除危险网段），用于前端预填
	NeedACL   bool     `json:"needAcl"` // 服务端无可用 ACL（为空或 0.0.0.0/0），必须手动填写网段
}

// FetchSeeinpsResult 「登录 seeinps 获取」第一步结果：登录+拉取列表，不落库
type FetchSeeinpsResult struct {
	OpResult
	NeedCaptcha bool           `json:"needCaptcha"`
	CaptchaID   string         `json:"captchaId"`
	CaptchaB64  string         `json:"captchaB64"`
	SourcePS    string         `json:"sourcePs"` // 实际登录成功的 seeinps 地址（host:port）
	Items       []FetchOpsItem `json:"items"`
}

// ConfirmOpsItem 确认生成卡片时单个代理的用户编辑结果
type ConfirmOpsItem struct {
	OpsID     int      `json:"opsId"`
	ProxyUser string   `json:"proxyUser"` // 运维代理用户名（必填，服务端为空时可在此补填）
	ProxyPass string   `json:"proxyPass"` // 运维代理密码（可留空，稍后在卡片填写）
	Name      string   `json:"name"`      // 代理名称（留空用默认「账号 · 运维ID」）
	ACL       []string `json:"acl"`
}
