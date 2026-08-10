package process_test

import (
	"sync"
	"testing"

	"github.com/rvben/shinyhub/internal/process"
)

// TestGopsutilSamplerConcurrentSamplesAreSafe covers two callers sampling the
// same replica at once, which the API handler and the metrics-history collector
// do whenever their timers coincide. gopsutil's Percent reads and then
// overwrites the previous CPU times stored on the handle, so an unsynchronised
// pair corrupts the baseline the next rate is measured against. Run under -race.
func TestGopsutilSamplerConcurrentSamplesAreSafe(t *testing.T) {
	pgid := startIdleParentBusyChild(t)

	var s process.GopsutilSampler
	handle := process.RunHandle{PID: pgid}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				if _, err := s.Sample(handle); err != nil {
					t.Errorf("concurrent sample: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
