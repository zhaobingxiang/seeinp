package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/seeinp/seeinp/internal/logx"
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

// loadConfig 读取配置（不存在返回默认配置）。
// 返回的 warnings 交给调用方按 WARN 级记录——尤其是 DPAPI 解密失败：
// 早期实现静默把密码置空，用户只看到卡片变成"待填密码"，日志里毫无线索。
func loadConfig() (*Config, []string, error) {
	configMu.Lock()
	defer configMu.Unlock()
	cfg := &Config{Settings: defaultSettings()}
	data, err := os.ReadFile(confPath())
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil, nil
		}
		return nil, nil, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, nil, fmt.Errorf("配置文件损坏: %w", err)
	}
	if cfg.Settings.ProbeIntervalSec <= 0 {
		cfg.Settings.ProbeIntervalSec = 30
	}
	if _, ok := logx.ParseLevel(cfg.Settings.LogLevel); !ok {
		cfg.Settings.LogLevel = defaultSettings().LogLevel
	}
	// 解密口令字段 + 清洗危险网段（旧配置可能含 0.0.0.0/0）
	var warnings []string
	for _, e := range cfg.Entries {
		if e.ProxyPass != "" {
			if plain, derr := decryptString(e.ProxyPass); derr == nil {
				e.ProxyPass = plain
			} else {
				// 换机/换用户后 DPAPI 密钥不同，密文无法解开：置空并留下明确告警
				e.ProxyPass = ""
				warnings = append(warnings, fmt.Sprintf(
					"entry %q (%s) proxy password could not be decrypted (%v); cleared, please re-enter it",
					e.Name, e.ID, derr))
			}
		}
		before := len(e.ACL)
		e.ACL = sanitizeACL(e.ACL)
		if dropped := before - len(e.ACL); dropped > 0 {
			warnings = append(warnings, fmt.Sprintf(
				"entry %q: dropped %d unsafe/invalid ACL entr%s (0.0.0.0/0 or non-IPv4)",
				e.Name, dropped, map[bool]string{true: "y", false: "ies"}[dropped == 1]))
		}
	}
	return cfg, warnings, nil
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
