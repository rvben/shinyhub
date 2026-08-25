package process

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// memoryAttribution is the physical-memory attribution Linux reports in
// /proc/<pid>/smaps_rollup. USS is the sum of private clean and dirty pages;
// PSS and SwapPSS retain the kernel's proportional accounting for shared pages.
type memoryAttribution struct {
	PSS     uint64
	USS     uint64
	SwapPSS uint64
}

func parseMemoryAttribution(r io.Reader) (memoryAttribution, error) {
	var out memoryAttribution
	foundPSS := false
	s := bufio.NewScanner(r)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) < 2 {
			continue
		}
		var target *uint64
		switch fields[0] {
		case "Pss:":
			target, foundPSS = &out.PSS, true
		case "Private_Clean:", "Private_Dirty:", "Private_Hugetlb:":
			target = &out.USS
		case "SwapPss:":
			target = &out.SwapPSS
		default:
			continue
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || kb > math.MaxUint64/1024 {
			return memoryAttribution{}, fmt.Errorf("parse %s value %q", fields[0], fields[1])
		}
		*target += kb * 1024
	}
	if err := s.Err(); err != nil {
		return memoryAttribution{}, fmt.Errorf("scan smaps_rollup: %w", err)
	}
	if !foundPSS {
		return memoryAttribution{}, fmt.Errorf("smaps_rollup has no Pss field")
	}
	return out, nil
}
