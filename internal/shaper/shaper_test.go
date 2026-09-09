package shaper

import (
	"context"
	"testing"
	"time"
)

func TestUnlimited(t *testing.T) {
	l := New(0)
	start := time.Now()
	for i := 0; i < 100; i++ {
		if err := l.Wait(context.Background(), 1<<20); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Errorf("unlimited limiter should not block, took %v", d)
	}
}

func TestTokenBucketPacesBeyondBurst(t *testing.T) {
	// 100 KB/s：桶容量 1 秒突发（100 KB）。先耗尽突发额度，再请求 50 KB 应等待约 0.5s
	l := New(100_000)
	ctx := context.Background()
	if err := l.Wait(ctx, 100_000); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := l.Wait(ctx, 50_000); err != nil {
		t.Fatal(err)
	}
	d := time.Since(start)
	if d < 400*time.Millisecond || d > 1200*time.Millisecond {
		t.Errorf("expected ~500ms pacing, got %v", d)
	}
}

func TestSharedAcrossProxies(t *testing.T) {
	// 两个"代理"共用一个桶：各 60 KB（超过 1 秒容量的一半），合计 120 KB > 100 KB 容量 → 必有等待
	l := New(100_000)
	start := time.Now()
	_ = l.Wait(context.Background(), 60_000)
	_ = l.Wait(context.Background(), 60_000)
	if time.Since(start) < 150*time.Millisecond {
		t.Errorf("shared bucket should pace second borrower, elapsed %v", time.Since(start))
	}
}

func TestSetRateLive(t *testing.T) {
	l := New(1_000_000)
	l.SetRate(0)
	if l.Rate() != 0 {
		t.Fatal("rate not applied")
	}
	start := time.Now()
	_ = l.Wait(context.Background(), 10<<20)
	if time.Since(start) > 100*time.Millisecond {
		t.Error("unlimited after SetRate(0) should not block")
	}
	l.SetRate(-5) // 负数归零
	if l.Rate() != 0 {
		t.Error("negative rate should clamp to 0")
	}
}
