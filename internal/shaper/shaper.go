// Package shaper 提供按用户共享的令牌桶限速器：
// 一个用户的所有一代理共用同一只桶，双向（读+写）合计消耗令牌。
// bps=0 表示不限速；桶容量固定为 1 秒流量，空闲不攒突发额度，
// 语义贴近路由器 QoS 的“平均带宽封顶”，实现与依赖同为零。
package shaper

import (
	"context"
	"sync"
	"time"
)

const ChunkSize = 32 * 1024 // 单次最大取令牌字节数，同时也是转发拷贝的分块大小

type bucket struct {
	mu     sync.Mutex
	tokens float64 // 当前令牌（字节），可为负（欠账，等待回填）
	max    float64
	last   time.Time
}

// Limiter 每用户一个；SetRate 支持运行期改限速。
type Limiter struct {
	mu       sync.Mutex
	bps      float64 // 每秒字节数，0=不限
	b        *bucket
}

// New 创建限速器；bps=0 表示不限速。
func New(bps int64) *Limiter {
	l := &Limiter{}
	l.SetRate(bps)
	return l
}

// SetRate 修改速率（字节/秒；0=不限）。重建令牌桶，进行中的 Wait 会按新速率重新排程。
func (l *Limiter) SetRate(bps int64) {
	if bps < 0 {
		bps = 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.bps = float64(bps)
	l.b = &bucket{last: time.Now()}
	if bps > 0 {
		l.b.max = float64(bps)
		l.b.tokens = l.b.max
	}
}

// Rate 返回当前速率（字节/秒，0=不限），主要供测试与观测。
func (l *Limiter) Rate() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return int64(l.bps)
}

// Active 表示是否处于限速状态（bps>0）。数据面据此决定走限速拷贝还是直通 io.Copy。
func (l *Limiter) Active() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.bps > 0
}

// Wait 消耗 n 字节令牌，速率不足时阻塞等待；ctx 取消则提前返回 err。
// 无限速时立即返回。大请求按 chunk 分片消耗，避免单次长阻塞。
func (l *Limiter) Wait(ctx context.Context, n int64) error {
	l.mu.Lock()
	bps, b := l.bps, l.b
	l.mu.Unlock()
	if bps <= 0 || n <= 0 || b == nil {
		return nil
	}
	for n > 0 {
		c := n
		if c > ChunkSize {
			c = ChunkSize
		}
		if err := l.waitChunk(ctx, b, float64(c), bps); err != nil {
			return err
		}
		n -= c
	}
	return nil
}

func (l *Limiter) waitChunk(ctx context.Context, b *bucket, need, bps float64) error {
	b.mu.Lock()
	now := time.Now()
	b.tokens += now.Sub(b.last).Seconds() * bps
	if b.tokens > b.max {
		b.tokens = b.max
	}
	b.last = now
	b.tokens -= need
	if b.tokens >= 0 {
		b.mu.Unlock()
		return nil
	}
	wait := time.Duration(-b.tokens / bps * float64(time.Second))
	b.mu.Unlock()
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
