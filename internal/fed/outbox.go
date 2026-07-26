package fed

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type Outbox struct {
	StateDir string
	Now      func() time.Time
	mu       sync.Mutex
}

func NewOutbox(stateDir string) *Outbox {
	return &Outbox{StateDir: stateDir, Now: time.Now}
}

func (o *Outbox) peerDir(peer string) string {
	return filepath.Join(o.StateDir, "fed", "out", peer)
}

// Enqueue writes one outbound message using the same temporary-file plus hard
// link publication pattern as the local message queue.
func (o *Outbox) Enqueue(peer, to, from, text string) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	dir := o.peerDir(peer)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	now := o.Now()
	base := fmt.Sprintf("q%019d", now.UnixNano())
	tmp, err := os.CreateTemp(dir, ".pending-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", err
	}
	id := base + "-000000"
	message := Message{ID: id, To: to, From: from, Msg: text, TS: float64(now.UnixNano()) / 1e9}
	if err := encodeSyncClose(tmp, message); err != nil {
		return "", err
	}
	for suffix := 1; ; suffix++ {
		path := filepath.Join(dir, id+".json")
		err = os.Link(tmpName, path)
		if errors.Is(err, os.ErrExist) {
			id = fmt.Sprintf("%s-%06d", base, suffix)
			message.ID = id
			if err := rewriteTemp(tmpName, message); err != nil {
				return "", err
			}
			continue
		}
		if err != nil {
			return "", err
		}
		syncDir(dir)
		return id, nil
	}
}

func encodeSyncClose(file *os.File, message Message) error {
	err := json.NewEncoder(file).Encode(message)
	if syncErr := file.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

func rewriteTemp(path string, message Message) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	return encodeSyncClose(file, message)
}

func syncDir(path string) {
	if dir, err := os.Open(path); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
}

// Poll returns and removes a peer's messages in filename/FIFO order.
func (o *Outbox) Poll(peer string) ([]Message, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	paths, err := filepath.Glob(filepath.Join(o.peerDir(peer), "q*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	messages := make([]Message, 0, len(paths))
	for _, path := range paths {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, fmt.Errorf("read %s: %w", path, readErr)
		}
		var message Message
		if err := json.Unmarshal(data, &message); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove %s: %w", path, err)
		}
		messages = append(messages, message)
	}
	if len(paths) > 0 {
		syncDir(o.peerDir(peer))
	}
	return messages, nil
}

func (o *Outbox) Depth(peer string) (int, error) {
	paths, err := filepath.Glob(filepath.Join(o.peerDir(peer), "q*.json"))
	return len(paths), err
}

func QueueOutbound(stateDir string, peers map[string]Peer, peer, to, from, text string) (string, error) {
	if _, ok := peers[peer]; !ok {
		return "", fmt.Errorf("unknown peer: %s", peer)
	}
	if len([]byte(text)) > MaxMessageBytes {
		return "", fmt.Errorf("message exceeds %d bytes", MaxMessageBytes)
	}
	to, from, text = sanitize(to), sanitize(from), sanitize(text)
	if err := validateName(to); err != nil {
		return "", fmt.Errorf("invalid target: %w", err)
	}
	if err := validateName(from); err != nil {
		return "", fmt.Errorf("invalid sender: %w", err)
	}
	return NewOutbox(stateDir).Enqueue(peer, to, from, text)
}
