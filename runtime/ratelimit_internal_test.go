package runtime

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/imohiyoko/API/port"
)

// 満タンでないバケツばかりでも、バケツの数は上限で止まる。
func TestMemoryRateLimiterIsBounded(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	l := NewMemoryRateLimiter(func() time.Time { return now })
	l.max = 10
	rate := port.Rate{Requests: 5, Per: time.Minute}
	for i := range 100 {
		if ok, _, _ := l.Allow(context.Background(), fmt.Sprintf("k%d", i), rate); !ok {
			t.Fatalf("first call of a new key must pass")
		}
		if len(l.buckets) > l.max {
			t.Fatalf("after %d keys: %d buckets, max %d", i+1, len(l.buckets), l.max)
		}
	}
	// 呼ばれ続けているキーは、上限に達するまで数え続ける。
	for range 4 {
		l.Allow(context.Background(), "k99", rate)
	}
	if ok, _, _ := l.Allow(context.Background(), "k99", rate); ok {
		t.Fatal("k99 must be limited after 5 calls")
	}
}
