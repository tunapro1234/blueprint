//go:build linux || darwin

package api

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// socketPath stays short: macOS limits a socket path to 104 bytes.
func socketPath(t *testing.T) string {
	dir, err := os.MkdirTemp("", "bpapi")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "api", "api.sock")
}

func acceptOne(t *testing.T, listener net.Listener) <-chan net.Conn {
	accepted := make(chan net.Conn, 1)
	go func() {
		if conn, err := listener.Accept(); err == nil {
			accepted <- conn
		}
		close(accepted)
	}()
	return accepted
}

func TestListenUnixAcceptsSameUID(t *testing.T) {
	path := socketPath(t)
	listener, err := ListenUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := acceptOne(t, listener)
	client, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	select {
	case conn, ok := <-accepted:
		if !ok {
			t.Fatal("same-uid connection was rejected")
		}
		defer conn.Close()
		if uid, ok := peerUID(conn); !ok || uid != uint32(os.Getuid()) {
			t.Fatalf("peerUID = %d, %v; want %d, true", uid, ok, os.Getuid())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("same-uid connection was not accepted")
	}
}

func TestPeerCredListenerDropsOtherUID(t *testing.T) {
	path := socketPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	inner, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	listener := &peerCredListener{Listener: inner, uid: uint32(os.Getuid()) + 1}
	accepted := acceptOne(t, listener)
	client, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := client.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("read from a dropped connection = %v, want EOF", err)
	}
	listener.Close()
	if conn, ok := <-accepted; ok {
		conn.Close()
		t.Fatal("connection from another uid was accepted")
	}
}

func TestPeerUIDRejectsWhatItCannotRead(t *testing.T) {
	pipe, other := net.Pipe()
	defer pipe.Close()
	defer other.Close()
	if _, ok := peerUID(pipe); ok {
		t.Fatal("peerUID accepted a connection that is not a unix socket")
	}
	path := socketPath(t)
	listener, err := ListenUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := acceptOne(t, listener)
	client, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	conn, ok := <-accepted
	if !ok {
		t.Fatal("same-uid connection was rejected")
	}
	conn.Close()
	if _, ok := peerUID(conn); ok {
		t.Fatal("peerUID read credentials from a closed connection")
	}
}
