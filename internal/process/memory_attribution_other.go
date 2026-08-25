//go:build !linux

package process

import "fmt"

func readMemoryAttribution(pid int32) (memoryAttribution, error) {
	return memoryAttribution{}, fmt.Errorf("memory attribution unavailable for pid %d", pid)
}
