package main

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
)

// netSplitHostPort 拆分 host:port；缺端口时返回 error 由调用方回退
func netSplitHostPort(addr string) (host, port string, err error) {
	if addr == "" {
		return "", "", fmt.Errorf("empty addr")
	}
	return net.SplitHostPort(addr)
}

// targetInfo 一次部署的目标描述
type targetInfo struct {
	Kind     string `json:"kind"`     // "windows" | "linux"
	Host     string `json:"host"`     // Linux 时必填
	SSHPort  int    `json:"sshPort"`  // 默认 22
	Username string `json:"username"` // SSH 登录用户
	RootPass string `json:"rootPass"` // 非 root 用户时提权用
	Password string `json:"password"` // SSH 登录密码
}

// validate 校验并补齐目标表单
func (t *targetInfo) validate() error {
	switch t.Kind {
	case "windows":
		// 本机部署：无额外必填
	case "linux":
		if strings.TrimSpace(t.Host) == "" {
			return fmt.Errorf("请输入 Linux 服务器地址")
		}
		if t.SSHPort == 0 {
			t.SSHPort = 22
		}
		if t.SSHPort < 1 || t.SSHPort > 65535 {
			return fmt.Errorf("SSH 端口非法")
		}
		if strings.TrimSpace(t.Username) == "" {
			return fmt.Errorf("请输入 SSH 用户名")
		}
		if t.Password == "" {
			return fmt.Errorf("请输入 SSH 密码")
		}
		if t.Username != "root" && strings.TrimSpace(t.RootPass) == "" {
			return fmt.Errorf("非 root 用户需填写 root 用户密码（用于提权安装服务）")
		}
	default:
		return fmt.Errorf("未知部署目标: %s", t.Kind)
	}
	return nil
}

// decodeJSONBody 把请求体 JSON 解码到 v
func decodeJSONBody(r interface{ Read([]byte) (int, error) }, v interface{}) error {
	return json.NewDecoder(r).Decode(v)
}
