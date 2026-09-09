package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// remoteSSH 远程 Linux 部署的 SSH/SFTP 会话封装
type remoteSSH struct {
	Host     string
	Port     int
	User     string
	Pass     string
	RootPass string // 非 root 用户提权用

	client *ssh.Client
}

const sshInstallDir = "/opt/seeinps"

// remotePath 把 Windows 反斜杠路径转为正斜杠（SFTP 目标机是 Linux，反斜杠会被当作字面字符）。
// 部署工具在 Windows 上运行，filepath.Join/Dir 返回反斜杠，直接传给 SFTP 会创建
// 名为 "\opt\seeinps" 的目录而非 /opt/seeinps。
func remotePath(p string) string {
	return strings.ReplaceAll(filepath.ToSlash(p), `\`, `/`)
}

// friendlySSHError 把 SSH 连接错误翻译为可操作的提示
func friendlySSHError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "EOF"):
		return "SSH 握手被对端关闭（EOF）：请确认地址与端口确实指向目标机的 SSH 服务" +
			"（NAT/端口映射需指向目标机 22 端口）；服务器 fail2ban 可能已临时封禁你的 IP；" +
			"也可能是 sshd 禁用了兼容算法，可在目标机 sshd 日志中核实。"
	case strings.Contains(msg, "unable to authenticate"), strings.Contains(msg, "authenticate"):
		return "SSH 认证失败：请检查用户名与登录密码是否正确。"
	case strings.Contains(msg, "connection refused"):
		return "连接被拒绝：目标端口没有服务在监听，请确认 SSH 端口是否正确。"
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "i/o timeout"):
		return "连接超时：网络不通或被防火墙拦截，请确认地址、端口与防火墙放行规则。"
	case strings.Contains(msg, "no common algorithm"), strings.Contains(msg, "no supported"):
		return "SSH 算法协商失败：服务器使用了过旧或过新的算法，需调整 sshd 配置。"
	case strings.Contains(msg, "no route"), strings.Contains(msg, "unreachable"):
		return "网络不可达：请检查服务器地址是否正确。"
	}
	return "SSH 连接失败: " + msg
}

// connect 建立 SSH 会话并做钥匙交换
func (r *remoteSSH) connect() error {
	port := r.Port
	if port == 0 {
		port = 22
	}
	cfg := &ssh.ClientConfig{
		User:            r.User,
		Auth:            []ssh.AuthMethod{ssh.Password(r.Pass)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // 首次部署可接受未知主机指纹
		Timeout:         15 * time.Second,
	}
	// 尝试加入键盘交互认证（部分服务器只开放 keyboard-interactive）
	cfg.Auth = append(cfg.Auth, ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
		answers := make([]string, len(questions))
		for i := range answers {
			answers[i] = r.Pass
		}
		return answers, nil
	}))

	client, err := ssh.Dial("tcp", fmt.Sprintf("%s:%d", r.Host, port), cfg)
	if err != nil {
		return err
	}
	r.client = client
	return nil
}

func (r *remoteSSH) close() {
	if r.client != nil {
		_ = r.client.Close()
	}
}

// run 在远端执行命令（以登录用户身份），返回 stdout。非 root 时可用 RootPass 提权由调用方自理。
func (r *remoteSSH) run(cmd string) ([]byte, error) {
	if r.client == nil {
		return nil, fmt.Errorf("SSH 未连接")
	}
	sess, err := r.client.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	var out, errb bytes.Buffer
	sess.Stdout = &out
	sess.Stderr = &errb
	if err := sess.Run(cmd); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.Bytes(), fmt.Errorf("%s", msg)
	}
	return out.Bytes(), nil
}

// runAsRoot 提权执行：root 用户直接用；普通用户尝试 sudo -S（以 RootPass 喂密码）。
// 整条命令经 bash -c 包裹后交给 sudo，确保含 && / || / | 的链式命令全部在 root 下执行。
func (r *remoteSSH) runAsRoot(cmd string) ([]byte, error) {
	// 先看当前用户是否 root
	if r.User == "root" {
		return r.run(cmd)
	}
	// 用密码提升到 root：sudo -S，从 stdin 喂密码；bash -c 包裹整条命令确保全链 root 执行
	command := fmt.Sprintf("printf '%%s\\n' '%s' | sudo -S -p '' bash -c %s", escapeShell(r.RootPass), shellQuote(cmd))
	return r.run(command)
}

// sftpCopy 把本地文件 binPath 推送到远端目标 dstPath（保持可执行位）。
// onProgress 非空时上报上传进度。
// 策略：优先直接 SFTP 写到最终路径（绕过 /tmp 限制）；失败则回退到
// SFTP 写临时目录 + sudo mv（兼容目标目录需要 root 权限的场景）。
// 写入后显式验证文件存在且大小正确，任何一步失败都返回明确错误。
func (r *remoteSSH) sftpCopy(binPath, dstPath string, executable bool, onProgress progressFunc) error {
	cli, err := sftp.NewClient(r.client)
	if err != nil {
		return err
	}
	defer cli.Close()

	src, err := os.Open(binPath)
	if err != nil {
		return err
	}
	defer src.Close()
	srcInfo, _ := src.Stat()
	if srcInfo == nil || srcInfo.Size() == 0 {
		return fmt.Errorf("本地文件为空或不可读: %s", binPath)
	}
	fileSize := srcInfo.Size()

	// tryWriteSFTP 写单个 SFTP 文件；写入后验证远程大小 >= expectedSize
	tryWriteSFTP := func(dest string, expectedSize int64) error {
		f, err := cli.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY)
		if err != nil {
			return fmt.Errorf("OpenFile(%s) 失败: %w", dest, err)
		}
		if _, serr := src.Seek(0, io.SeekStart); serr != nil {
			f.Close()
			return fmt.Errorf("Seek 失败: %w", serr)
		}
		var body io.Reader = src
		if onProgress != nil && expectedSize > 0 {
			body = &progressReader{r: src, total: expectedSize, onProgress: onProgress}
		}
		written, cErr := io.Copy(f, body)
		if cErr != nil {
			f.Close()
			return fmt.Errorf("io.Copy 写入 %s 失败（已写 %d 字节）: %w", dest, written, cErr)
		}
		if cErr = f.Close(); cErr != nil {
			return fmt.Errorf("Close(%s) 失败: %w", dest, cErr)
		}
		// 写入后显式验证远程文件大小
		info, serr := cli.Stat(dest)
		if serr != nil {
			return fmt.Errorf("写入后 Stat(%s) 失败: %w", dest, serr)
		}
		if info.Size() < expectedSize {
			return fmt.Errorf("远程文件 %s 大小不匹配：期望 %d，实际 %d", dest, expectedSize, info.Size())
		}
		return nil
	}

	// verifyRemote 验证远程路径存在且大小 >= expected
	verifyRemote := func(path string, expected int64) error {
		info, err := cli.Stat(path)
		if err != nil {
			return fmt.Errorf("远程文件 %s 不存在或无法 Stat: %w", path, err)
		}
		if info.Size() < expected {
			return fmt.Errorf("远程文件 %s 大小不足：期望 >=%d，实际 %d", path, expected, info.Size())
		}
		return nil
	}

	// 策略 1：直接写最终路径（用户对目标目录有写权限时一步到位，完全绕过 /tmp）
	if err := tryWriteSFTP(dstPath, fileSize); err == nil {
		if executable {
			_ = cli.Chmod(dstPath, 0o755)
		}
		appLog("info", "[sftp] 直接写 %s 成功，大小=%d", dstPath, fileSize)
		return nil
	} else {
		appLog("warn", "[sftp] 直接写 %s 失败（回退到 tmp+mv）: %v", dstPath, err)
	}

	// 策略 2：写用户主目录临时文件 + sudo mv（目标目录需要 root 权限时）
	home, _ := r.run("echo -n $HOME")
	tmpDir := strings.TrimSpace(string(home)) + "/.seeinps-tmp"
	if tmpDir == "" || tmpDir == "/.seeinps-tmp" {
		tmpDir = "/root/.seeinps-tmp"
	}
	if _, err := r.run(fmt.Sprintf("mkdir -p %s", shellQuote(tmpDir))); err != nil {
		return fmt.Errorf("创建临时目录 %s 失败: %w", tmpDir, err)
	}
	tmpDst := remotePath(path.Join(tmpDir, path.Base(dstPath)))
	if err := tryWriteSFTP(tmpDst, fileSize); err != nil {
		return fmt.Errorf("SFTP 写临时文件 %s 失败: %w", tmpDst, err)
	}
	appLog("info", "[sftp] 写临时文件 %s 成功，大小=%d", tmpDst, fileSize)

	// 验证临时文件到位（防御性检查）
	if err := verifyRemote(tmpDst, fileSize); err != nil {
		return fmt.Errorf("临时文件验证失败: %w", err)
	}

	// sudo 移动到目标位置
	mkdir := fmt.Sprintf("mkdir -p %s", shellQuote(path.Dir(dstPath)))
	mv := fmt.Sprintf("mv -f %s %s", shellQuote(tmpDst), shellQuote(dstPath))
	chmod := fmt.Sprintf("chmod 0755 %s", shellQuote(dstPath))
	if _, err := r.runAsRoot(mkdir + " && " + mv + " && " + chmod); err != nil {
		return fmt.Errorf("sudo mv 失败（%s → %s）: %w", tmpDst, dstPath, err)
	}
	_, _ = r.runAsRoot(fmt.Sprintf("rm -f %s", shellQuote(tmpDst)))

	// 最终验证目标路径
	if err := verifyRemote(dstPath, fileSize); err != nil {
		return fmt.Errorf("最终验证失败（sudo mv 后文件未就位）: %w", err)
	}
	appLog("info", "[sftp] %s → %s 成功，大小=%d", tmpDst, dstPath, fileSize)
	return nil
}

// deployLinux 在远程 Linux 上部署 seeinps：写包/配置文件 → 生成 systemd unit → 初始化 → 启动服务
// checkSeeinpmReachable 部署前置检查：目标服务器必须能直连 seeinpm 的 API 端口（下载安装包）
// 与信令端口（部署后建立连接），任一不通则拒绝部署。
func checkSeeinpmReachable(stream *sseWriter, pack *installPackage, remote *remoteSSH) error {
	stream.info("检测目标服务器到 seeinpm 的连通性…")
	// API 端口：HTTP 探测，任何 HTTP 状态码都视为可达
	out, err := remote.run(fmt.Sprintf(
		"curl -s --connect-timeout 4 -o /dev/null -w '%%{http_code}' http://%s:%d/ || echo FAIL", pack.pmHost, pack.apiPort))
	code := strings.TrimSpace(string(out))
	if err != nil || code == "FAIL" || code == "000" || code == "" {
		return fmt.Errorf("目标服务器无法访问 seeinpm 的 API 端口 %s:%d。按部署要求不允许继续："+
			"安装包需由目标机直连 seeinpm 下载，请检查目标机的网络与防火墙后重试", pack.pmHost, pack.apiPort)
	}
	// 信令端口：TLS 探测。仅把"连接层失败"视为不可达（6=域名解析失败 7=连接拒绝 28=超时 127=缺 curl）；
	// 52（TLS 握手成功但服务端不回 HTTP 响应）等其余退出码均说明端口实际可达
	out, err = remote.run(fmt.Sprintf(
		"curl -sk --connect-timeout 4 -o /dev/null https://%s:%d/; echo $?", pack.pmHost, pack.controlPort))
	rc := strings.TrimSpace(string(out))
	if err == nil && rc == "127" {
		return fmt.Errorf("目标服务器缺少 curl，无法下载安装包与执行连通性检测，请先安装 curl（yum install -y curl）后重试")
	}
	if err != nil || rc == "6" || rc == "7" || rc == "28" || rc == "" {
		return fmt.Errorf("目标服务器无法访问 seeinpm 的信令端口 %s:%d（curl 退出码 %s），部署后 seeinps 将无法连接 seeinpm，不允许部署。"+
			"请检查目标机的网络与防火墙", pack.pmHost, pack.controlPort, rc)
	}
	stream.info("连通性检查通过（API 与信令端口均可达）")
	return nil
}

// probeDirectDownload 快速探测目标机是否能从 seeinpm 下载数据（Range 0-99 = 100 字节，超时 5 秒）。
// 用于在完整直连下载前判断网络路径是否支持 HTTP 响应体传输，避免卡住 45 秒。
func probeDirectDownload(stream *sseWriter, pack *installPackage, remote *remoteSSH) bool {
	dlURL := fmt.Sprintf("http://%s:%d/api/v1/ps-release/download?username=%s&authCode=%s&goos=%s&goarch=%s",
		pack.pmHost, pack.apiPort, pack.username, pack.authCode, pack.goos, pack.goarch)
	urlB64 := base64.StdEncoding.EncodeToString([]byte(dlURL))
	out, err := remote.run(fmt.Sprintf(
		"URL=$(printf '%%s' '%s' | base64 -d) && curl -s -r 0-1023 --connect-timeout 5 --max-time 8 \"$URL\" -o /tmp/_probe.bin 2>/dev/null && stat -c '%%s' /tmp/_probe.bin 2>/dev/null; rm -f /tmp/_probe.bin",
		urlB64))
	if err != nil {
		appLog("info", "[deploy] 直连探测失败: %v", err)
		return false
	}
	size := 0
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &size)
	if size > 0 {
		stream.info(fmt.Sprintf("直连探测成功（%d 字节），开始直连下载…", size))
		appLog("info", "[deploy] 直连探测成功 %d bytes", size)
		return true
	}
	appLog("info", "[deploy] 直连探测返回 0 字节，网络不支持直连下载")
	return false
}

// downloadOnRemote 让目标服务器用 curl 直连 seeinpm 下载安装包：
// 使用 8KB Range 分片下载（某些网络路径对 >10KB 的 HTTP 响应有限制），
// 后台启动 → SSH 轮询文件大小上报进度 → 完成后校验 sha256 → 安装到位。
// 返回 true 表示成功；返回 false 表示失败（调用方可回退到 SFTP 上传）。
func (a *API) downloadOnRemote(stream *sseWriter, pack *installPackage, remote *remoteSSH, dstPath string) bool {
	stream.info(fmt.Sprintf("通知目标服务器直连下载 seeinps %s（%s，8KB 分片）…", pack.version, humanSize(pack.fileSize)))

	dlURL := fmt.Sprintf("http://%s:%d/api/v1/ps-release/download?username=%s&authCode=%s&goos=%s&goarch=%s",
		pack.pmHost, pack.apiPort, pack.username, pack.authCode, pack.goos, pack.goarch)
	urlB64 := base64.StdEncoding.EncodeToString([]byte(dlURL))

	// 写 shell 下载脚本到目标机：8KB Range 分片循环，避免网络路径阻断大响应
	totalBytes := pack.fileSize
	dlScript := fmt.Sprintf(`#!/bin/sh
URL=$(printf '%%s' '%s' | base64 -d)
DST=%s
rm -f "$DST" /tmp/seeinps-pkg.done /tmp/seeinps-pkg.sha /tmp/seeinps-pkg.exit /tmp/seeinps-pkg.err
DL=0; TOTAL=%d; CHUNK=8192; EC=0
while [ $DL -lt $TOTAL ]; do
  END=$((DL + CHUNK - 1))
  [ $END -ge $TOTAL ] && END=$((TOTAL - 1))
  curl -s --connect-timeout 10 --max-time 30 -r ${DL}-${END} "$URL" >> "$DST" 2>>/tmp/seeinps-pkg.err
  EC=$?
  [ $EC -ne 0 ] && break
  DL=$((DL + CHUNK))
  [ $DL -gt $TOTAL ] && DL=$TOTAL
done
echo $EC > /tmp/seeinps-pkg.exit
[ $EC -eq 0 ] && sha256sum "$DST" > /tmp/seeinps-pkg.sha 2>/dev/null
touch /tmp/seeinps-pkg.done
`, urlB64, shellQuote(dstPath+".part"), totalBytes)

	if _, err := remote.runAsRoot(fmt.Sprintf("printf '%%s' %s | base64 -d > /tmp/seeinps-dl.sh && chmod +x /tmp/seeinps-dl.sh",
		shellB64(base64.StdEncoding.EncodeToString([]byte(dlScript))))); err != nil {
		stream.warn("直连下载准备失败（回退到 SFTP 上传）：" + err.Error())
		appLog("warn", "[deploy] 直连下载准备失败: %v", err)
		return false
	}

	// 预检：部分带安全 agent/网关的机器只放行小响应，吞掉 HTTP 大文件下载体 ——
	// 这类环境直连必然 0 字节超时。用 1MB Range 探针尽早判定，命中即切 SFTP，不再空等分片下载。
	probeScript := fmt.Sprintf(`#!/bin/sh
URL=$(printf '%%s' '%s' | base64 -d)
curl -sS --connect-timeout 10 --max-time 30 -r 0-1048575 "$URL" -o /tmp/seeinps-pkg.probe 2>/tmp/seeinps-pkg.probe.err
RC=$?
SZ=$(stat -c %%s /tmp/seeinps-pkg.probe 2>/dev/null || echo 0)
rm -f /tmp/seeinps-pkg.probe
echo "rc=$RC size=$SZ"
`, urlB64)
	probeOut, probeErr := remote.runAsRoot(fmt.Sprintf("printf '%%s' %s | base64 -d > /tmp/seeinps-probe.sh && chmod +x /tmp/seeinps-probe.sh && bash /tmp/seeinps-probe.sh; rm -f /tmp/seeinps-probe.sh",
		shellB64(base64.StdEncoding.EncodeToString([]byte(probeScript)))))
	_ = probeErr // runAsRoot 的退出码仅反映外层 rm 是否成功，结果一律从 stdout 解析
	var preRC, preSize int
	fmt.Sscanf(strings.TrimSpace(string(probeOut)), "rc=%d size=%d", &preRC, &preSize)
	if preRC != 0 || preSize == 0 {
		stream.warn(fmt.Sprintf("直连下载被目标机安全策略拦截（探针 rc=%d 已下载 %d 字节），回退到 SFTP 上传", preRC, preSize))
		appLog("warn", "[deploy] 直连下载被拦截: 探针 rc=%d size=%d", preRC, preSize)
		return false
	}
	stream.info("直连下载预检通过（探针 rc=0 已下载 1MiB），开始分片下载")

	// 后台启动下载脚本
	if _, err := remote.runAsRoot("nohup /tmp/seeinps-dl.sh >/dev/null 2>&1 & echo started"); err != nil {
		stream.warn("直连下载启动失败（回退到 SFTP 上传）：" + err.Error())
		appLog("warn", "[deploy] 直连下载启动失败: %v", err)
		return false
	}

	// 轮询进度（下载文件大小 vs 元数据大小），卡死检测后失败退出
	deadline := time.Now().Add(15 * time.Minute)
	lastSize, stallSince, milestone := int64(-1), time.Now(), 0
	for time.Now().Before(deadline) {
		time.Sleep(800 * time.Millisecond)
		out, _ := remote.run(fmt.Sprintf("stat -c %%s %s.part 2>/dev/null || echo 0", shellQuote(dstPath)))
		size := int64(0)
		fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &size)
		done, _ := remote.run("test -f /tmp/seeinps-pkg.done && echo yes || echo no")
		isDone := strings.TrimSpace(string(done)) == "yes"
		if !isDone && pack.fileSize > 0 {
			if size != lastSize {
				stream.event("progress", map[string]int64{"received": size, "total": pack.fileSize})
			}
		}
		if pack.fileSize > 0 {
			if pct := int(size * 100 / pack.fileSize); pct/25 > milestone {
				milestone = pct / 25
				appLog("info", "[deploy] 目标机下载进度 %d%%（%d/%d 字节）", milestone*25, size, pack.fileSize)
			}
		}
		if isDone {
			break
		}
		if size == lastSize {
			if time.Since(stallSince) > 45*time.Second {
				errOut, _ := remote.run("cat /tmp/seeinps-pkg.err 2>/dev/null | tail -3")
				stream.warn(fmt.Sprintf("直连下载停滞超 45 秒（已下载 %d/%d 字节），回退到 SFTP 上传；curl 输出：%s", size, pack.fileSize, strings.TrimSpace(string(errOut))))
				appLog("warn", "[deploy] 直连下载停滞 %d/%d curl_stderr=%s", size, pack.fileSize, strings.TrimSpace(string(errOut)))
				return false
			}
		} else {
			lastSize, stallSince = size, time.Now()
		}
	}
	if done, _ := remote.run("test -f /tmp/seeinps-pkg.done && echo yes || echo no"); strings.TrimSpace(string(done)) != "yes" {
		stream.warn("直连下载超时（15 分钟），回退到 SFTP 上传")
		appLog("warn", "[deploy] 直连下载超时")
		return false
	}
	stream.event("progress", map[string]int64{"received": pack.fileSize, "total": pack.fileSize})

	// 校验 curl 退出码
	exitOut, _ := remote.run("cat /tmp/seeinps-pkg.exit 2>/dev/null || echo 1")
	if rc := strings.TrimSpace(string(exitOut)); rc != "0" {
		errOut, _ := remote.run("cat /tmp/seeinps-pkg.err 2>/dev/null | tail -3")
		stream.warn("直连下载失败（curl 退出码 " + rc + "），回退到 SFTP 上传；curl 输出：" + strings.TrimSpace(string(errOut)))
		appLog("warn", "[deploy] 直连下载 curl 退出码=%s stderr=%s", rc, strings.TrimSpace(string(errOut)))
		return false
	}
	// 校验 sha256
	shaOut, _ := remote.run("head -1 /tmp/seeinps-pkg.sha 2>/dev/null")
	fields := strings.Fields(strings.TrimSpace(string(shaOut)))
	if len(fields) == 0 || !strings.EqualFold(fields[0], pack.sha256) {
		stream.warn("直连下载 sha256 校验失败，回退到 SFTP 上传")
		appLog("warn", "[deploy] 直连下载 sha256 不匹配")
		return false
	}
	// 安装到位并清理临时文件
	if _, err := remote.runAsRoot(fmt.Sprintf("chmod +x %s.part && mv %s.part %s && rm -f /tmp/seeinps-curl.conf /tmp/seeinps-pkg.done /tmp/seeinps-pkg.sha /tmp/seeinps-pkg.exit",
		shellQuote(dstPath), shellQuote(dstPath), shellQuote(dstPath))); err != nil {
		stream.warn("直连下载就位失败（回退到 SFTP 上传）：" + err.Error())
		appLog("warn", "[deploy] 直连下载就位失败: %v", err)
		return false
	}
	stream.info("目标机直连下载完成，sha256 校验通过")
	appLog("info", "[deploy] 直连下载完成并校验通过")
	return true
}

func (a *API) deployLinux(stream *sseWriter, pack *installPackage, remote *remoteSSH, pc *pmClient) (*deployResult, error) {
	instDir := sshInstallDir
	binPath := path.Join(instDir, "seeinps")
	confPath := path.Join(instDir, "conf", "seeinps.toml")

	stream.info("创建安装目录 " + instDir)
	if _, err := remote.runAsRoot(fmt.Sprintf("mkdir -p %s/conf %s/logs %s/data", shellQuote(instDir), shellQuote(instDir), shellQuote(instDir))); err != nil {
		return nil, fmt.Errorf("创建安装目录失败: %v", err)
	}

	// 部署前置检查：目标服务器必须能直连 seeinpm（下载安装包 + 后续建立信令连接都依赖）
	if err := checkSeeinpmReachable(stream, pack, remote); err != nil {
		return nil, err
	}

	// 下载安装包：先快速探测目标机是否能从 seeinpm 下载数据（Range 0-99 = 100 字节，超时 5 秒），
	// 探测成功才尝试完整直连下载；探测失败则跳过，直接走部署机下载 + SFTP 上传。
	// （某些网络路径对 HTTP 响应体有限制，直连下载会卡住 45 秒才超时，浪费时间。）
	if probeDirectDownload(stream, pack, remote) {
		if a.downloadOnRemote(stream, pack, remote, binPath) {
			goto downloadDone
		}
	}
	// 直连下载不可用或失败：部署机下载 + SFTP 上传
	stream.info("改用部署机下载 → SFTP 上传…")
	appLog("info", "[deploy] 使用部署机下载+SFTP上传")
	{
		tmpDir, err := os.MkdirTemp("", "seeinps-deploy-*")
		if err != nil {
			return nil, fmt.Errorf("创建临时目录失败: %v", err)
		}
		defer os.RemoveAll(tmpDir)
		localPath := filepath.Join(tmpDir, "seeinps-bin")
		stream.info(fmt.Sprintf("部署机下载 seeinps %s（%s）…", pack.version, humanSize(pack.fileSize)))
		binName, actualVer, err := pc.download(pack.version, pack.goos, pack.goarch, localPath, pack.fileSize, func(received, total int64) {
			stream.event("progress", map[string]int64{"received": received, "total": total})
		})
		if err != nil {
			return nil, fmt.Errorf("部署机下载失败: %v", err)
		}
		if binName != "" {
			appLog("info", "[deploy] 部署机下载完成，版本=%s 就位=%s", actualVer, binName)
		}
		stream.info("部署机下载完成，SHA-256 校验通过。正在 SFTP 上传到目标机…")
		if err := remote.sftpCopy(localPath, binPath, true, func(received, total int64) {
			stream.event("progress", map[string]int64{"received": received, "total": total})
		}); err != nil {
			return nil, fmt.Errorf("SFTP 上传失败: %v", err)
		}
		stream.info("SFTP 上传完成")
	}
downloadDone:

	// 写配置前复查端口：递进探测在部署最开始，之后下载可能耗时数分钟，端口可能已被新进程抢占；
	// 被占则自动顺延（必须在写 seeinps.toml 之前完成，bend_addr 才会同步）
	recheckBendPort(stream, pack)

	// 写 seeinps.toml（server_addr 指向 seeinpm 控制端口）
	serverAddr := fmt.Sprintf("%s:%d", pack.pmHost, pack.controlPort)
	confContent := seeinpsConfigTemplate(pack, serverAddr, fmt.Sprintf(":%d", pack.bendPort))
	if err := remote.writeRemoteFile(confContent, confPath, true); err != nil {
		return nil, fmt.Errorf("写入配置文件失败: %v", err)
	}
	stream.info("配置文件已写入")

	// 生成 systemd unit
	unit := systemdUnit("seeinps", instDir)
	unitPath := "/etc/systemd/system/seeinps.service"
	if err := remote.writeRemoteFile(unit, unitPath, true); err != nil {
		return nil, fmt.Errorf("写入 systemd 单元失败: %v", err)
	}
	if _, err := remote.runAsRoot("systemctl daemon-reload"); err != nil {
		return nil, fmt.Errorf("systemctl daemon-reload 失败: %v", err)
	}
	stream.info("systemd 服务单元已就绪")

	// 启动前校验二进制与配置确已就位（部分安全加固系统会静默拦截写入，缺失时提前报错而非 203/EXEC 崩溃循环）
	if out, err := remote.runAsRoot(fmt.Sprintf("test -s %s && test -s %s && echo OK || echo MISSING", shellQuote(binPath), shellQuote(confPath))); err != nil || strings.TrimSpace(string(out)) != "OK" {
		return nil, fmt.Errorf("安装文件未就位（%s 或 %s 缺失），目标服务器可能限制了文件写入，请检查后重试", binPath, confPath)
	}

	// 先启动服务完成初始化：seeinps 启动后 B 端监听管理页端口，初始化接口才能被调用
	stream.info("启动 seeinps 服务…")
	if _, err := remote.runAsRoot("systemctl enable --now seeinps"); err != nil {
		return nil, fmt.Errorf("启动服务失败: %v", err)
	}

	// 等待 B 端就绪后调用初始化接口
	if err := a.remoteInit(stream, pack, remote); err != nil {
		return nil, fmt.Errorf("初始化网页账号失败: %v", err)
	}

	webURL := fmt.Sprintf("http://%s:%d", remote.Host, pack.bendPort)
	if pack.createWebMapping {
		if fp := createWebMappingRemote(stream, pack, remote); fp > 0 {
			webURL = fmt.Sprintf("http://%s:%d", pack.pmHost, fp)
		}
	}

	return &deployResult{
		message: fmt.Sprintf("seeinps %s 已在 %s 部署并启动成功", pack.version, remote.Host),
		url:     webURL,
	}, nil
}

// createWebMappingRemote 远程路径：经 SSH 在目标机回环调用 B 端 API，为管理页创建 TCP 映射。
// 返回 seeinpm 分配的外部端口（>0）；失败返回 0（仅告警，不中断部署）。
func createWebMappingRemote(stream *sseWriter, pack *installPackage, remote *remoteSSH) int64 {
	stream.info("正在为管理页创建 TCP 映射（经 SSH 在目标机执行）…")
	base := fmt.Sprintf("http://127.0.0.1:%d", pack.bendPort)

	loginBody, _ := json.Marshal(map[string]string{"username": pack.username, "password": pack.password})
	loginB64 := base64.StdEncoding.EncodeToString(loginBody)
	loginCmd := fmt.Sprintf(
		"printf '%%s' %s | base64 -d > /tmp/_wm_login.json && "+
			"curl -s -X POST %s/api/v1/auth/login -H 'Content-Type: application/json' -d @/tmp/_wm_login.json && "+
			"rm -f /tmp/_wm_login.json",
		shellB64(loginB64), base)
	out, err := remote.runAsRoot(loginCmd)
	if err != nil {
		stream.warn("管理页映射创建失败（登录失败）：" + err.Error() + "，可稍后在管理页手动添加")
		return 0
	}
	token := extractJSONField(string(out), "token")
	if token == "" {
		stream.warn("管理页映射创建失败（登录未成功：" + truncateStr(strings.TrimSpace(string(out)), 120) + "），可稍后在管理页手动添加")
		return 0
	}

	proxyBody, _ := json.Marshal(map[string]interface{}{
		"id": "web-ui", "type": "tcp", "localAddr": "127.0.0.1", "localPort": pack.bendPort,
	})
	proxyB64 := base64.StdEncoding.EncodeToString(proxyBody)
	proxyCmd := fmt.Sprintf(
		"printf '%%s' %s | base64 -d > /tmp/_wm_proxy.json && "+
			"curl -s -X POST %s/api/v1/proxies -H 'Content-Type: application/json' -H 'Authorization: Bearer %s' -d @/tmp/_wm_proxy.json && "+
			"rm -f /tmp/_wm_proxy.json",
		shellB64(proxyB64), base, token)
	out, err = remote.runAsRoot(proxyCmd)
	if err != nil {
		stream.warn("管理页映射创建失败：" + err.Error() + "，可稍后在管理页手动添加")
		return 0
	}
	fpStr := extractJSONField(string(out), "forwardPort")
	if fpStr == "" {
		stream.warn("管理页映射创建失败（服务器返回：" + truncateStr(strings.TrimSpace(string(out)), 160) + "），可稍后在管理页手动添加")
		return 0
	}
	var fp int64
	fmt.Sscanf(fpStr, "%d", &fp)
	stream.level("success", fmt.Sprintf("管理页映射已创建，外部访问地址: http://%s:%d", pack.pmHost, fp))
	return fp
}

// extractJSONField 从 SSE/JSON 响应中提取顶层 data 内的字符串/数字字段（轻量解析，避免引入依赖）
func extractJSONField(raw, field string) string {
	// 形如 {"code":0,"message":"ok","data":{...,"token":"xxx",...}}
	re := regexp.MustCompile(`"` + field + `"\s*:\s*"?([^",}]+)"?`)
	m := re.FindStringSubmatch(raw)
	if len(m) < 2 {
		return ""
	}
	return strings.Trim(m[1], `"`)
}

