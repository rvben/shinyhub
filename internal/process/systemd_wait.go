package process

import (
	"context"
	"time"
)

// Exit notifications are hints only. The broker still verifies the durable unit
// identity and confirms physical cleanup before a worker is declared stopped.
const systemdStatusInterval = 30 * time.Second
const systemdRetryInterval = 2 * time.Second

type systemdExitWaiter interface {
	wait(context.Context) error
	close()
}

func waitSystemdRetry(ctx context.Context) error {
	timer := time.NewTimer(systemdRetryInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
