package runtime

import (
	"context"
	"sync"
	"time"

	"github.com/imohiyoko/API/port"
)

// maxBuckets を超えたら、満タンに戻った（しばらく呼ばれていない）バケツを捨てる。
const maxBuckets = 100000

// MemoryRateLimiter はプロセス内のトークンバケット。期間あたり Requests 回まで、続けて Requests 回まで呼べる。
// プロセスごとに数えるので、複数のプロセスで動かすと上限はプロセスの数だけ増える。
type MemoryRateLimiter struct {
	now     func() time.Time
	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
	rate   port.Rate
}

// NewMemoryRateLimiter は MemoryRateLimiter を作る。now が nil なら time.Now。
func NewMemoryRateLimiter(now func() time.Time) *MemoryRateLimiter {
	if now == nil {
		now = time.Now
	}
	return &MemoryRateLimiter{now: now, buckets: map[string]*bucket{}}
}

// Allow は port.RateLimiter を満たす。
func (l *MemoryRateLimiter) Allow(_ context.Context, key string, rate port.Rate) (bool, time.Duration, error) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok || b.rate != rate {
		if len(l.buckets) >= maxBuckets {
			l.purge(now)
		}
		b = &bucket{tokens: float64(rate.Requests), last: now, rate: rate}
		l.buckets[key] = b
	}
	b.refill(now)
	if b.tokens >= 1 {
		b.tokens--
		return true, 0, nil
	}
	perToken := rate.Per / time.Duration(rate.Requests)
	wait := time.Duration((1 - b.tokens) * float64(perToken))
	return false, wait, nil
}

func (b *bucket) refill(now time.Time) {
	if el := now.Sub(b.last); el > 0 {
		b.tokens += el.Seconds() / b.rate.Per.Seconds() * float64(b.rate.Requests)
		if max := float64(b.rate.Requests); b.tokens > max {
			b.tokens = max
		}
		b.last = now
	}
}

func (l *MemoryRateLimiter) purge(now time.Time) {
	for k, b := range l.buckets {
		b.refill(now)
		if b.tokens >= float64(b.rate.Requests) {
			delete(l.buckets, k)
		}
	}
}
