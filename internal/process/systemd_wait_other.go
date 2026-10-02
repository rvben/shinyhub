//go:build !linux

package process

import "context"

type systemdPollingWaiter struct{}

func newSystemdExitWaiter(int) systemdExitWaiter { return systemdPollingWaiter{} }
func (systemdPollingWaiter) close()              {}
func (systemdPollingWaiter) wait(ctx context.Context) error {
	return waitSystemdRetry(ctx)
}
