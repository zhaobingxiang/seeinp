package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// PMConfig is the configuration for seeinpm
type PMConfig struct {
	Server   ServerConfig   `toml:"server"`
	TLS      TLSConfig      `toml:"tls"`
	Auth     PMAuthConfig   `toml:"auth"`
	Database DatabaseConfig `toml:"database"`
	Logging  LoggingConfig  `toml:"logging"`
	PortPool PortPoolConfig `toml:"port_pool"`
	Audit    AuditConfig    `toml:"audit"`
}

type ServerConfig struct {
	Addr     string `toml:"addr"`      // 控制通道监听地址（seeinps 拨入）
	HTTPAddr string `toml:"http_addr"` // B 端 Web/API 监听地址（空=默认 :90）
}

type TLSConfig struct {
	Enabled  bool   `toml:"enabled"`
	CertFile string `toml:"cert_file"`
	KeyFile  string `toml:"key_file"`
}

type PMAuthConfig struct {
	JWTSecret         string `toml:"jwt_secret"`
	JWTExpire         int    `toml:"jwt_expire"`
	JWTRefreshExpire  int    `toml:"jwt_refresh_expire"`
}

type DatabaseConfig struct {
	Path string `toml:"path"`
}

type LoggingConfig struct {
	Level      string `toml:"level"`
	Path       string `toml:"path"`
	MaxSize    int    `toml:"max_size"`
	MaxBackups int    `toml:"max_backups"`
}

// AuditConfig 审计行为配置。
type AuditConfig struct {
	// FailClosed 为 true 时，不可逆/高敏感操作（删除用户、重置授权码、删除升级包等）
	// 改为"先写审计再执行"：审计落库失败则直接拒绝该操作，避免出现"动作成功但无审计"。
	// 默认 false（best-effort：审计失败记 ERROR，业务继续）。
	FailClosed bool `toml:"fail_closed"`
}

type PortPoolConfig struct {
	Ranges []PortRange `toml:"ranges"`
}

type PortRange struct {
	Start int `toml:"start"`
	End   int `toml:"end"`
}

// LoadPMConfig loads seeinpm configuration from file
func LoadPMConfig(path string) (*PMConfig, error) {
	config := &PMConfig{
		Server: ServerConfig{Addr: ":99", HTTPAddr: ":90"},
		TLS:    TLSConfig{Enabled: true},
		Auth: PMAuthConfig{
			JWTExpire:        1800,
			JWTRefreshExpire: 86400,
		},
		Database: DatabaseConfig{Path: "data/seeinpm.db"},
		Logging: LoggingConfig{
			Level:      "info",
			Path:       "logs",
			MaxSize:    100,
			MaxBackups: 7,
		},
		PortPool: PortPoolConfig{
			Ranges: []PortRange{{Start: 20000, End: 30000}},
		},
	}

	if path != "" {
		if _, err := toml.DecodeFile(path, config); err != nil {
			return nil, fmt.Errorf("decode config: %w", err)
		}
	}

	// Auto-generate JWT secret if empty
	if config.Auth.JWTSecret == "" {
		secret, err := generateRandomHex(32)
		if err != nil {
			return nil, fmt.Errorf("generate jwt secret: %w", err)
		}
		config.Auth.JWTSecret = secret
	}

	// Create directories
	if err := os.MkdirAll(config.Database.Path[:len(config.Database.Path)-len("seeinpm.db")], 0755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	if err := os.MkdirAll(config.Logging.Path, 0755); err != nil {
		return nil, fmt.Errorf("create logs dir: %w", err)
	}

	return config, nil
}

func generateRandomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
