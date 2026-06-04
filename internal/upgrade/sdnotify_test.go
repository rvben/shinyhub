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
	dir := t.TempDir()
	sockPath := dir + "/notify.sock"
	laddr, err := net.ResolveUnixAddr("unixgram", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenUnixgram("unixgram", laddr)
	if err != nil {
		t.Fatalf("listen unixgram: %v", err)
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
