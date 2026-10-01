package workloadmetrics

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	gops "github.com/shirou/gopsutil/v4/process"
)

type reading struct {
	created int64
	cpu     float64
	rss     uint64
}

func readProcess(pid int32) (reading, error) {
	p, err := gops.NewProcess(pid)
	if err != nil {
		return reading{}, err
	}
	created, err := p.CreateTime()
	if err != nil {
		return reading{}, err
	}
	times, err := p.Times()
	if err != nil {
		return reading{created: created}, err
	}
	mem, err := p.MemoryInfo()
	if err != nil {
		return reading{created: created}, err
	}
	return reading{created: created, cpu: times.User + times.System, rss: mem.RSS}, nil
}

func scanGroups() (map[int32][]int32, error) {
	pids, err := gops.Pids()
	if err != nil {
		return nil, err
	}
	groups := make(map[int32][]int32)
	for _, pid := range pids {
		pgid, err := syscall.Getpgid(int(pid))
		if err == nil {
			groups[int32(pgid)] = append(groups[int32(pgid)], pid)
		}
	}
	return groups, nil
}

// Read only the dedicated cgroup supplied by the native runtime. In particular,
// never infer it from /proc: an uncapped workload can share ShinyHub's cgroup.
func readCgroupCPU(dir string) (float64, error) {
	b, err := os.ReadFile(filepath.Join(dir, "cpu.stat"))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "usage_usec" {
			v, err := strconv.ParseUint(fields[1], 10, 64)
			return float64(v) / 1e6, err
		}
	}
	return 0, fmt.Errorf("cpu.stat has no usage_usec")
}

type sampler struct {
	process func(int32) (reading, error)
	groups  func() (map[int32][]int32, error)
	cgroup  func(string) (float64, error)
}

func newSampler() sampler {
	return sampler{process: readProcess, groups: scanGroups, cgroup: readCgroupCPU}
}

// sample accumulates only observed per-process CPU increments. An exited child
// keeps its last contribution, and a reused PID starts a new contribution. The
// result is monotonic but excludes work done by children entirely between ticks.
// Cgroup accounting, when available at registration, includes that work instead.
func (s sampler) sample(w *workload, members []int32, now uint64) {
	root, rootErr := s.process(w.pid)
	if rootErr == nil && w.created != 0 && root.created != w.created {
		return // root PID now belongs to an unrelated launch
	}
	if root.created != 0 && w.created == 0 {
		w.created = root.created
	}
	if w.cgroup != "" {
		if cpu, err := s.cgroup(w.cgroup); err == nil && cpu >= w.cpuBaseline {
			w.cpu = math.Max(w.cpu, cpu-w.cpuBaseline)
			w.cpuTime = now
		}
	}
	var rss uint64
	var measured, partial bool
	seen := make(map[int32]struct{}, len(members))
	for _, pid := range members {
		seen[pid] = struct{}{}
		r, err := root, rootErr
		if pid != w.pid {
			r, err = s.process(pid)
		}
		if err != nil {
			partial = true
			continue
		}
		if math.IsNaN(r.cpu) || math.IsInf(r.cpu, 0) || r.cpu < 0 {
			partial = true
			continue
		}
		if rss > math.MaxInt64 || r.rss > math.MaxInt64-rss {
			partial = true
			continue
		}
		rss += r.rss
		measured = true
		if w.cgroup == "" {
			previous, ok := w.members[pid]
			if !ok || previous.created != r.created {
				// The root and children already alive before registration need
				// a baseline. Especially after recovery, their lifetime totals
				// must not be attributed to the new observation interval.
				if pid != w.pid && r.created >= int64(w.start/1e6) {
					w.cpu += r.cpu
				}
			} else if r.cpu >= previous.cpu {
				w.cpu += r.cpu - previous.cpu
			}
			if !ok || previous.created != r.created || r.cpu >= previous.cpu {
				w.members[pid] = r
			}
		}
	}
	// Bound state by current group membership; keep readings across transient
	// read failures so the next successful read does not double-count CPU.
	for pid := range w.members {
		if _, ok := seen[pid]; !ok {
			// A transient getpgid failure can omit a live member. Keep its
			// baseline until exit/reuse is established, avoiding double-counting
			// its lifetime CPU if it reappears on the next scan.
			r, err := s.process(pid)
			if err != nil || r.created != w.members[pid].created {
				delete(w.members, pid)
			}
		}
	}
	if measured && !partial {
		w.rss, w.rssTime = int64(rss), now
	}
	if measured && w.cgroup == "" {
		w.cpuTime = now
	}
}
