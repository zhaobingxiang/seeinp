package main

// saveConfig"未知字段保留"合并写回回归测试：
// 模拟旧版本实例写盘（其结构体没有 servers 字段）与新版本读写的场景，
// 确保本版本不认识的历史键/未来键不被抹掉。测试在 go test 临时构建目录
// 的 conf/ 下读写（confPath 基于 os.Executable），不触碰任何真实安装。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func rawFileSettings(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(confPath())
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	var s map[string]json.RawMessage
	if err := json.Unmarshal(top["settings"], &s); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}
	return s
}

func TestSaveConfigPreservesUnknownKeys(t *testing.T) {
	// 预置一份带"当前版本不认识"的键的配置（settings/顶层/entry 三层都有）
	seed := `{
	  "settings": {"probeIntervalSec": 45, "minimizeToTray": true, "logLevel": "info",
	               "servers": [{"name":"主","addr":"a.example.com"}], "futureSetting": {"x": 1}},
	  "entries": [{"id": "e1", "name": "旧卡", "opsId": 20001, "proxyUser": "u",
	               "acl": ["10.0.0.0/24"], "futureEntryKey": "keep-me"}],
	  "futureTopKey": [1, 2, 3]
	}`
	if err := os.MkdirAll(filepath.Dir(confPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(confPath(), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(cfg.Settings.Servers) != 1 || cfg.Settings.Servers[0].Addr != "a.example.com" {
		t.Fatalf("servers lost on load: %+v", cfg.Settings.Servers)
	}

	// 新版本改动其他字段后保存：未知键必须还在，已知键以新值为准
	cfg.Settings.ProbeIntervalSec = 60
	cfg.Settings.Servers = nil // 模拟用户清空 → 下次加载应回默认，但文件里 servers 键本身可被覆盖
	if err := saveConfig(cfg); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	final, err := os.ReadFile(confPath())
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(final, &top); err != nil {
		t.Fatal(err)
	}
	if _, ok := top["futureTopKey"]; !ok {
		t.Error("top-level unknown key futureTopKey was dropped")
	}
	set := rawFileSettings(t)
	if _, ok := set["futureSetting"]; !ok {
		t.Error("settings unknown key futureSetting was dropped")
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(top["entries"], &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries count changed: %d", len(entries))
	}
	if _, ok := entries[0]["futureEntryKey"]; !ok {
		t.Error("entry unknown key futureEntryKey was dropped")
	}
	var pin struct {
		ProbeIntervalSec int `json:"probeIntervalSec"`
	}
	_ = json.Unmarshal(top["settings"], &pin)
	if pin.ProbeIntervalSec != 60 {
		t.Errorf("known field not updated: probeIntervalSec=%d", pin.ProbeIntervalSec)
	}

	// 二次加载-保存：上轮保留下来的未知键仍要继续存在（基准更新正确）
	cfg2, _, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(cfg2); err != nil {
		t.Fatal(err)
	}
	set2 := rawFileSettings(t)
	if _, ok := set2["futureSetting"]; !ok {
		t.Error("futureSetting dropped on second save")
	}
}

func TestSaveConfigNoRawWritesFull(t *testing.T) {
	// 文件不存在 → 首次全量写：结构体所有键齐全
	if err := os.Remove(confPath()); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	cfg, _, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Settings.Servers = defaultServers()
	if err := saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	set := rawFileSettings(t)
	if _, ok := set["servers"]; !ok {
		t.Error("servers key missing on first full write")
	}
}

func TestServersKeyMissingSelfHeal(t *testing.T) {
	// 模拟旧版本写回后的文件：settings 无 servers 键
	seed := `{"settings":{"probeIntervalSec":30,"minimizeToTray":true,"logLevel":"info"},"entries":[]}`
	if err := os.MkdirAll(filepath.Dir(confPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(confPath(), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !serversKeyMissing() {
		t.Fatal("serversKeyMissing should be true on legacy file")
	}
	// 自愈动作：保存一次 → 键显式落盘，检测复位
	if err := saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := rawFileSettings(t)["servers"]; !ok {
		t.Error("self-heal save did not persist servers key")
	}
	if _, _, err := loadConfig(); err != nil {
		t.Fatal(err)
	}
	if serversKeyMissing() {
		t.Error("serversKeyMissing should be false after heal")
	}

	// 键存在时不应触发
	if err := saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadConfig(); err != nil {
		t.Fatal(err)
	}
	if serversKeyMissing() {
		t.Error("false positive: file has servers key")
	}
}
