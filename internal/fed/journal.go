package fed

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The journal records the CONTENT of every federation message crossing this
// machine, in both directions. The audit log (log.jsonl) records that requests
// happened; the journal records what was actually said, so external traffic
// can be inspected after the fact. Append-only JSONL, same rotation as audit.
type JournalEntry struct {
	TS   string `json:"ts"`
	Dir  string `json:"dir"` // "in" (from a peer) or "out" (to a peer)
	ID   string `json:"id,omitempty"`
	From string `json:"from"`
	To   string `json:"to"`
	Msg  string `json:"msg"`
}

var journalMu sync.Mutex

func JournalPath(stateDir string) string {
	return filepath.Join(stateDir, "fed", "messages.jsonl")
}

// Journal appends one entry. A journal failure never blocks delivery: the
// caller logs it at most.
func Journal(stateDir, dir, id, from, to, msg string) error {
	journalMu.Lock()
	defer journalMu.Unlock()
	path := JournalPath(stateDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(JournalEntry{
		TS:   time.Now().UTC().Format(time.RFC3339Nano),
		Dir:  dir,
		ID:   id,
		From: from,
		To:   to,
		Msg:  msg,
	})
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if info, statErr := os.Stat(path); statErr == nil && info.Size()+int64(len(data)) > maxAuditBytes {
		backup := path + ".1"
		if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Rename(path, backup); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	return writeErr
}

// ReadJournal returns the last n entries, oldest first. A missing journal is
// an empty history, not an error.
func ReadJournal(stateDir string, n int) ([]JournalEntry, error) {
	file, err := os.Open(JournalPath(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var entries []JournalEntry
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var entry JournalEntry
		if json.Unmarshal(scanner.Bytes(), &entry) == nil {
			entries = append(entries, entry)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if n > 0 && len(entries) > n {
		entries = entries[len(entries)-n:]
	}
	return entries, nil
}
