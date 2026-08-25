//go:build linux

package process

import (
	"fmt"
	"os"
)

func readMemoryAttribution(pid int32) (memoryAttribution, error) {
	f, err := os.Open(fmt.Sprintf("/proc/%d/smaps_rollup", pid))
	if err != nil {
		return memoryAttribution{}, err
	}
	defer f.Close()
	return parseMemoryAttribution(f)
}
