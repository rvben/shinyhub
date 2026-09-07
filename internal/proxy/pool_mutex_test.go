package proxy

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
)

func TestPoolMutexExcludesRoutingAndAccountingReaders(t *testing.T) {
	var mu poolMutex
	var value, checksum int
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 1000 {
				var unlock func()
				if i%2 == 0 {
					stripe := mu.accountingRLock("app", fmt.Sprint(i))
					unlock = stripe.RUnlock
				} else {
					mu.RLock()
					unlock = mu.RUnlock
				}
				if value != checksum {
					t.Error("reader observed a partial lifecycle mutation")
				}
				unlock()
			}
		}()
	}
	close(start)
	for i := range 1000 {
		mu.Lock()
		value = i
		runtime.Gosched()
		checksum = i
		mu.Unlock()
	}
	wg.Wait()
}

func BenchmarkPoolMutexLifecycle(b *testing.B) {
	var mu poolMutex
	for b.Loop() {
		mu.Lock()
		mu.Unlock()
	}
}
