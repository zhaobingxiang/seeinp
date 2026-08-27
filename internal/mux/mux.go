package mux

import (
	"fmt"
	"io"
	"net"
	"time"

	"github.com/hashicorp/yamux"
)

// Config is the configuration for yamux session
type Config struct {
	KeepAliveInterval int  // seconds
	MaxStreamWindow   uint32
}

// DefaultConfig returns default yamux configuration
func DefaultConfig() *Config {
	return &Config{
		KeepAliveInterval: 10,
		MaxStreamWindow:   4 * 1024 * 1024, // 4MB
	}
}

// Server wraps a net.Conn and creates a yamux session for server side
func Server(conn net.Conn, config *Config) (*yamux.Session, error) {
	cfg := yamux.DefaultConfig()
	cfg.KeepAliveInterval = time.Duration(config.KeepAliveInterval) * time.Second
	cfg.ConnectionWriteTimeout = 30 * time.Second
	cfg.MaxStreamWindowSize = config.MaxStreamWindow

	session, err := yamux.Server(conn, cfg)
	if err != nil {
		return nil, fmt.Errorf("create yamux server: %w", err)
	}

	return session, nil
}

// Client wraps a net.Conn and creates a yamux session for client side
func Client(conn net.Conn, config *Config) (*yamux.Session, error) {
	cfg := yamux.DefaultConfig()
	cfg.KeepAliveInterval = time.Duration(config.KeepAliveInterval) * time.Second
	cfg.ConnectionWriteTimeout = 30 * time.Second
	cfg.MaxStreamWindowSize = config.MaxStreamWindow

	session, err := yamux.Client(conn, cfg)
	if err != nil {
		return nil, fmt.Errorf("create yamux client: %w", err)
	}

	return session, nil
}

// Copy copies data bidirectionally between two connections
func Copy(dst io.Writer, src io.Reader) (int64, error) {
	return io.Copy(dst, src)
}
