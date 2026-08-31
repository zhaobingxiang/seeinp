package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// PSConfig is the configuration for seeinps
type PSConfig struct {
	Server  PSServerConfig `toml:"server"`
	Auth    PSAuthConfig   `toml:"auth"`
	TLS     PSTLSConfig    `toml:"tls"`
	Local   PSLocalConfig  `toml:"local"`
	Logging LoggingConfig  `toml:"logging"`
}

type PSServerConfig struct {
	ServerAddr string `toml:"server_addr"`
}

type PSAuthConfig struct {
	Username string `toml:"username"`
	AuthCode string `toml:"auth_code"`
}

type PSTLSConfig struct {
	Enabled    bool `toml:"enabled"`
	SkipVerify bool `toml:"skip_verify"`
}

type PSLocalConfig struct {
	BendAddr string `toml:"bend_addr"`
}

// LoadPSConfig loads seeinps configuration from file
func LoadPSConfig(path string) (*PSConfig, error) {
	config := &PSConfig{
		Server: PSServerConfig{ServerAddr: "127.0.0.1:99"},
		TLS:    PSTLSConfig{Enabled: true},
		Local:  PSLocalConfig{BendAddr: ":65443"},
		Logging: LoggingConfig{
			Level:      "info",
			Path:       "logs",
			MaxSize:    100,
			MaxBackups: 7,
		},
	}

	if path != "" {
		if _, err := toml.DecodeFile(path, config); err != nil {
			return nil, fmt.Errorf("decode config: %w", err)
		}
	}

	// Create directories
	if err := os.MkdirAll(config.Logging.Path, 0755); err != nil {
		return nil, fmt.Errorf("create logs dir: %w", err)
	}

	return config, nil
}

// GenerateAuthCode generates a random auth code
func GenerateAuthCode() (string, error) {
	b := make([]byte, 32) // 256 bits
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "seeinp_" + hex.EncodeToString(b), nil
}

