package api

import (
	"net"

	"golang.org/x/sys/unix"
)

// xucredVersion is XUCRED_VERSION from <sys/ucred.h>; another layout is refused.
const xucredVersion = 0

func peerCredSupported() error { return nil }

// macOS has no SO_PEERCRED; LOCAL_PEERCRED returns the peer's xucred.
func peerUID(conn net.Conn) (uint32, bool) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return 0, false
	}
	var cred *unix.Xucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil || credErr != nil || cred == nil || cred.Version != xucredVersion {
		return 0, false
	}
	return cred.Uid, true
}