// remoteInit 经 SSH 隧道/直连调用远端 B 端 /auth/init 完成网页初始化。
// 由于调用方（部署机）可能无法直连远端管理页端口，这里通过 SSH exec curl 在目标机回环调用。
func (a *API) remoteInit(stream *sseWriter, pack *installPackage, remote *remoteSSH) error {
	stream.info("等待 seeinps 网页就绪，完成账号初始化…")
	// 轮询服务健康；期间若发现服务启动失败，立即带上日志摘录报错，而不是傻等 60 秒后报裸 exit 7
	journalDump := func() string {
		out, _ := remote.runAsRoot("systemctl is-active seeinps 2>/dev/null; journalctl -u seeinps -n 6 --no-pager 2>/dev/null | tail -6")
		return strings.TrimSpace(string(out))
	}
	deadline := time.Now().Add(60 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		out, err := remote.runAsRoot(fmt.Sprintf("curl -s -o /dev/null -w '%%{http_code}' http://127.0.0.1:%d/health 2>/dev/null || echo none", pack.bendPort))
		code := strings.TrimSpace(string(out))
		if err == nil && (code == "200" || code == "404" || strings.HasPrefix(code, "4")) {
			ready = true
			break
		}
		if state, _ := remote.run("systemctl is-active seeinps 2>/dev/null"); strings.TrimSpace(string(state)) == "failed" {
			return fmt.Errorf("seeinps 服务启动失败，日志摘录：%s", truncateStr(journalDump(), 500))
		}
		time.Sleep(2 * time.Second)
	}
	if !ready {
		// journal 里没有 seeinps 的运行日志（logx 写文件），补抓 [WEB] 绑定相关行：
		// B端绑定失败不退进程（5s 重试），最常见原因就是端口被占，只报 journal 摘录会误导排查
		webLog := ""
		if out, err := remote.runAsRoot(fmt.Sprintf(
			"tail -n 300 %s/logs/seeinps_*.log 2>/dev/null | grep -iE '\\[WEB\\]|bind|listen|address already' | tail -4", sshInstallDir)); err == nil {
			webLog = strings.TrimSpace(string(out))
		}
		detail := truncateStr(journalDump(), 400)
		if webLog != "" {
			detail += "；seeinps 日志摘录：" + truncateStr(webLog, 300)
		}
		return fmt.Errorf("管理页端口 %d 在 60 秒内未就绪（若端口被其他进程占用，seeinps 会持续 5 秒重试绑定但不退出，服务仍显示 active），服务状态/日志：%s", pack.bendPort, detail)
	}
	time.Sleep(2 * time.Second)

	// 调用 auth/init：将 JSON payload 经 base64 写成临时文件再解码，避免 shell 转义问题。
	// 注意：$(printf ...) 命令替换在 runAsRoot 的 bash -c '...' 包裹下不工作（单引号内 $() 是字面值），
	// 所以改用文件方式传递 JSON body。
	payload := fmt.Sprintf(`{"username":%q,"authCode":%q,"password":%q}`, pack.username, pack.authCode, pack.password)
	b64 := base64.StdEncoding.EncodeToString([]byte(payload))
	initCmd := fmt.Sprintf(
		"printf '%%s' %s | base64 -d > /tmp/_seeinps_init.json && "+
			"curl -s -X POST http://127.0.0.1:%d/api/v1/auth/init -H 'Content-Type: application/json' -d @/tmp/_seeinps_init.json && "+
			"rm -f /tmp/_seeinps_init.json",
		shellB64(b64), pack.bendPort)
	out, err := remote.runAsRoot(initCmd)
	if err != nil {
		return fmt.Errorf("初始化请求失败: %v（服务状态/日志：%s）", err, truncateStr(journalDump(), 400))
	}
	resp := strings.TrimSpace(string(out))
	if !strings.Contains(resp, `"code":0`) && !strings.Contains(resp, `"code": 0`) {
		if strings.Contains(resp, "3001") {
			return fmt.Errorf("目标 seeinps 已初始化，请勿重复设置密码")
		}
		return fmt.Errorf("初始化未成功（服务器返回：%s）", truncateStr(resp, 200))
	}
	stream.info("网页账号初始化完成")
	return nil
}

