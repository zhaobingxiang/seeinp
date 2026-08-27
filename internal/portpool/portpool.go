package portpool

import (
	"fmt"
	"net"
	"sync"

	"github.com/seeinp/seeinp/internal/store"
)

type Pool struct {
	mu     sync.RWMutex
	ranges []Range
	store  *store.Store
}

type Range struct {
	Start int
	End   int
}

type Allocation struct {
	Port    int
	UserID  string
	ProxyID string
	Type    string
}

func New(ranges []Range, s *store.Store) *Pool {
	return &Pool{
		ranges: ranges,
		store:  s,
	}
}

func (p *Pool) Allocate(userID, proxyID, proxyType string, preferredPort *int) (*Allocation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Check if this proxyID already has an active allocation
	if existing, err := p.store.GetPortByProxyID(proxyID); err == nil {
		fmt.Printf("[POOL] Reusing port %d for proxy %s\n", existing.Port, proxyID)
		return &Allocation{
			Port:    existing.Port,
			UserID:  existing.UserID,
			ProxyID: existing.ProxyID,
			Type:    existing.ProxyType,
		}, nil
	}

	var port int
	if preferredPort != nil {
		if err := p.checkAvailable(*preferredPort); err != nil {
			return nil, err
		}
		port = *preferredPort
	} else {
		// 复用该代理的历史端口（含已释放）：见 inps 断线重连后对外端口保持稳定，
		// 避免 cleanup 释放 + 重连重分配导致端口漂移
		if last, err := p.store.GetLastPortByProxyID(proxyID); err == nil && !p.store.IsPortAllocated(last.Port) {
			if err := p.checkSystemPort(last.Port); err == nil {
				port = last.Port
				fmt.Printf("[POOL] Restoring port %d for proxy %s\n", port, proxyID)
			}
		}
		if port == 0 {
			var err error
			port, err = p.findAvailable()
			if err != nil {
				return nil, err
			}
		}
	}

	// 清理本代理的历史释放记录，再按端口 upsert（端口可能残留其他代理的释放行）
	if err := p.store.DeleteReleasedByProxyID(proxyID); err != nil {
		return nil, fmt.Errorf("cleanup released allocation: %w", err)
	}
	if err := p.store.UpsertPortAllocation(port, userID, proxyID, proxyType); err != nil {
		return nil, fmt.Errorf("store allocation: %w", err)
	}

	return &Allocation{
		Port:    port,
		UserID:  userID,
		ProxyID: proxyID,
		Type:    proxyType,
	}, nil
}

func (p *Pool) Release(proxyID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.store.ReleasePort(proxyID)
}

func (p *Pool) checkAvailable(port int) error {
	if p.store.IsPortAllocated(port) {
		return fmt.Errorf("port %d already allocated", port)
	}
	valid := false
	for _, r := range p.ranges {
		if port >= r.Start && port <= r.End {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("port %d not in valid range", port)
	}
	if err := p.checkSystemPort(port); err != nil {
		return fmt.Errorf("port %d not available: %w", port, err)
	}
	return nil
}

func (p *Pool) findAvailable() (int, error) {
	for _, r := range p.ranges {
		for port := r.Start; port <= r.End; port++ {
			if p.store.IsPortAllocated(port) {
				continue
			}
			if err := p.checkSystemPort(port); err == nil {
				return port, nil
			}
		}
	}
	return 0, fmt.Errorf("no available ports in pool")
}

func (p *Pool) checkSystemPort(port int) error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
	ln.Close()
	return nil
}

func (p *Pool) GetStats() (total, used int) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, r := range p.ranges {
		total += r.End - r.Start + 1
	}
	allocs, _ := p.store.GetAllocatedPorts()
	used = len(allocs)
	return
}
