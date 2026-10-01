//go:build !linux

package nativebroker

import (
	"errors"
	"net"
	"os"
)

func sendPacket(*net.UnixConn, []byte, []*os.File) error {
	return errors.New("native user isolation requires Linux; no fallback")
}
func receivePacket(*net.UnixConn) ([]byte, []*os.File, error) {
	return nil, nil, errors.New("native user isolation requires Linux; no fallback")
}
func verifyServer(*net.UnixConn) error {
	return errors.New("native user isolation requires Linux; no fallback")
}
