//go:build !linux && !darwin

package api

import (
	"net"
	"path/filepath"
	"testing"
)

func TestListenUnixFailsClosedWithoutPeerCredentials(t *testing.T) {
	if _, err := ListenUnix(filepath.Join(t.TempDir(), "api.sock")); err == nil {
		t.Fatal("ListenUnix served a socket whose peers it cannot identify")
	}
	pipe, other := net.Pipe()
	defer pipe.Close()
	defer other.Close()
	if _, ok := peerUID(pipe); ok {
		t.Fatal("peerUID accepted a connection")
	}
}
