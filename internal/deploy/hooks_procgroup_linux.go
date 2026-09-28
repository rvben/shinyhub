//go:build linux

package deploy

import (
	"os/exec"

	"golang.org/x/sys/unix"
)

// waitKillingGroup observes the hook leader's exit via waitid(WNOWAIT),
// which reports the exit status without reaping the process. That keeps the
// leader a zombie, so its PID (and therefore the process group ID it leads,
// since runHookExec sets Setpgid) cannot be recycled by the kernel for an
// unrelated process. Only once the exit has been observed does it kill the
// whole group and reap the leader for real via cmd.Wait.
//
// This covers the case a plain timeout-triggered Cancel cannot: a hook
// leader that backgrounds a grandchild and then exits cleanly on its own,
// well before any timeout fires. Without this sweep the grandchild is
// orphaned to the host with nothing left to signal it.
func waitKillingGroup(cmd *exec.Cmd) error {
	pid := cmd.Process.Pid
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err == nil {
			break
		}
		if err == unix.EINTR {
			continue
		}
		// Waitid failed for a reason other than a signal interrupt (e.g. the
		// leader is already gone). Fall through to cmd.Wait, which will
		// surface the real error.
		break
	}
	_ = killProcessGroup(pid)
	return cmd.Wait()
}
