package fed

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	MaxPeerMessages = 500
	MaxPeerBytes    = 8 * 1024 * 1024
	MaxMessageAge   = 7 * 24 * time.Hour
	InflightTimeout = 5 * time.Minute
)

type Outbox struct {
	StateDir string
	Now      func() time.Time
	Log      io.Writer
	mu       sync.Mutex
}

func NewOutbox(stateDir string) *Outbox {
	return &Outbox{StateDir: stateDir, Now: time.Now, Log: os.Stderr}
}

func (o *Outbox) peerDir(kind, peer string) (string, error) {
	if peer == "" || peer == "." || peer == ".." {
		return "", fmt.Errorf("invalid peer %q", peer)
	}
	if err := validateName(peer); err != nil {
		return "", fmt.Errorf("invalid peer %q: %w", peer, err)
	}
	base := filepath.Base(peer)
	if base != peer || base == "." || base == ".." {
		return "", fmt.Errorf("invalid peer %q", peer)
	}
	return filepath.Join(o.StateDir, "fed", kind, base), nil
}

// Enqueue writes one outbound message using the same temporary-file plus hard
// link publication pattern as the local message queue.
func (o *Outbox) Enqueue(peer, to, from, text string) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	dir, err := o.peerDir("out", peer)
	if err != nil {
		return "", err
	}
	inflightDir, err := o.peerDir("inflight", peer)
	if err != nil {
		return "", err
	}
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
		if _, statErr := os.Stat(filepath.Join(inflightDir, id+".json")); statErr == nil {
			id = fmt.Sprintf("%s-%06d", base, suffix)
			message.ID = id
			if err := rewriteTemp(tmpName, message); err != nil {
				return "", err
			}
			continue
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
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
		if err := o.enforceDir(peer, "out", dir); err != nil {
			return "", err
		}
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

// Poll atomically moves a peer's messages into inflight storage and returns
// them in filename/FIFO order. Ack removes successfully delivered messages.
func (o *Outbox) Poll(peer string) ([]Message, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	outDir, err := o.peerDir("out", peer)
	if err != nil {
		return nil, err
	}
	inflightDir, err := o.peerDir("inflight", peer)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(inflightDir, 0o755); err != nil {
		return nil, err
	}
	if err := o.enforceDir(peer, "inflight", inflightDir); err != nil {
		return nil, err
	}
	if err := o.recoverInflight(outDir, inflightDir); err != nil {
		return nil, err
	}
	if err := o.enforceDir(peer, "out", outDir); err != nil {
		return nil, err
	}
	paths, err := filepath.Glob(filepath.Join(outDir, "q*.json"))
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
		target := filepath.Join(inflightDir, filepath.Base(path))
		if err := os.Rename(path, target); err != nil {
			return nil, fmt.Errorf("move %s to inflight: %w", path, err)
		}
		now := o.Now()
		if err := os.Chtimes(target, now, now); err != nil {
			return nil, fmt.Errorf("mark %s inflight: %w", target, err)
		}
		messages = append(messages, message)
	}
	if len(paths) > 0 {
		syncDir(outDir)
		syncDir(inflightDir)
	}
	return messages, nil
}

func (o *Outbox) recoverInflight(outDir, inflightDir string) error {
	paths, err := filepath.Glob(filepath.Join(inflightDir, "q*.json"))
	if err != nil {
		return err
	}
	now := o.Now()
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if now.Sub(info.ModTime()) < InflightTimeout {
			continue
		}
		target := filepath.Join(outDir, filepath.Base(path))
		if err := os.Rename(path, target); err != nil {
			return fmt.Errorf("return stale inflight message %s: %w", path, err)
		}
	}
	if len(paths) > 0 {
		syncDir(outDir)
		syncDir(inflightDir)
	}
	return nil
}

