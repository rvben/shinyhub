//go:build linux

package nativebroker

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func packetPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s")
	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: path, Net: "unixpacket"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.DialUnix("unixpacket", nil, &net.UnixAddr{Name: path, Net: "unixpacket"})
	if err != nil {
		t.Fatal(err)
	}
	server, err := listener.AcceptUnix()
	if err != nil {
		client.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close(); server.Close() })
	client.SetDeadline(time.Now().Add(3 * time.Second))
	server.SetDeadline(time.Now().Add(3 * time.Second))
	return client, server
}
func TestPacketDescriptorHandoffAndTruncation(t *testing.T) {
	a, b := packetPair(t)
	f, err := os.CreateTemp(t.TempDir(), "lock")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if err := sendPacket(a, []byte("synthetic"), []*os.File{f}); err != nil {
		t.Fatal(err)
	}
	payload, files, err := receivePacket(b)
	if err != nil || string(payload) != "synthetic" || len(files) != 1 {
		t.Fatalf("handoff: %q %d %v", payload, len(files), err)
	}
	defer files[0].Close()
	flags, err := unix.FcntlInt(files[0].Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatal("received descriptor can leak through exec")
	}
	f.Close()
	contender, err := os.OpenFile(f.Name(), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer contender.Close()
	if err := unix.Flock(int(contender.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
		t.Fatal("open-file lock identity was lost")
	}
	files[0].Close()
	if err := unix.Flock(int(contender.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal("received descriptor kept lock after close")
	}
	if err := sendPacket(a, []byte(strings.Repeat("x", MaxMessage+2)), []*os.File{contender}); err != nil {
		t.Fatal(err)
	}
	if _, files, err := receivePacket(b); err == nil || len(files) != 0 {
		t.Fatal("oversized packet was accepted")
	}
}
func TestClientRefusesNonRootPeerBeforeSending(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires an unprivileged test user")
	}
	path := filepath.Join(t.TempDir(), "s")
	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: path, Net: "unixpacket"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan bool, 1)
	go func() {
		c, err := listener.AcceptUnix()
		if err != nil {
			done <- false
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(time.Second))
		buf := make([]byte, MaxMessage)
		oob := make([]byte, 512)
		n, rights, _, _, _ := c.ReadMsgUnix(buf, oob)
		done <- n == 0 && rights == 0
	}()
	_, err = (Client{Socket: path}).Call(context.Background(), Request{Op: "start", Launch: &Launch{Env: []string{"SECRET=synthetic"}}}, nil)
	if err == nil || !strings.Contains(err.Error(), "not root") {
		t.Fatalf("untrusted peer accepted: %v", err)
	}
	if !<-done {
		t.Fatal("credentials were sent to an untrusted peer")
	}
}
