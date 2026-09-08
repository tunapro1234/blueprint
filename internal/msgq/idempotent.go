package msgq

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"blueprint/internal/messagetext"
)

// EnqueueOnce binds an authenticated transport key to one durable local record.
// A lost network reply can be retried even after delivery or process restart.
// These records must be retained: deleting them also deletes replay protection.
func (q *Queue) EnqueueOnce(key, to, from, text string) (Message, error) {
	return q.EnqueueOnceOrigin(key, to, from, text, nil)
}

func (q *Queue) EnqueueOnceOrigin(key, to, from, text string, origin *Origin) (Message, error) {
	if key == "" {
		return Message{}, fmt.Errorf("empty idempotency key")
	}
	if err := messagetext.Validate(text); err != nil {
		return Message{}, err
	}
	if err := messagetext.Label(from); err != nil {
		return Message{}, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := os.MkdirAll(q.pending(), 0755); err != nil {
		return Message{}, err
	}
	lock, err := os.OpenFile(filepath.Join(q.Root, ".enqueue-once.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return Message{}, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return Message{}, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	id := fmt.Sprintf("qp%x", sha256.Sum256([]byte(key)))
	if old, err := q.Record(id); err == nil {
		// Peer aliases can change. The authenticated origin and original body,
		// rather than today's presentation label, define replay identity.
		same := old.To == to && old.From == from && old.Msg == text && (origin == nil) == (old.Origin == nil)
		if origin != nil && old.Origin != nil {
			a, b := *origin, *old.Origin
			a.PeerAlias, b.PeerAlias = "", ""
			same = a == b && old.To == to && strings.TrimPrefix(old.Msg, "["+old.From+"] ") == strings.TrimPrefix(text, "["+from+"] ")
		}
		if !same {
			return Message{}, fmt.Errorf("idempotency key reused with different content")
		}
		return old, nil
	} else if !os.IsNotExist(err) {
		return Message{}, err
	}
	m := Message{ID: id, To: to, From: from, Msg: text, TS: float64(q.Now().UnixNano()) / 1e9, Origin: origin}
	if err := writePending(filepath.Join(q.pending(), id+".json"), m); err != nil {
		return Message{}, err
	}
	dir, err := os.Open(q.pending())
	if err != nil {
		return Message{}, err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return Message{}, err
	}
	return m, nil
}

// Record returns structured evidence; missing or corrupt records are errors.
func (q *Queue) Record(id string) (Message, error) {
	if id == "" || filepath.Base(id) != id || id == "." || id == ".." {
		return Message{}, fmt.Errorf("invalid channel ID")
	}
	// finish writes done before unlinking pending. Prefer the terminal evidence.
	if m, err := read(filepath.Join(q.done(), id+".json")); err == nil || !os.IsNotExist(err) {
		return m, err
	}
	m, err := read(filepath.Join(q.pending(), id+".json"))
	if os.IsNotExist(err) {
		// finish may have published done and removed pending between the reads.
		return read(filepath.Join(q.done(), id+".json"))
	}
	return m, err
}