// Ack removes the selected message IDs from a peer's inflight storage.
func (o *Outbox) Ack(peer string, ids []string) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	dir, err := o.peerDir("inflight", peer)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, id := range ids {
		if err := validateMessageID(id); err != nil {
			return removed, err
		}
		err := os.Remove(filepath.Join(dir, id+".json"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return removed, err
		}
		removed++
	}
	if removed > 0 {
		syncDir(dir)
	}
	return removed, nil
}

func validateMessageID(id string) error {
	if id == "" || filepath.Base(id) != id || !strings.HasPrefix(id, "q") {
		return fmt.Errorf("invalid message id %q", id)
	}
	for _, char := range []byte(id) {
		if !((char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			char == '.' || char == '_' || char == '-') {
			return fmt.Errorf("invalid message id %q", id)
		}
	}
	return nil
}

func (o *Outbox) Depth(peer string) (int, error) {
	dir, err := o.peerDir("out", peer)
	if err != nil {
		return 0, err
	}
	paths, err := filepath.Glob(filepath.Join(dir, "q*.json"))
	return len(paths), err
}

func (o *Outbox) enforceDir(peer, kind, dir string) error {
	paths, err := filepath.Glob(filepath.Join(dir, "q*.json"))
	if err != nil {
		return err
	}
	sort.Strings(paths)
	now := o.Now()
	type queuedFile struct {
		path string
		size int64
	}
	kept := make([]queuedFile, 0, len(paths))
	var total int64
	expired := 0
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		message, err := readOutboxMessage(path)
		if err != nil {
			return err
		}
		created := time.Unix(0, int64(message.TS*1e9))
		if !created.IsZero() && now.Sub(created) > MaxMessageAge {
			if err := os.Remove(path); err != nil {
				return err
			}
			expired++
			continue
		}
		kept = append(kept, queuedFile{path: path, size: info.Size()})
		total += info.Size()
	}
	dropped := 0
	for len(kept) > MaxPeerMessages || total > MaxPeerBytes {
		oldest := kept[0]
		if err := os.Remove(oldest.path); err != nil {
			return err
		}
		total -= oldest.size
		kept = kept[1:]
		dropped++
	}
	if expired > 0 && o.Log != nil {
		fmt.Fprintf(o.Log, "fed: dropped %d expired messages for peer %s (%s)\n", expired, peer, kind)
	}
	if dropped > 0 && o.Log != nil {
		fmt.Fprintf(o.Log, "fed: dropped %d oldest messages for peer %s (quota)\n", dropped, peer)
	}
	if expired+dropped > 0 {
		syncDir(dir)
	}
	return nil
}

func readOutboxMessage(path string) (Message, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Message{}, fmt.Errorf("read %s: %w", path, err)
	}
	var message Message
	if err := json.Unmarshal(data, &message); err != nil {
		return Message{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return message, nil
}

func QueueOutbound(stateDir string, peers map[string]Peer, peer, to, from, text string) (string, error) {
	if _, ok := peers[peer]; !ok {
		return "", fmt.Errorf("unknown peer: %s", peer)
	}
	if len([]byte(text)) > MaxMessageBytes {
		return "", fmt.Errorf("message exceeds %d bytes", MaxMessageBytes)
	}
	if err := validateName(to); err != nil {
		return "", fmt.Errorf("invalid target: %w", err)
	}
	if err := validateMessageFrom(from); err != nil {
		return "", fmt.Errorf("invalid sender: %w", err)
	}
	text = sanitize(text)
	id, err := NewOutbox(stateDir).Enqueue(peer, to, from, text)
	if err == nil {
		_ = Journal(stateDir, "out", id, from, to+"@"+peer, text)
	}
	return id, err
}

func validateMessageFrom(from string) error {
	if !strings.Contains(from, "@") {
		return validateName(from)
	}
	_, _, federated, err := ParseAddress(from)
	if err != nil {
		return err
	}
	if !federated {
		return fmt.Errorf("invalid sender")
	}
	return nil
}
