package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// TokenPath is the per-user API token file.
func TokenPath(stateDir string) string { return filepath.Join(stateDir, "api", "token") }

// SocketPath is the per-user API unix socket.
func SocketPath(stateDir string) string { return filepath.Join(stateDir, "api", "api.sock") }

// LoadOrCreateToken returns the API token, creating it (32 random bytes,
// file mode 0600) on first use. A token file readable by others is refused.
func LoadOrCreateToken(stateDir string) (string, error) {
	path := TokenPath(stateDir)
	var token string
	err := withLock(path, func() error {
		info, err := os.Stat(path)
		if err == nil {
			if info.Mode().Perm()&0o077 != 0 {
				return fmt.Errorf("%s is readable by other users (mode %v); fix with chmod 600", path, info.Mode().Perm())
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			token = strings.TrimSpace(string(data))
			if len(token) < 32 {
				return fmt.Errorf("%s holds no usable token", path)
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			return err
		}
		token = "bpt_" + base64.RawURLEncoding.EncodeToString(b[:])
		return writeFileAtomic(path, []byte(token+"\n"))
	})
	return token, err
}

// tokenEqual compares tokens in constant time.
func tokenEqual(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// ListenUnix opens the API socket: 0600 inside a 0700 directory, and every
// connection is checked to come from this uid (peercred_*.go).
func ListenUnix(path string) (net.Listener, error) {
	if err := peerCredSupported(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return nil, err
	}
	if err := os.Chmod(filepath.Dir(path), dirMode); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		// A live server answers; a stale socket from a dead one is replaced.
		if conn, err := net.Dial("unix", path); err == nil {
			conn.Close()
			return nil, fmt.Errorf("another bp API server is listening on %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, fileMode); err != nil {
		listener.Close()
		return nil, err
	}
	return &peerCredListener{Listener: listener, uid: uint32(os.Getuid())}, nil
}

type peerCredListener struct {
	net.Listener
	uid uint32
}

// Accept drops connections from other uids before any byte is read.
func (l *peerCredListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if uid, ok := peerUID(conn); ok && uid == l.uid {
			return conn, nil
		}
		conn.Close()
	}
}
