//go:build !linux

package deploy

import "os/exec"

// waitKillingGroup reaps the hook leader directly. golang.org/x/sys/unix
// does not expose waitid(WNOWAIT) on this platform, so there is no way to
// observe the leader's exit without reaping it first, which means a hook
// leader that backgrounds a grandchild and exits cleanly on its own (before
// any timeout fires) can leave that grandchild orphaned to the host: there
// is a narrow window after the leader is reaped, and before any later kill,
// where its process group ID could be recycled by the kernel for an
// unrelated process. The timeout path is unaffected: cmd.Cancel kills the
// group while the leader (and therefore the group) is still known-live, so
// a hook that overruns its timeout is still fully cleaned up on this
// platform. Production targets Linux, where hooks_procgroup_linux.go closes
// this gap.
func waitKillingGroup(cmd *exec.Cmd) error {
	return cmd.Wait()
}
