//go:build linux

package nativebroker

import (
	"errors"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

func sendPacket(c *net.UnixConn, payload []byte, files []*os.File) error {
	descriptors := make([]int, len(files))
	for i, f := range files {
		if f == nil {
			return errors.New("nil launch descriptor")
		}
		descriptors[i] = int(f.Fd())
	}
	var control []byte
	if len(descriptors) > 0 {
		control = unix.UnixRights(descriptors...)
	}
	n, _, err := c.WriteMsgUnix(payload, control, nil)
	if err == nil && n != len(payload) {
		return errors.New("incomplete native broker packet")
	}
	return err
}
func receivePacket(c *net.UnixConn) ([]byte, []*os.File, error) {
	payload := make([]byte, MaxMessage+1)
	control := make([]byte, unix.CmsgSpace(MaxFiles*4))
	raw, err := c.SyscallConn()
	if err != nil {
		return nil, nil, err
	}
	var n, oob, flags int
	var socketErr error
	err = raw.Read(func(fd uintptr) bool {
		n, oob, flags, _, socketErr = unix.Recvmsg(int(fd), payload, control, unix.MSG_CMSG_CLOEXEC)
		return socketErr != unix.EAGAIN && socketErr != unix.EWOULDBLOCK && socketErr != unix.EINTR
	})
	if err == nil {
		err = socketErr
	}
	if err != nil {
		return nil, nil, err
	}
	messages, err := unix.ParseSocketControlMessage(control[:oob])
	var files []*os.File
	if err == nil {
		for _, m := range messages {
			var fds []int
			fds, err = unix.ParseUnixRights(&m)
			if err != nil {
				break
			}
			for _, fd := range fds {
				unix.CloseOnExec(fd)
				files = append(files, os.NewFile(uintptr(fd), "native-launch"))
			}
		}
	}
	if err != nil || n > MaxMessage || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 || len(files) > MaxFiles {
		for _, f := range files {
			_ = f.Close()
		}
		return nil, nil, errors.New("invalid or oversized native broker packet")
	}
	return payload[:n], files, nil
}
func peerUID(c *net.UnixConn) (int, int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, 0, err
	}
	var cred *unix.Ucred
	var socketErr error
	err = raw.Control(func(fd uintptr) { cred, socketErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) })
	if err != nil {
		return 0, 0, err
	}
	if socketErr != nil {
		return 0, 0, socketErr
	}
	if cred == nil {
		return 0, 0, errors.New("missing peer credentials")
	}
	return int(cred.Uid), int(cred.Pid), nil
}
func verifyServer(c *net.UnixConn) error {
	uid, _, err := peerUID(c)
	if err != nil {
		return err
	}
	if uid != 0 {
		return errors.New("native broker peer is not root")
	}
	return nil
}
