//go:build linux

package process

import "syscall"

// oneShotSysProcAttr makes the scheduled command a process-group leader. Do
// not attach Pdeathsig: the root command is the durable holder of any inherited
// publication fence and must survive a control-plane crash while it waits for
// its descendants. Killing only that root could reparent a still-writing child
// that closed inherited descriptors and release the fence prematurely.
func oneShotSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
