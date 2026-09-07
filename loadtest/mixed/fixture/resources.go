package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// These are kernel-wide observations from the target's Linux environment.
// Inside a VM they do not describe the physical hypervisor's other workloads.
type resourceSnapshot struct {
	CPUStat   map[string]uint64 `json:"cpu_stat,omitempty"`
	CPUMax    string            `json:"cpu_max,omitempty"`
	HostLoad  []float64         `json:"host_load,omitempty"`
	HostCPUs  int               `json:"host_cpus,omitempty"`
	HostTicks []uint64          `json:"host_ticks,omitempty"`
	Errors    []string          `json:"errors,omitempty"`
}

func targetResources(readFile func(string) ([]byte, error)) resourceSnapshot {
	var result resourceSnapshot
	read := func(path string) string {
		data, err := readFile(path)
		if err != nil {
			result.Errors = append(result.Errors, path+": unavailable")
			return ""
		}
		return strings.TrimSpace(string(data))
	}
	stats := read("/sys/fs/cgroup/cpu.stat")
	if stats != "" {
		result.CPUStat = map[string]uint64{}
		for _, line := range strings.Split(stats, "\n") {
			fields := strings.Fields(line)
			if len(fields) != 2 {
				result.Errors = append(result.Errors, "invalid cgroup CPU statistic")
				continue
			}
			value, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				result.Errors = append(result.Errors, "invalid cgroup CPU value")
				continue
			}
			result.CPUStat[fields[0]] = value
		}
	}
	result.CPUMax = read("/sys/fs/cgroup/cpu.max")
	load := strings.Fields(read("/proc/loadavg"))
	if len(load) >= 3 {
		for _, field := range load[:3] {
			value, err := strconv.ParseFloat(field, 64)
			if err != nil {
				result.Errors = append(result.Errors, "invalid kernel load average")
				break
			}
			result.HostLoad = append(result.HostLoad, value)
		}
	}
	for _, line := range strings.Split(read("/proc/stat"), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "cpu" && len(fields) >= 9 {
			for _, field := range fields[1:9] {
				value, err := strconv.ParseUint(field, 10, 64)
				if err != nil {
					result.Errors = append(result.Errors, "invalid kernel CPU ticks")
					break
				}
				result.HostTicks = append(result.HostTicks, value)
			}
		} else if strings.HasPrefix(fields[0], "cpu") {
			if _, err := strconv.Atoi(strings.TrimPrefix(fields[0], "cpu")); err == nil {
				result.HostCPUs++
			}
		}
	}
	return result
}

func readTargetResources() resourceSnapshot { return targetResources(os.ReadFile) }

func (s resourceSnapshot) validate() error {
	for _, key := range []string{"usage_usec", "nr_periods", "nr_throttled", "throttled_usec"} {
		if _, ok := s.CPUStat[key]; !ok {
			return fmt.Errorf("cgroup v2 CPU accounting unavailable: %s", key)
		}
	}
	if len(s.HostTicks) != 8 || len(s.HostLoad) != 3 || s.HostCPUs < 1 || s.CPUMax == "" {
		return fmt.Errorf("kernel resource observations incomplete")
	}
	if len(s.Errors) > 0 {
		return fmt.Errorf("resource observations incomplete")
	}
	return nil
}
