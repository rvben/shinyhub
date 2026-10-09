package process

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// NativeProcessStartIdentity combines kernel start ticks and host boot ID.
// Neither input depends on the wall clock or on gopsutil boot-time estimates.
func NativeProcessStartIdentity(pid int) (int64, error) {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	end := strings.LastIndexByte(string(stat), ')')
	if end < 0 {
		return 0, fmt.Errorf("invalid process stat")
	}
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) <= 19 {
		return 0, fmt.Errorf("incomplete process stat")
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return 0, err
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return 0, err
	}
	if len(strings.TrimSpace(string(boot))) == 0 {
		return 0, fmt.Errorf("empty host boot identity")
	}
	digest := sha256.Sum256([]byte(strings.TrimSpace(string(boot)) + ":" + fields[19]))
	identity := int64(binary.BigEndian.Uint64(digest[:8]) & 0x7fffffffffffffff)
	if identity == 0 {
		return 0, fmt.Errorf("invalid process identity")
	}
	return identity, nil
}
