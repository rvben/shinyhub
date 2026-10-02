//go:build linux

package process

import (
	"context"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

type systemdPIDWaiter struct{ fd int }

func newSystemdExitWaiter(pid int) systemdExitWaiter {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		// Unsupported kernels, a just-exited process, or descriptor exhaustion
		// only disable notifications; broker verification remains mandatory.
		fd = -1
	}
	return &systemdPIDWaiter{fd: fd}
}

func (w *systemdPIDWaiter) close() {
	if w.fd >= 0 {
		_ = unix.Close(w.fd)
		w.fd = -1
	}
}

func (w *systemdPIDWaiter) wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.fd < 0 {
		return waitSystemdRetry(ctx)
	}
	// A separate pipe wakes poll immediately on cancellation without a busy
	// loop or a goroutine left waiting for a long-lived worker to exit.
	reader, writer, err := os.Pipe()
	if err != nil {
		return waitSystemdRetry(ctx)
	}
	defer reader.Close()
	defer writer.Close()
	stop := context.AfterFunc(ctx, func() { _, _ = writer.Write([]byte{1}) })
	defer stop()
	fds := []unix.PollFd{{Fd: int32(w.fd), Events: unix.POLLIN}, {Fd: int32(reader.Fd()), Events: unix.POLLIN}}
	deadline := time.Now().Add(systemdStatusInterval)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return ctx.Err()
		}
		_, err := unix.Poll(fds, int((remaining+time.Millisecond-1)/time.Millisecond))
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil || fds[0].Revents&unix.POLLNVAL != 0 {
			w.close()
			return waitSystemdRetry(ctx)
		}
		if fds[0].Revents != 0 {
			// Consume the notification once. A job's descendants may outlive its
			// leader; repeatedly polling an already-readable pidfd would spin.
			w.close()
		}
		return nil
	}
}
