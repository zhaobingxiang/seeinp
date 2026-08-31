package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// normGOOS 把前端传的缩写归一化为 Go 标准 goos；空则用本机
func normGOOS(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "linux", "darwin", "windows":
		return strings.ToLower(strings.TrimSpace(s))
	case "win", "win32":
		return "windows"
	}
	return runtime.GOOS
}

// normGOARCH 归一化为 Go 标准 goarch；空则用本机
func normGOARCH(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "amd64", "x86_64", "x64":
		return "amd64"
	case "arm64", "aarch64", "armv8":
		return "arm64"
	case "386", "i386", "x86", "i686":
		return "386"
	case "arm", "armv7", "armv7l":
		return "arm"
	}
	return runtime.GOARCH
}

// archFromUname 把 Linux `uname -m` 输出映射为 goarch
func archFromUname(machine string) string {
	m := strings.ToLower(strings.TrimSpace(machine))
	switch {
	case strings.Contains(m, "x86_64"), strings.Contains(m, "amd64"):
		return "amd64"
	case strings.Contains(m, "aarch64"), strings.Contains(m, "arm64"):
		return "arm64"
	case strings.Contains(m, "i386"), strings.Contains(m, "i686"), strings.Contains(m, "i486"):
		return "386"
	case strings.Contains(m, "armv7"), strings.Contains(m, "armv6"), strings.Contains(m, " arm"):
		return "arm"
	default:
		return "amd64" // 兜底
	}
}

// deployResult 部署成功返回的摘要
type deployResult struct {
	message string
	url     string
}

// errNeedUninstall 哨兵错误：检测到已安装 seeinps，需前端引导用户先卸载。
var errNeedUninstall = fmt.Errorf("need_uninstall")

