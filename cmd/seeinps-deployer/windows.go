package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// deployWindows 在本机 Windows 上部署 seeinps：
// 1. 将可执行文件放到安装目录
// 2. 写 seeinps.toml（指向 seeinpm 服务器）
// 3. 注册为 Windows 服务（sc create）并启动
// 4. 等待 B 端就绪后调用 /auth/init 完成网页初始化
func (a *API) deployWindows(stream *sseWriter, pack *installPackage, req *installReq) (*deployResult, error) {
	instDir := req.InstallDir
	if instDir == "" {
		instDir = defaultWinInstallDir(installBaseDir())
	}
	if err := os.MkdirAll(filepath.Join(instDir, "conf"), 0755); err != nil {
		return nil, fmt.Errorf("创建安装目录失败: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(instDir, "logs"), 0755); err != nil {
		return nil, fmt.Errorf("创建日志目录失败: %v", err)
	}

	// 复制可执行文件到安装目录
	exeName := pack.binName
	if !strings.EqualFold(filepath.Ext(exeName), ".exe") {
		exeName += ".exe"
	}
	dstExe := filepath.Join(instDir, exeName)
	stream.info("安装 seeinps 到 " + instDir)
	if err := copyFile(pack.binPath, dstExe); err != nil {
		return nil, fmt.Errorf("复制可执行文件失败: %v", err)
	}

	// 写 seeinps.toml（server_addr 指向 seeinpm 控制端口）
	serverAddr := fmt.Sprintf("%s:%d", pack.pmHost, pack.controlPort)
	confContent := seeinpsConfigTemplate(pack, serverAddr, fmt.Sprintf(":%d", pack.bendPort))
	confPath := filepath.Join(instDir, "conf", "seeinps.toml")
	if err := os.WriteFile(confPath, []byte(confContent), 0644); err != nil {
		return nil, fmt.Errorf("写入配置文件失败: %v", err)
	}
	stream.info("配置文件已写入")

	// 注册为 Windows 服务
	svcName := "seeinps"
	serviceBin := dstExe
	stream.info("注册 Windows 服务 " + svcName + "…")
	if err := createWindowsService(svcName, serviceBin, confPath); err != nil {
		return nil, fmt.Errorf("注册服务失败: %v", err)
	}
	if err := startWindowsService(svcName); err != nil {
		return nil, fmt.Errorf("启动服务失败: %v", err)
	}
	stream.info("Windows 服务已启动")

	// 等待 B 端就绪后完成网页初始化
	if err := a.windowsInit(stream, pack); err != nil {
		return nil, fmt.Errorf("初始化网页账号失败: %v", err)
	}

	// 可选：为管理页创建 TCP 映射（占用一个端口池端口）
	webURL := fmt.Sprintf("http://127.0.0.1:%d", pack.bendPort)
	if pack.createWebMapping {
		if fp := createWebMapping(stream, pack, fmt.Sprintf("http://127.0.0.1:%d", pack.bendPort)); fp > 0 {
			webURL = fmt.Sprintf("http://%s:%d", pack.pmHost, fp)
		}
	}

	return &deployResult{
		message: "seeinps " + pack.version + " 已部署并启动成功",
		url:     webURL,
	}, nil
}

// windowsInit 轮询本机 B 端 /health 就绪后调用 /auth/init 完成初始化
func (a *API) windowsInit(stream *sseWriter, pack *installPackage) error {
	stream.info("等待 seeinps 网页就绪，完成账号初始化…")
	base := fmt.Sprintf("http://127.0.0.1:%d", pack.bendPort)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/health")
		if err == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(2 * time.Second)
	}
	time.Sleep(2 * time.Second)

	payload := fmt.Sprintf(`{"username":%q,"authCode":%q,"password":%q}`, pack.username, pack.authCode, pack.password)
	resp, err := http.Post(base+"/api/v1/auth/init", "application/json", strings.NewReader(payload))
	if err != nil {
		return fmt.Errorf("初始化请求失败: %v", err)
	}
	defer resp.Body.Close()
	// 读取简化判断
	if resp.StatusCode == http.StatusConflict {
		return fmt.Errorf("目标 seeinps 已初始化，请勿重复设置密码")
	}
	if resp.StatusCode >= 400 {
		buf := make([]byte, 4)
		n, _ := resp.Body.Read(buf)
		return fmt.Errorf("初始化未成功（HTTP %d: %s）", resp.StatusCode, truncateStr(string(buf[:n]), 200))
	}
	stream.info("网页账号初始化完成")
	return nil
}

// uninstallWindows 卸载：停服务 → 删服务 → 删安装目录
func uninstallWindows() error {
	_ = stopWindowsService("seeinps")
	time.Sleep(500 * time.Millisecond)
	if err := deleteWindowsService("seeinps"); err != nil {
		// 服务可能不存在，忽略删除错误
	}
	dir := defaultWinInstallDir(installBaseDir())
	_ = os.RemoveAll(dir)
	return nil
}

// copyFile 复制文件（保留权限）
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0755)
}
