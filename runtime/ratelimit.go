package runtime

import (
	"context"
	"sync"
	"time"

	"github.com/imohiyoko/API/port"
)

// maxBuckets に達したら、満タンに戻った（しばらく呼ばれていない）バケツを捨てる。
// 捨てられるものがなければ、任意のバケツを1つ捨ててから新しいキーを入れる。
// 捨てられたキーは満タンからやり直すので、そのぶん上限をわずかに超えうるが、メモリは maxBuckets で止まる。
const maxBuckets = 100000

// purgeEvery は全バケツを走査する掃除の最短間隔。満杯が続いても新しいキーごとに走査しない。
const purgeEvery = time.Second

// MemoryRateLimiter はプロセス内のトークンバケット。期間あたり Requests 回まで、続けて Requests 回まで呼べる。
// プロセスごとに数えるので、複数のプロセスで動かすと上限はプロセスの数だけ増える。
type MemoryRateLimiter struct {
	now       func() time.Time
	mu        sync.Mutex
	buckets   map[string]*bucket
	lastPurge time.Time
	max       int
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
	return &MemoryRateLimiter{now: now, buckets: map[string]*bucket{}, max: maxBuckets}
}

// Allow は port.RateLimiter を満たす。
func (l *MemoryRateLimiter) Allow(_ context.Context, key string, rate port.Rate) (bool, time.Duration, error) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok || b.rate != rate {
		if !ok {
			l.makeRoom(now)
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

// makeRoom はバケツの数を max 未満にする。
func (l *MemoryRateLimiter) makeRoom(now time.Time) {
	if len(l.buckets) < l.max {
		return
	}
	if now.Sub(l.lastPurge) >= purgeEvery || now.Before(l.lastPurge) {
		l.lastPurge = now
		l.purge(now)
	}
	for k := range l.buckets {
		if len(l.buckets) < l.max {
			break
		}
		delete(l.buckets, k)
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
