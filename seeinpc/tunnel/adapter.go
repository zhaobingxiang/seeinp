//go:build windows

// Package tunnel 实现 seeinpc 的虚拟网卡隧道层：
// wintun 适配器 + gVisor netstack 协议栈（替代原 tun2socks.exe 外部进程），
// 把目标网段的 TCP 流量经 HTTP 代理（CONNECT + Basic 认证）导入内网。
package tunnel

import (
	"fmt"
	"net"
	"os/exec"
	"strings"

	wtun "golang.zx2c4.com/wireguard/tun"
)

// AdapterName 虚拟网卡名称（单活 VPN，固定名称，重连时复用同一适配器）
const AdapterName = "SeeinpcVpn"

// DefaultMTU wintun 支持的最大 MTU（与 proxytovpn 保持同量级）
const DefaultMTU = 65535

// CreateDevice 创建（或复用同名）wintun 虚拟网卡，返回设备句柄。
// 需要管理员权限；wintun.dll 必须已位于可执行文件同目录（应用目录加载规则）。
func CreateDevice(mtu int) (wtun.Device, error) {
	dev, err := wtun.CreateTUN(AdapterName, mtu)
	if err != nil {
		return nil, fmt.Errorf("创建虚拟网卡失败（需管理员权限）: %w", err)
	}
	return dev, nil
}

// ConfigureAddress 为适配器配置静态 IPv4 地址（netsh 优先，失败降级 PowerShell）。
// 幂等：若目标地址已配置（重复连接/适配器复用场景）则直接视为成功，不报错。
func ConfigureAddress(ip, mask string) error {
	cmd := noWindow(exec.Command("netsh", "interface", "ipv4", "set", "address",
		"name="+AdapterName, "source=static", "address="+ip, "mask="+mask))
	if out, err := cmd.CombinedOutput(); err != nil {
		if hasAddress(ip) {
			return nil
		}
		ps := fmt.Sprintf("New-NetIPAddress -InterfaceAlias '%s' -IPAddress %s -PrefixLength %d -ErrorAction Stop",
			AdapterName, ip, maskToPrefix(mask))
		cmd2 := noWindow(exec.Command("powershell", "-NoProfile", "-Command", ps))
		if out2, err2 := cmd2.CombinedOutput(); err2 != nil {
			if hasAddress(ip) {
				return nil
			}
			return fmt.Errorf("配置网卡地址失败: netsh=%v(%s) powershell=%v(%s)",
				err, strings.TrimSpace(decodeGBK(out)), err2, strings.TrimSpace(decodeGBK(out2)))
		}
	}
	return nil
}

// hasAddress 检查适配器是否已配置指定 IP（用于幂等判断）。
func hasAddress(ip string) bool {
	out, err := noWindow(exec.Command("netsh", "interface", "ipv4", "show", "addresses",
		"name="+AdapterName)).Output()
	return err == nil && strings.Contains(string(out), ip)
}

// RemoveAddress 移除适配器上的静态地址（断开时保持适配器但清理 IP）。
func RemoveAddress(ip string) {
	_ = noWindow(exec.Command("netsh", "interface", "ipv4", "delete", "address",
		"name="+AdapterName, "address="+ip)).Run()
}

// AdapterIndex 通过 netsh 解析适配器接口索引（失败降级 PowerShell）。
func AdapterIndex() (int, error) {
	out, err := noWindow(exec.Command("netsh", "interface", "ipv4", "show", "interfaces")).Output()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.Contains(line, AdapterName) {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) >= 1 {
				var idx int
				if _, err := fmt.Sscanf(fields[0], "%d", &idx); err == nil && idx > 0 {
					return idx, nil
				}
			}
		}
	}
	out, err = noWindow(exec.Command("powershell", "-NoProfile", "-Command",
		fmt.Sprintf("(Get-NetAdapter -Name '%s' -ErrorAction Stop).ifIndex", AdapterName))).Output()
	if err != nil {
		return 0, fmt.Errorf("查询接口索引失败: %w", err)
	}
	var idx int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &idx); err != nil {
		return 0, fmt.Errorf("解析接口索引失败: %s", strings.TrimSpace(string(out)))
	}
	return idx, nil
}

func maskToPrefix(mask string) int {
	ip := net.ParseIP(mask)
	if ip == nil || ip.To4() == nil {
		return 24
	}
	bits := 0
	for _, b := range ip.To4() {
		for b > 0 {
			bits += int(b & 1)
			b >>= 1
		}
	}
	return bits
}
