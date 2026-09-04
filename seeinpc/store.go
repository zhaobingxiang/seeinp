package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// 路径约定（§9.3）：安装目录下 conf/ logs/ resource/
func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

func confPath() string    { return filepath.Join(exeDir(), "conf", "config.json") }
func logsDir() string     { return filepath.Join(exeDir(), "logs") }
func resourceDir() string { return filepath.Join(exeDir(), "resource") }

var configMu sync.Mutex

// loadConfig 读取配置（不存在返回默认配置）
func loadConfig() (*Config, error) {
	configMu.Lock()
	defer configMu.Unlock()
	cfg := &Config{Settings: defaultSettings()}
	data, err := os.ReadFile(confPath())
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("配置文件损坏: %w", err)
	}
	if cfg.Settings.ProbeIntervalSec <= 0 {
		cfg.Settings.ProbeIntervalSec = 30
	}
	// 解密口令字段 + 清洗危险网段（旧配置可能含 0.0.0.0/0）
	for _, e := range cfg.Entries {
		if e.ProxyPass != "" {
			if plain, err := decryptString(e.ProxyPass); err == nil {
				e.ProxyPass = plain
			} else {
				e.ProxyPass = "" // 解密失败视为未设置（换机场景）
			}
		}
		e.ACL = sanitizeACL(e.ACL)
	}
	return cfg, nil
}

// saveConfig 原子写入配置（口令先 DPAPI 加密）
func saveConfig(cfg *Config) error {
	configMu.Lock()
	defer configMu.Unlock()
	clone := &Config{Settings: cfg.Settings}
	for _, e := range cfg.Entries {
		cp := *e
		if cp.ProxyPass != "" {
			enc, err := encryptString(cp.ProxyPass)
			if err != nil {
				return fmt.Errorf("加密代理密码失败: %w", err)
			}
			cp.ProxyPass = enc
		}
		clone.Entries = append(clone.Entries, &cp)
	}
	data, err := json.MarshalIndent(clone, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(confPath()), 0o755); err != nil {
		return err
	}
	tmp := confPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, confPath())
}
