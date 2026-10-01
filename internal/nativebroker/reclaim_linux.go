//go:build linux

package nativebroker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func (s *Server) suspend(ctx context.Context, rec record, fraction float64) (freed bool, err error) {
	if fraction <= 0 || fraction > 1 {
		return false, errors.New("reclaim fraction must be greater than zero and at most one")
	}
	if _, err = system(ctx, "/usr/bin/systemctl", "freeze", rec.Unit); err != nil {
		return false, err
	}
	defer func() {
		if !freed {
			// Cancellation must not leave a resident app frozen.
			thawCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_, thawErr := system(thawCtx, "/usr/bin/systemctl", "thaw", rec.Unit)
			if thawErr != nil {
				err = errors.Join(err, thawErr)
			}
		}
	}()
	props, err := properties(ctx, rec.Unit)
	if err != nil {
		return false, err
	}
	dir := filepath.Clean("/sys/fs/cgroup" + props["ControlGroup"])
	if dir != filepath.Join("/sys/fs/cgroup/system.slice", rec.Unit) {
		return false, errors.New("unexpected reclaim cgroup")
	}
	read := func() (uint64, error) {
		raw, err := os.ReadFile(filepath.Join(dir, "memory.current"))
		if err != nil {
			return 0, err
		}
		return strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	}
	before, err := read()
	if err != nil {
		return false, err
	}
	if before == 0 {
		return false, nil
	}
	// EAGAIN is a partial reclaim, not a request to wait for writability.
	// Go's File.Write would enter the netpoller on this pseudo-file indefinitely.
	fd, openErr := unix.Open(filepath.Join(dir, "memory.reclaim"), unix.O_WRONLY|unix.O_CLOEXEC, 0)
	if openErr != nil {
		err = openErr
	} else {
		for {
			_, err = unix.Write(fd, []byte(strconv.FormatUint(before, 10)))
			if !errors.Is(err, unix.EINTR) {
				break
			}
		}
		unix.Close(fd)
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	// The kernel returns EAGAIN when it reclaims less than the requested amount.
	if err != nil && !errors.Is(err, unix.EAGAIN) {
		return false, err
	}
	after, err := read()
	if err != nil {
		return false, err
	}
	return after < before && float64(before-after)/float64(before) >= fraction, nil
}
