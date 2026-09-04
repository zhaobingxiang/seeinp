//go:build windows

package tunnel

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// AddRoutes 为目标网段列表写入静态路由（先删后加，网关取虚拟网卡自身地址）。
// 返回已成功添加的网段，便于失败时精确回滚。
func AddRoutes(subnets []string, gateway string, ifIdx int) ([]string, error) {
	var added []string
	for _, cidr := range subnets {
		ip, ipNet, err := net.ParseCIDR(cidr)
		if err != nil || ip.To4() == nil {
			continue
		}
		if ones, bits := ipNet.Mask.Size(); bits == 32 && ones == 0 {
			continue // 0.0.0.0/0 全路由禁止下发（兜底防护）
		}
		netAddr := ipNet.IP.To4().String()
		mask := net.IP(ipNet.Mask).String()
		// 先删旧路由（不存在时忽略报错）
		_ = noWindow(exec.Command("route", "delete", netAddr)).Run()
		cmd := noWindow(exec.Command("route", "add", netAddr, "mask", mask, gateway, "if", fmt.Sprint(ifIdx)))
		if out, err := cmd.CombinedOutput(); err != nil {
			return added, fmt.Errorf("添加路由 %s 失败: %v (%s)", cidr, err, strings.TrimSpace(decodeGBK(out)))
		}
		added = append(added, netAddr)
	}
	return added, nil
}

// DeleteRoutes 删除指定网段的静态路由（逐项执行，忽略单项失败）。
func DeleteRoutes(nets []string) {
	for _, n := range nets {
		_ = noWindow(exec.Command("route", "delete", n)).Run()
	}
}
