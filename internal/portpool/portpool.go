package portpool

import (
	"fmt"
	"net"
	"sync"

	"github.com/seeinp/seeinp/internal/logx"
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

// SetRanges 运行时更新端口池范围（web 配置热生效，带锁）
func (p *Pool) SetRanges(ranges []Range) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ranges = append([]Range(nil), ranges...)
}

// GetRanges 返回当前端口池范围副本
func (p *Pool) GetRanges() []Range {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]Range(nil), p.ranges...)
}

// Contains 判断端口是否在当前池内
func (p *Pool) Contains(port int) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.containsLocked(port)
}

// containsLocked 无锁内部版：调用方需持有 p.mu
func (p *Pool) containsLocked(port int) bool {
	for _, r := range p.ranges {
		if port >= r.Start && port <= r.End {
			return true
		}
	}
	return false
}

// inRanges 判断端口是否在给定范围列表内；ranges 为空表示不限制
func inRanges(port int, ranges []Range) bool {
	if len(ranges) == 0 {
		return true
	}
	for _, r := range ranges {
		if port >= r.Start && port <= r.End {
			return true
		}
	}
	return false
}

// Allocate 分配端口；userRanges 非空时（用户端口池），候选端口必须同时落在
// 用户范围与全局池的交集中，历史端口复用同样受此约束
func (p *Pool) Allocate(userID, proxyID, proxyType string, preferredPort *int, userRanges []Range) (*Allocation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Check if this (user, proxy) already has an active allocation.
	// 复合键定位：proxyID 并非全局唯一（不同用户的 web-ui 同名），只按 proxyID 会误复用他人端口
	if existing, err := p.store.GetPortByUserProxy(userID, proxyID); err == nil {
		logx.Debugf("[POOL] reusing port: port=%d user=%s proxy=%s", existing.Port, userID, proxyID)
		return &Allocation{
			Port:    existing.Port,
			UserID:  existing.UserID,
			ProxyID: existing.ProxyID,
			Type:    existing.ProxyType,
		}, nil
	}

	var port int
	if preferredPort != nil {
		if err := p.checkAvailable(*preferredPort, proxyType); err != nil {
			return nil, err
		}
		if !inRanges(*preferredPort, userRanges) {
			return nil, fmt.Errorf("port %d outside user pool", *preferredPort)
		}
		port = *preferredPort
	} else {
		// 复用该代理的历史端口（含已释放）：见 inps 断线重连后对外端口保持稳定，
		// 避免 cleanup 释放 + 重连重分配导致端口漂移。
		// 注意：历史端口必须仍在新池内（端口池改小后池外端口不再复用，强制回池内重新分配）
		if last, err := p.store.GetLastPortByUserProxy(userID, proxyID); err == nil && !p.store.IsPortAllocated(last.Port) {
			if p.containsLocked(last.Port) {
				if inRanges(last.Port, userRanges) {
					if err := p.checkSystemPort(last.Port, proxyType); err == nil {
						port = last.Port
						logx.Infof("[POOL] restoring port: port=%d proxy=%s", port, proxyID)
					}
				} else {
					logx.Infof("[POOL] historical port outside user pool, re-allocating: port=%d proxy=%s", last.Port, proxyID)
				}
			} else {
				logx.Infof("[POOL] historical port outside current pool, re-allocating: port=%d proxy=%s", last.Port, proxyID)
			}
		}
		if port == 0 {
			var err error
			port, err = p.findAvailable(userRanges, proxyType)
			if err != nil {
				return nil, err
			}
		}
	}

	// 清理本代理的历史释放记录，再按端口 upsert（端口可能残留其他代理的释放行）
	if err := p.store.DeleteReleasedByUserProxy(userID, proxyID); err != nil {
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

func (p *Pool) Release(userID, proxyID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.store.ReleasePortByUserProxy(userID, proxyID)
}

func (p *Pool) checkAvailable(port int, proxyType string) error {
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
	if err := p.checkSystemPort(port, proxyType); err != nil {
		return fmt.Errorf("port %d not available: %w", port, err)
	}
	return nil
}

func (p *Pool) findAvailable(userRanges []Range, proxyType string) (int, error) {
	for _, r := range p.ranges {
		for port := r.Start; port <= r.End; port++ {
			if !inRanges(port, userRanges) {
				continue
			}
			if p.store.IsPortAllocated(port) {
				continue
			}
			if err := p.checkSystemPort(port, proxyType); err == nil {
				return port, nil
			}
		}
	}
	return 0, fmt.Errorf("no available ports in pool")
}

// checkSystemPort 探测端口在系统中是否可绑定；UDP 代理按 UDP 探测，其余按 TCP。
func (p *Pool) checkSystemPort(port int, proxyType string) error {
	if proxyType == "udp" {
		pc, err := net.ListenUDP("udp", &net.UDPAddr{Port: port})
		if err != nil {
			return err
		}
		pc.Close()
		return nil
	}
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
