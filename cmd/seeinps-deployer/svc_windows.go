//go:build windows

package main

import (
	"fmt"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// createWindowsService 用 Windows 服务管理器注册 seeinps 服务。
// 通过 x/sys/windows/svc/mgr 的 CreateService 创建，可正确处理带空格路径与服务参数的
// BinaryPathName，避免手拼 sc 命令行时出现的引号/转义问题（ERROR_INVALID_COMMAND_LINE 1639）。
func createWindowsService(name, binPath, confPath string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("连接服务管理器失败: %v", err)
	}
	defer m.Disconnect()

	s, err := m.CreateService(
		name,
		binPath,
		mgr.Config{
			DisplayName:      "seeinps service",
			Description:      "seeinps 隧道客户端",
			StartType:        mgr.StartAutomatic,
			ServiceStartName: "LocalSystem",
		},
		"--service",
		"-conf",
		confPath,
	)
	if err != nil {
		return fmt.Errorf("创建服务失败: %v", err)
	}
	s.Close()
	return nil
}

func startWindowsService(name string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("连接服务管理器失败: %v", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("打开服务失败: %v", err)
	}
	defer s.Close()

	if err := s.Start(); err != nil {
		return fmt.Errorf("启动服务失败: %v", err)
	}
	return nil
}

func stopWindowsService(name string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("连接服务管理器失败: %v", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(name)
	if err != nil {
		// 服务不存在视为已停止
		return nil
	}
	defer s.Close()

	_, err = s.Control(svc.Stop)
	return err
}

func deleteWindowsService(name string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("连接服务管理器失败: %v", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(name)
	if err != nil {
		// 服务不存在视为已删除
		return nil
	}
	defer s.Close()

	_ = stopWindowsService(name)
	return s.Delete()
}

// svcExists 检测 Windows 服务是否已注册
func svcExists(name string) bool {
	m, err := mgr.Connect()
	if err != nil {
		return false
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return false
	}
	s.Close()
	return true
}
