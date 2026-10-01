//go:build linux

package nativebroker

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Bootstrap runs under the systemd app identity. No app environment or command
// is evaluated until the caller has durably acknowledged the worker PID.
func Bootstrap(gate string) error {
	if os.Geteuid() == 0 {
		return errors.New("worker bootstrap must not run as root")
	}
	raw, err := os.ReadFile(filepath.Join(os.Getenv("CREDENTIALS_DIRECTORY"), "launch.json"))
	if err != nil {
		return err
	}
	var launch Launch
	if json.Unmarshal(raw, &launch) != nil {
		return errors.New("invalid launch credential")
	}
	conn, err := net.DialUnix("unixpacket", nil, &net.UnixAddr{Name: gate, Net: "unixpacket"})
	if err != nil {
		return err
	}
	if err = verifyServer(conn); err != nil {
		conn.Close()
		return err
	}
	payload, files, err := receivePacket(conn)
	conn.Close()
	defer func() {
		for _, f := range files {
			_ = f.Close()
		}
	}()
	if err != nil {
		return err
	}
	if string(payload) != "ready" {
		return errors.New("invalid descriptor handoff")
	}
	expected := launch.LifetimeCount + 1
	if launch.Guarded {
		expected++
	}
	if len(files) != expected {
		return errors.New("incomplete descriptor handoff")
	}
	index := 1 // descriptor zero is the combined stdout/stderr pipe
	if launch.Guarded {
		// SCM_RIGHTS may deliver a blocking pipe. Poll the raw descriptor rather
		// than relying on Go file deadlines, which require a pollable FD.
		fd := int(files[index].Fd())
		if err = unix.SetNonblock(fd, true); err != nil {
			return err
		}
		line := make([]byte, 0, 33)
		deadline := time.Now().Add(30 * time.Second)
		for len(line) < 33 && !bytes.ContainsRune(line, '\n') {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return errors.New("launch acknowledgement timed out")
			}
			poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN | unix.POLLHUP}}
			n, err := unix.Poll(poll, int(remaining.Milliseconds()))
			if err == unix.EINTR {
				continue
			}
			if err != nil {
				return err
			}
			if n == 0 {
				return errors.New("launch acknowledgement timed out")
			}
			buf := make([]byte, 33-len(line))
			n, err = unix.Read(fd, buf)
			if err == unix.EAGAIN || err == unix.EINTR {
				continue
			}
			if err != nil {
				return err
			}
			if n == 0 {
				break
			}
			line = append(line, buf[:n]...)
		}
		if !bytes.Equal(line, []byte("ready\n")) {
			return errors.New("launch was not acknowledged")
		}
		_ = files[index].Close()
		index++
	}
	// Work at high FD numbers first so replacing stdin/stdout/FD 3 cannot
	// overwrite a source descriptor or close an inherited publication lock.
	sources := append([]*os.File{files[0], files[0]}, files[index:]...)
	duplicates := make([]int, len(sources))
	for i, f := range sources {
		duplicates[i], err = unix.FcntlInt(f.Fd(), unix.F_DUPFD_CLOEXEC, 256)
		if err != nil {
			return err
		}
	}
	for _, f := range files {
		_ = f.Close()
	}
	files = nil
	for i, fd := range duplicates {
		target := i + 1
		if err = unix.Dup3(fd, target, 0); err != nil {
			return err
		}
		unix.Close(fd)
	}
	// The private primary app group and group-readable files allow the controller
	// to manage storage through its app-group memberships.
	unix.Umask(0007)
	env := make([]string, 0, len(launch.Env)+8)
	for _, e := range launch.Env {
		if !strings.HasPrefix(e, "CREDENTIALS_DIRECTORY=") {
			env = append(env, e)
		}
	}
	// Never let UV hardlinks turn permission preparation into a cross-tree edit.
	env = append(env, "UV_LINK_MODE=copy", "TMPDIR=/tmp")
	if err = unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	return unix.Exec(launch.Argv[0], launch.Argv, env)
}
