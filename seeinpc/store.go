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

// ---------------- 未知字段保留 ----------------
// Go 的结构体序列化会静默丢弃它不认识的键：旧版本实例写回配置时，
// 会把新版本加入的字段抹掉（settings.servers 代理服务器池即曾被 0905 旧实例写丢）。
// 读取时保留原始 JSON 快照，写回时"已知字段以当前为准、未知键原样保留"地合并。
var (
	rawTop      map[string]json.RawMessage
	rawSettings map[string]json.RawMessage
	rawEntries  map[string]json.RawMessage // key = entry id，值含该条目的全部原始键
)

func clearRaw() { rawTop, rawSettings, rawEntries = nil, nil, nil }

func captureRaw(data []byte) {
	clearRaw()
	var top map[string]json.RawMessage
	if json.Unmarshal(data, &top) != nil {
		return
	}
	rawTop = top
	if b, ok := top["settings"]; ok {
		var s map[string]json.RawMessage
		if json.Unmarshal(b, &s) == nil {
			rawSettings = s
		}
	}
	if b, ok := top["entries"]; ok {
		var arr []json.RawMessage
		if json.Unmarshal(b, &arr) == nil {
			rawEntries = map[string]json.RawMessage{}
			for _, it := range arr {
				var idf struct {
					ID string `json:"id"`
				}
				if json.Unmarshal(it, &idf) == nil && idf.ID != "" {
					rawEntries[idf.ID] = it
				}
			}
		}
	}
}

func mergeJSON(base, cur map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(base)+len(cur))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range cur {
		out[k] = v
	}
	return out
}

// serversKeyMissing 磁盘上已有 settings 但缺 servers 键——历史上被不认识该字段的
// 旧版本实例写回的典型特征（缺文件时返回 false，留给首次保存自然落盘）。
func serversKeyMissing() bool {
	configMu.Lock()
	defer configMu.Unlock()
	if rawSettings == nil {
		return false
	}
	_, ok := rawSettings["servers"]
	return !ok
}

// loadConfig 读取配置（不存在返回默认配置）。
// 返回的 warnings 交给调用方按 WARN 级记录——尤其是 DPAPI 解密失败：
// 早期实现静默把密码置空，用户只看到卡片变成"待填密码"，日志里毫无线索。
func loadConfig() (*Config, []string, error) {
	configMu.Lock()
	defer configMu.Unlock()
	cfg := &Config{Settings: defaultSettings()}
	data, err := os.ReadFile(confPath())
	if err != nil {
		clearRaw()
		if os.IsNotExist(err) {
			return cfg, nil, nil
		}
		return nil, nil, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, nil, fmt.Errorf("配置文件损坏: %w", err)
	}
	captureRaw(data)
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

// saveConfig 原子写入配置（口令先 DPAPI 加密）。
// 有原始快照时按"未知字段保留"合并写回：本版本认识的键以当前值为准，
// 不认识的历史键原样保留，防止跨版本互相抹字段。
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

	if rawTop == nil {
		// 无原始树（首次写盘/加载失败后）：按当前结构体全量写
		data, err := json.MarshalIndent(clone, "", "  ")
		if err != nil {
			return err
		}
		return writeConfigAtomic(data)
	}

	setJSON, err := json.Marshal(clone.Settings)
	if err != nil {
		return err
	}
	var setCur map[string]json.RawMessage
	_ = json.Unmarshal(setJSON, &setCur)
	setMerged := mergeJSON(rawSettings, setCur)
	sb, err := json.Marshal(setMerged)
	if err != nil {
		return err
	}

	arr := make([]json.RawMessage, 0, len(clone.Entries))
	nextEntries := map[string]json.RawMessage{}
	for _, e := range clone.Entries {
		eb, err := json.Marshal(e)
		if err != nil {
			return err
		}
		merged := eb
		var eCur map[string]json.RawMessage
		_ = json.Unmarshal(eb, &eCur)
		if old, ok := rawEntries[e.ID]; ok {
			var eOld map[string]json.RawMessage
			if json.Unmarshal(old, &eOld) == nil {
				if mb, merr := json.Marshal(mergeJSON(eOld, eCur)); merr == nil {
					merged = mb
				}
			}
		}
		arr = append(arr, merged)
		nextEntries[e.ID] = merged
	}

	top := map[string]json.RawMessage{}
	for k, v := range rawTop {
		top[k] = v
	}
	top["settings"] = sb
	entriesJSON, err := json.Marshal(arr)
	if err != nil {
		return err
	}
	top["entries"] = entriesJSON
	if len(clone.IgnoredVersions) > 0 {
		iv, err := json.Marshal(clone.IgnoredVersions)
		if err != nil {
			return err
		}
		top["ignoredVersions"] = iv
	} else {
		delete(top, "ignoredVersions")
	}
	data, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return err
	}
	if err := writeConfigAtomic(data); err != nil {
		return err
	}
	// 写盘成功的内容成为下次合并基准（含本轮新保留的未知键）
	rawTop, rawSettings, rawEntries = top, setMerged, nextEntries
	return nil
}

func writeConfigAtomic(data []byte) error {
	if err := os.MkdirAll(filepath.Dir(confPath()), 0o755); err != nil {
		return err
	}
	tmp := confPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, confPath())
}