// runDeploy 编排完整部署：检测已安装→确认/卸载→取版本→下载→解析包→写配置→安装→初始化→启动服务。
// 以 SSE stream 回推进度。pc 已构造好（含见 seeinpm base）。
func (a *API) runDeploy(stream *sseWriter, req *installReq, pc *pmClient) (*deployResult, error) {
	// 1. 校验授权
	if _, err := pc.validate(); err != nil {
		return nil, fmt.Errorf("seeinpm 授权校验失败: %v", err)
	}
	stream.info("seeinpm 授权校验通过")

	// 2. 检测是否已安装 seeinps → 提示需先卸载
	installed, detail := a.detectInstalled(&req.Target)
	if installed {
		// 卸载后重试仍检测到已安装：不再循环提示，直接给出可操作的失败原因
		if req.AfterUninstall {
			return nil, fmt.Errorf("卸载后仍检测到已安装的 seeinps（%s）。请手动登录服务器检查："+
				"systemctl status seeinps、/opt/seeinps 目录、以及残留的 seeinps 进程（pgrep -af 'seeinps -conf'）", detail)
		}
		stream.warn("检测到目标主机已安装 seeinps：" + detail)
		stream.confirmUninstall("检测到已有 seeinps 安装（将卸载其服务、/opt/seeinps 目录与残留进程）。确认卸载并继续，或退出安装。")
		// 前端收到 confirm 后决定：a) 调用 /uninstall 再带 afterUninstall 重新发 /install；b) 退出。
		return nil, errNeedUninstall
	}
	stream.info("未检测到已安装的 seeinps，继续部署")

	// 3. 决定目标平台并探测架构
	goos, goarch := normGOOS(""), normGOARCH("")
	var remote *remoteSSH // 仅 Linux 时非空
	if req.Target.Kind == "linux" {
		goos = "linux"
		remote = &remoteSSH{
			Host:     req.Target.Host,
			Port:     req.Target.SSHPort,
			User:     req.Target.Username,
			Pass:     req.Target.Password,
			RootPass: req.Target.RootPass,
		}
		if err := remote.connect(); err != nil {
			return nil, fmt.Errorf("SSH 连接失败: %v", err)
		}
		defer remote.close()
		stream.info("SSH 连接成功: " + remote.Host)

		// 探测架构
		if out, err := remote.run("uname -m"); err == nil {
			goarch = archFromUname(string(out))
			stream.info("检测到系统架构: " + goarch)
		} else {
			stream.warn("探测系统架构失败，默认按 " + goarch + " 处理")
		}
	} else {
		goos = "windows"
		goarch = runtime.GOARCH
	}

	// 4. 取最新版本元数据；Linux 由目标机直连下载（免双重传输），Windows 在部署机下载
	stream.info(fmt.Sprintf("正在获取 seeinps 版本（%s/%s）…", goos, goarch))
	versions, err := pc.listReleases(goos, goarch)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("seeinpm 中暂无 %s/%s 平台的 seeinps 安装包", goos, goarch)
	}
	latest := versions[0]

	// seeinps 配置的 server_addr 必须指向 seeinpm 主机的信令端口，而非部署目标主机
	pmHost, apiPort := parsePMBase(pc)
	pack := &installPackage{
		version:     latest.Version,
		goos:        goos,
		goarch:      goarch,
		username:    req.Username,
		authCode:    req.AuthCode,
		password:    req.Password,
		pmHost:      pmHost,
		apiPort:     apiPort,
		controlPort: defaultPMControlPort,
		bendPort:    pickBendPort(stream, remote),
		fileSize:    latest.FileSize,
		sha256:      latest.Sha256,
	}
	pack.createWebMapping = req.CreateWebMapping

	if req.Target.Kind == "linux" {
		return a.deployLinux(stream, pack, remote, pc)
	}

	// Windows 本机：部署机下载（带进度条）
	tmpDir, err := os.MkdirTemp("", "seeinps-deploy-*")
	if err != nil {
		return nil, fmt.Errorf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	pkgPath := filepath.Join(tmpDir, "seeinps-bin")
	stream.info("正在下载 seeinps " + latest.Version + "（" + humanSize(latest.FileSize) + "）…")
	binName, actualVer, err := pc.download(latest.Version, goos, goarch, pkgPath, latest.FileSize, func(received, total int64) {
		stream.event("progress", map[string]int64{"received": received, "total": total})
	})
	if err != nil {
		return nil, err
	}
	if binName == "" {
		binName = "seeinps.exe"
	}
	stream.info("下载完成（版本 " + actualVer + "），SHA-256 校验通过")
	pack.binPath = pkgPath
	pack.binName = binName
	pack.version = actualVer
	return a.deployWindows(stream, pack, req)
}

// parsePMBase 从 pmClient BaseURL 提取 seeinpm 主机与 API 端口
func parsePMBase(pc *pmClient) (string, int) {
	host, p, err := net.SplitHostPort(strings.TrimPrefix(pc.BaseURL, "http://"))
	if err != nil {
		return "", defaultPMPort
	}
	port := defaultPMPort
	fmt.Sscanf(p, "%d", &port)
	return host, port
}

// pickBendPort 从 65443 起递进探测第一个未被占用的端口，作为 seeinps 管理页（B端）监听端口。
// remote 为 nil 时检测本机，否则经 SSH 在目标机上检测（ss/netstat 查看监听列表）。
func pickBendPort(stream *sseWriter, remote *remoteSSH) int {
	const base, maxTries = 65443, 50
	for i := 0; i < maxTries; i++ {
		port := base + i
		if !bendPortInUse(remote, port) {
			if i > 0 {
				stream.info(fmt.Sprintf("管理页端口 %d 已被占用，自动改用 %d", base, port))
			} else {
				stream.info(fmt.Sprintf("管理页端口：%d", port))
			}
			return port
		}
	}
	stream.warn(fmt.Sprintf("%d-%d 端口探测均被占用，仍默认使用 %d", base, base+maxTries-1, base))
	return base
}

