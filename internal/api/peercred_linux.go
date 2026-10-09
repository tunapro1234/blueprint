package api

import (
	"net"
	"syscall"
)

func peerCredSupported() error { return nil }

func peerUID(conn net.Conn) (uint32, bool) {
	unix, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := unix.SyscallConn()
	if err != nil {
		return 0, false
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || credErr != nil || cred == nil {
		return 0, false
	}
	return cred.Uid, true
}
