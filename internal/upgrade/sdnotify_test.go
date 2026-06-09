package upgrade

import (
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNotifyReady_SendsReadyAndMainPID(t *testing.T) {
	// The socket path must fit in sockaddr_un.sun_path (104 bytes on macOS, 108
	// on Linux). t.TempDir() embeds the long test name, and on macOS $TMPDIR
	// lives under /var/folders/..., so that path overruns the limit and bind()
	// fails with EINVAL ("invalid argument"). A short MkdirTemp dir (no embedded
	// test name) keeps the path well within the limit on both platforms.
	dir, err := os.MkdirTemp("", "sdnotify")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockPath := dir + "/notify.sock"
	laddr, err := net.ResolveUnixAddr("unixgram", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenUnixgram("unixgram", laddr)
	if err != nil {
		// Include the path and its byte length so any future sun_path overrun
		// is self-diagnosing rather than a cryptic EINVAL.
		t.Fatalf("listen unixgram at %q (%d bytes): %v", sockPath, len(sockPath), err)
	}
	defer conn.Close()

	t.Setenv("NOTIFY_SOCKET", sockPath)
	if err := NotifyReady(); err != nil {
		t.Fatalf("NotifyReady: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 256)
	n, _, err := conn.ReadFromUnix(buf)
	if err != nil {
		t.Fatalf("read notify message: %v", err)
	}
	msg := string(buf[:n])
	if !strings.Contains(msg, "READY=1") {
		t.Fatalf("notify message %q missing READY=1", msg)
	}
	if !strings.Contains(msg, "MAINPID="+strconv.Itoa(os.Getpid())) {
		t.Fatalf("notify message %q missing MAINPID=%d", msg, os.Getpid())
	}
}

func TestNotifyReady_NoSocketIsNoop(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	if err := NotifyReady(); err != nil {
		t.Fatalf("NotifyReady with no socket must be a no-op, got %v", err)
	}
}
