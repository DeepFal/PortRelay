package traffic

import (
	"sync"
	"testing"
	"time"
)

func TestLimiterDoesNotSerializeCallers(t *testing.T) {
	// 1 MB/s，初始令牌 1 MB；两个调用方各取 1 MB，总耗时应约 1 秒而不是被锁串行化
	l := NewLimiter(1)
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.Allow(1024 * 1024)
		}()
	}
	wg.Wait()
	if d := time.Since(start); d < 800*time.Millisecond || d > 1500*time.Millisecond {
		t.Fatalf("expected ~1s total wait, got %v", d)
	}
}

func TestNilLimiter(t *testing.T) {
	var l *Limiter
	l.Allow(1 << 20)
	if l.MBPerSec() != 0 {
		t.Fatal("nil limiter should report 0")
	}
	if NewLimiter(0) != nil {
		t.Fatal("NewLimiter(0) should be nil (unlimited)")
	}
}
