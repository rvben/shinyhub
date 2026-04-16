package process_test

import (
	"errors"
	"testing"

	"github.com/rvben/shinyhub/internal/process"
)

// fakeSampler implements process.Sampler for tests.
type fakeSampler struct {
	stats process.Stats
	err   error
}

func (f fakeSampler) Sample(_ process.RunHandle) (process.Stats, error) {
	return f.stats, f.err
}

func TestSamplerInterface(t *testing.T) {
	// Verify fakeSampler satisfies the Sampler interface at compile time.
	var _ process.Sampler = fakeSampler{}
}

func TestSampler_ReturnsStats(t *testing.T) {
	s := fakeSampler{stats: process.Stats{CPUPercent: 3.14, RSSBytes: 1 << 20}}
	got, err := s.Sample(process.RunHandle{PID: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.CPUPercent != 3.14 {
		t.Errorf("CPUPercent = %v, want 3.14", got.CPUPercent)
	}
	if got.RSSBytes != 1<<20 {
		t.Errorf("RSSBytes = %v, want %v", got.RSSBytes, 1<<20)
	}
}

func TestSampler_ReturnsError(t *testing.T) {
	s := fakeSampler{err: errors.New("process gone")}
	_, err := s.Sample(process.RunHandle{PID: 99999})
	if err == nil {
		t.Error("expected error, got nil")
	}
}
