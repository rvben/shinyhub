//go:build !linux

package process

import gops "github.com/shirou/gopsutil/v4/process"

// NativeProcessStartIdentity uses the OS process birth time on non-Linux hosts.
func NativeProcessStartIdentity(pid int) (int64, error) {
	p, err := gops.NewProcess(int32(pid))
	if err != nil {
		return 0, err
	}
	return p.CreateTime()
}
