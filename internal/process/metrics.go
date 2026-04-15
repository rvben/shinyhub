package process

import (
	"fmt"

	gops "github.com/shirou/gopsutil/v4/process"
)

// Stats holds a point-in-time resource snapshot for one process.
type Stats struct {
	CPUPercent float64
	RSSBytes   int64
}

// Sampler reads CPU and memory stats for a given PID.
// The interface exists so tests can inject a fake without a live process.
type Sampler interface {
	Sample(pid int) (Stats, error)
}

// GopsutilSampler is the production Sampler that reads from the OS via gopsutil.
type GopsutilSampler struct{}

// Sample returns CPU% and RSS for the process with the given PID.
// On the first call for a PID, CPUPercent is 0.0 (gopsutil needs two
// measurements to compute a delta). Subsequent calls return the real value.
func (GopsutilSampler) Sample(pid int) (Stats, error) {
	p, err := gops.NewProcess(int32(pid))
	if err != nil {
		return Stats{}, fmt.Errorf("process %d not found: %w", pid, err)
	}
	cpu, err := p.CPUPercent()
	if err != nil {
		return Stats{}, fmt.Errorf("cpu percent: %w", err)
	}
	mem, err := p.MemoryInfo()
	if err != nil {
		return Stats{}, fmt.Errorf("memory info: %w", err)
	}
	return Stats{
		CPUPercent: cpu,
		RSSBytes:   int64(mem.RSS),
	}, nil
}