// bendPortInUse 检测目标主机上端口是否已被监听：本机用回环连接测试；
// 远程经 SSH 查询监听列表（兼容无 ss仅有 netstat 的旧系统）。
func bendPortInUse(remote *remoteSSH, port int) bool {
	if remote == nil {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}
	out, err := remote.run(fmt.Sprintf(
		"(ss -tln 2>/dev/null || netstat -tln 2>/dev/null) | grep -qE '[:.]%d[[:space:]]' && echo USED || echo FREE", port))
	if err != nil {
		return false // 探测失败按未占用处理
	}
	return strings.Contains(string(out), "USED")
}

// bendLogin 登录 seeinps B 端，返回 JWT
func bendLogin(baseURL, username, password string) (string, error) {
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	resp, err := http.Post(baseURL+"/api/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var r struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", fmt.Errorf("响应解析失败: %v", err)
	}
	if r.Code != 0 {
		return "", errors.New(r.Message)
	}
	if r.Data.Token == "" {
		return "", errors.New("响应中无令牌")
	}
	return r.Data.Token, nil
}

// bendCreateTCPProxy 在 seeinps B 端创建 TCP 映射（external 端口由 seeinpm 端口池分配），
// 返回分配到的外部端口
func bendCreateTCPProxy(baseURL, token string, localPort int) (int64, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"id": "web-ui", "type": "tcp", "localAddr": "127.0.0.1", "localPort": localPort,
	})
	req, err := http.NewRequest("POST", baseURL+"/api/v1/proxies", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var r struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			ForwardPort int64 `json:"forwardPort"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return 0, fmt.Errorf("响应解析失败: %v", err)
	}
	if r.Code != 0 {
		return 0, errors.New(r.Message)
	}
	return r.Data.ForwardPort, nil
}

// createWebMapping 本机路径：为管理页创建 TCP 映射。返回 seeinpm 分配的外部端口（>0）；失败返回 0。
func createWebMapping(stream *sseWriter, pack *installPackage, baseURL string) int64 {
	stream.info("正在为管理页创建 TCP 映射…")
	token, err := bendLogin(baseURL, pack.username, pack.password)
	if err != nil {
		stream.warn("管理页映射创建失败（登录失败）：" + err.Error() + "，可稍后在管理页手动添加")
		return 0
	}
	forwardPort, err := bendCreateTCPProxy(baseURL, token, pack.bendPort)
	if err != nil {
		stream.warn("管理页映射创建失败：" + err.Error() + "，可稍后在管理页手动添加")
		return 0
	}
	stream.level("success", fmt.Sprintf("管理页映射已创建，外部访问地址: http://%s:%d", pack.pmHost, forwardPort))
	return forwardPort
}

// installPackage 传递给平台部署器的安装包上下文
type installPackage struct {
	binPath          string // 下载到本地的 seeinps 可执行文件路径
	binName          string // 可执行文件名（win 带 .exe）
	version          string
	goos             string
	goarch           string
	username         string
	authCode         string
	password         string
	pmHost           string // seeinpm 服务器主机（生成 seeinps 配置 server_addr 用）
	controlPort      int    // seeinpm 信令/控制端口（99）
	bendPort         int    // seeinps 管理页（B端）监听端口（65443 起递进探测）
	createWebMapping bool   // 部署成功后是否为管理页创建 TCP 映射
	apiPort          int    // seeinpm B端 Web/API 端口（目标机直连下载用）
	fileSize         int64  // 安装包大小（目标机直连下载的进度总大小）
	sha256           string // 安装包 sha256（目标机下载后校验）
}

// humanSize 友好显示字节大小
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// detectInstalled 检测目标主机是否已安装 seeinps。
// 返回 installed 与描述信息。
func (a *API) detectInstalled(t *targetInfo) (bool, string) {
	if t.Kind == "linux" {
		remote := &remoteSSH{Host: t.Host, Port: t.SSHPort, User: t.Username, Pass: t.Password, RootPass: t.RootPass}
		if err := remote.connect(); err != nil {
			return false, "SSH 检测失败"
		}
		defer remote.close()
		// 检查 systemd 服务与常见安装目录。
		// 注意必须精确匹配 "active"：服务未运行时 is-active 输出 "inactive"，同样包含 "active" 子串
		if out, err := remote.run("systemctl is-active seeinps 2>/dev/null || true"); err == nil && strings.TrimSpace(string(out)) == "active" {
			return true, "systemd 服务 seeinps 正在运行"
		}
		if _, err := remote.run("test -f /etc/systemd/system/seeinps.service"); err == nil {
			return true, "已存在 systemd 服务文件"
		}
		if _, err := remote.run("test -d /opt/seeinps"); err == nil {
			return true, "已存在安装目录 /opt/seeinps"
		}
		// systemd 之外运行的实例（如手工 nohup 启动）：会占用管理页端口，部署前必须让用户知情
		if out, err := remote.run("pgrep -af 'seeinps.*-co[nf]' | head -3"); err == nil && strings.TrimSpace(string(out)) != "" {
			return true, "检测到 seeinps 进程正在运行（非 systemd 托管）: " + strings.TrimSpace(string(out))
		}
		return false, "未安装"
	}

	// Windows 本机：检查服务与安装目录
	if svcExists("seeinps") {
		return true, "已注册 Windows 服务 seeinps"
	}
	dir := defaultWinInstallDir(installBaseDir())
	if _, err := os.Stat(filepath.Join(dir, "seeinps.exe")); err == nil {
		return true, "安装目录中已存在 seeinps 可执行文件: " + dir
	}
	return false, "未安装"
}

// runUninstall 卸载已安装的 seeinps
func (a *API) runUninstall(t *targetInfo) error {
	appLog("info", "[uninstall] 开始卸载 kind=%s host=%s port=%d", t.Kind, t.Host, t.SSHPort)
	if t.Kind == "linux" {
		remote := &remoteSSH{Host: t.Host, Port: t.SSHPort, User: t.Username, Pass: t.Password, RootPass: t.RootPass}
		if err := remote.connect(); err != nil {
			appLog("error", "[uninstall] SSH 连接失败: %v", err)
			return err
		}
		defer remote.close()
		if err := uninstallLinux(remote); err != nil {
			appLog("error", "[uninstall] 卸载失败: %v", err)
			return err
		}
		appLog("info", "[uninstall] 卸载完成")
		return nil
	}
	if err := uninstallWindows(); err != nil {
		appLog("error", "[uninstall] 卸载失败: %v", err)
		return err
	}
	appLog("info", "[uninstall] 卸载完成")
	return nil
}

// seeinpsConfigTemplate 生成 seeinps.toml（与 config_ps.go 结构一致）
func seeinpsConfigTemplate(pack *installPackage, serverAddr string, bendAddr string) string {
	var b bytes.Buffer
	b.WriteString("[server]\n")
	b.WriteString(fmt.Sprintf("server_addr = \"%s\"\n", serverAddr))
	b.WriteString("\n[auth]\n")
	b.WriteString(fmt.Sprintf("username = \"%s\"\n", pack.username))
	b.WriteString(fmt.Sprintf("auth_code = \"%s\"\n", pack.authCode))
	b.WriteString("\n[tls]\n")
	b.WriteString("enabled = true\n")
	// seeinpm 控制通道默认使用自签证书，必须跳过校验，否则 seeinps TLS 握手失败无法连接
	b.WriteString("skip_verify = true\n")
	b.WriteString("\n[local]\n")
	b.WriteString(fmt.Sprintf("bend_addr = \"%s\"\n", bendAddr))
	b.WriteString("\n[logging]\n")
	b.WriteString("level = \"info\"\n")
	b.WriteString("path = \"logs\"\n")
	b.WriteString("max_size = 100\n")
	b.WriteString("max_backups = 7\n")
	return b.String()
}

// installBaseDir 部署工具默认安装根目录（Windows 用户目录）
func installBaseDir() string {
	if runtime.GOOS == "windows" {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, ".seeinp")
		}
	}
	return "."
}

// defaultWinInstallDir 默认安装目录
func defaultWinInstallDir(base string) string {
	return filepath.Join(base, "seeinps")
}
