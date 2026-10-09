//go:build !linux && !darwin

package api

import (
	"fmt"
	"net"
	"runtime"
)

// Without the peer's uid the socket would serve any local user, so ListenUnix
// refuses to start and every connection is rejected.
func peerCredSupported() error {
	return fmt.Errorf("the API unix socket checks the peer's uid, which bp cannot read on %s", runtime.GOOS)
}

func peerUID(net.Conn) (uint32, bool) { return 0, false }
