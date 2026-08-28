//go:build !linux

package process

import "syscall"

func oneShotSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
