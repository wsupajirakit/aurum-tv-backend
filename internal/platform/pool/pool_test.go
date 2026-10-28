package pool

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestRunBoundsConcurrencyAndKeepsOrder(t *testing.T) {
	var active, peak atomic.Int32
	out := Run([]int{1, 2, 3, 4, 5, 6, 7}, 3, func(n int) int {
		cur := active.Add(1)
		for {
			p := peak.Load()
			if cur <= p || peak.CompareAndSwap(p, cur) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		active.Add(-1)
		return n * 10
	})
	if peak.Load() != 3 {
		t.Fatalf("peak %d", peak.Load())
	}
	for i, v := range out {
		if v != (i+1)*10 {
			t.Fatalf("order %v", out)
		}
	}
}
