// Package throttle 提供 IO 限速（令牌桶），避免长扫描打满 NAS 磁盘。
package throttle

import (
	"sync"
	"time"
)

type Bucket struct {
	mu       sync.Mutex
	rate     float64 // bytes per second
	capacity float64
	tokens   float64
	last     time.Time
}

func New(rateMBPerSec float64) *Bucket {
	rate := rateMBPerSec * 1024 * 1024
	return &Bucket{rate: rate, capacity: rate * 4, tokens: rate * 4, last: time.Now()}
}

func (b *Bucket) Enabled() bool { return b != nil && b.rate > 0 }

func (b *Bucket) Acquire(n int) {
	if !b.Enabled() || n <= 0 {
		return
	}
	need := float64(n)
	for {
		b.mu.Lock()
		now := time.Now()
		if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
			b.tokens += elapsed * b.rate
			if b.tokens > b.capacity {
				b.tokens = b.capacity
			}
			b.last = now
		}
		if b.tokens >= need {
			b.tokens -= need
			b.mu.Unlock()
			return
		}
		missing := need - b.tokens
		wait := missing / b.rate
		if wait > 0.25 {
			wait = 0.25
		}
		b.mu.Unlock()
		time.Sleep(time.Duration(wait * float64(time.Second)))
	}
}

var (
	mu      sync.RWMutex
	current = &Bucket{}
)

// SetLimit 设置全局读取限速（MB/s，0 表示不限）。
func SetLimit(mbPerSec float64) {
	mu.Lock()
	current = New(mbPerSec)
	mu.Unlock()
}

func Get() *Bucket {
	mu.RLock()
	defer mu.RUnlock()
	return current
}