// writeRemoteFile 经 SFTP 写入远端文件。
// 若系统目录（如 /etc/systemd/system）当前用户不可写，则先写到用户主目录临时文件，
// 再以 root（sudo）移动到位，避免普通用户无写权限导致失败。
func (r *remoteSSH) writeRemoteFile(content, dstPath string, asRoot bool) error {
	cli, err := sftp.NewClient(r.client)
	if err != nil {
		return err
	}
	defer cli.Close()

	writeViaSFTP := func(dst string) error {
		if err := cli.MkdirAll(path.Dir(dst)); err != nil {
			return err
		}
		f, err := cli.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY)
		if err != nil {
			return fmt.Errorf("OpenFile(%s): %w", dst, err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			f.Close()
			return fmt.Errorf("Write(%s): %w", dst, err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("Close(%s): %w", dst, err)
		}
		// 写入后显式验证内容长度
		info, err := cli.Stat(dst)
		if err != nil {
			return fmt.Errorf("Stat(%s): %w", dst, err)
		}
		if info.Size() < int64(len(content)) {
			return fmt.Errorf("文件 %s 大小不匹配：期望 %d，实际 %d", dst, len(content), info.Size())
		}
		return nil
	}

	// 直接按系统路径写（root 用户可行，普通用户若可写也通畅）
	if err := writeViaSFTP(dstPath); err == nil {
		if asRoot {
			_, _ = r.runAsRoot(fmt.Sprintf("chmod 644 %s", shellQuote(dstPath)))
		}
		return nil
	}

	// 普通用户 → 写临时文件后提权移动（使用用户主目录，兼容 /tmp 受限的环境）
	home, _ := r.run("echo -n $HOME")
	tmpDir := strings.TrimSpace(string(home))
	if tmpDir == "" {
		tmpDir = "/root"
	}
	tmp := remotePath(path.Join(tmpDir, ".seeinps-write.tmp"))
	if err := writeViaSFTP(tmp); err != nil {
		return fmt.Errorf("写临时文件 %s 失败: %w", tmp, err)
	}
	// 以 root 移动并设置权限/属主
	if _, err := r.runAsRoot(fmt.Sprintf("install -m 644 -o root -g root %s %s && rm -f %s",
		shellQuote(tmp), shellQuote(dstPath), shellQuote(tmp))); err != nil {
		return fmt.Errorf("install 移动文件失败（%s → %s）: %w", tmp, dstPath, err)
	}
	// 最终验证
	info, err := cli.Stat(dstPath)
	if err != nil || info.Size() < int64(len(content)) {
		return fmt.Errorf("最终验证失败：文件 %s 未就位或大小不足", dstPath)
	}
	return nil
}

// systemdUnit 生成 seeinps systemd 服务单元
func systemdUnit(serviceName, instDir string) string {
	return fmt.Sprintf(`[Unit]
Description=seeinps service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=%s
ExecStart=%s/seeinps -conf %s/conf/seeinps.toml
Restart=on-failure
RestartSec=5
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
`, instDir, instDir, instDir)
}

// uninstallLinux 卸载：停服务 → 移除 unit → 删除安装目录
func uninstallLinux(remote *remoteSSH) error {
	_, _ = remote.runAsRoot("systemctl stop seeinps 2>/dev/null; systemctl disable seeinps 2>/dev/null; true")
	// 清理 systemd 之外的残留 seeinps 进程（如手工 nohup 启动的实例）。
	// 模式用 [nf] 防止 pgrep/pkill 匹配到自身；-conf 特征不会误伤以 -c 传参的同名程序（如 frps）。
	_, _ = remote.runAsRoot("pkill -f 'seeinps.*-co[nf]' 2>/dev/null; true")
	_, _ = remote.runAsRoot("rm -f /etc/systemd/system/seeinps.service")
	if _, err := remote.runAsRoot("systemctl daemon-reload"); err != nil {
		return err
	}
	if _, err := remote.runAsRoot("rm -rf " + sshInstallDir); err != nil {
		return err
	}
	return nil
}

func escapeShell(s string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `'\''`).Replace(s)
}

// shellB64 把 base64 字符串安全地包进 shell 单引号（base64 只含 A-Za-z0-9+/=，可安全单引号包裹）
func shellB64(s string) string {
	return "'" + s + "'"
}

func shellQuote(s string) string {
	// 简化：单引号包裹，若含单引号则用双引号+转义兜底
	if !strings.Contains(s, "'") {
		return "'" + s + "'"
	}
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
