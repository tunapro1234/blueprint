package msgq

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

// MessageLogMaxBytes is the size at which messages.jsonl is rotated. Rotated
// files keep their timestamped name and are never removed by bp.
const MessageLogMaxBytes = 64 << 20

// LogEntry is one line of messages.jsonl: a message in its final state.
// Readers such as the local UI and later analysis use this file instead of
// parsing done/.
type LogEntry struct {
	ID       string  `json:"id"`
	To       string  `json:"to"`
	From     string  `json:"from"`
	Msg      string  `json:"msg"`
	TS       float64 `json:"ts"`
	Finished float64 `json:"finished"`
	Status   string  `json:"status"`
	Peer     string  `json:"peer,omitempty"`
	PeerID   string  `json:"peerId,omitempty"`
	Remote   bool    `json:"remote,omitempty"`
}

// MessageLogPath is the active log under a queue root.
func MessageLogPath(root string) string { return filepath.Join(root, "messages.jsonl") }

func entryOf(m Message) LogEntry {
	e := LogEntry{ID: m.ID, To: m.To, From: m.From, Msg: m.Msg, TS: m.TS, Finished: m.Finished, Status: m.Status}
	if m.Origin != nil {
		e.Peer, e.PeerID, e.Remote = m.Origin.PeerAlias, m.Origin.PeerID, true
	}
	return e
}

// appendMessageLog records one finished message. A failure here never undoes
// the delivery; the done/ record remains the source of truth and Reindex can
// rebuild the log.
func (q *Queue) appendMessageLog(m Message) error {
	return appendEntries(q.Root, []LogEntry{entryOf(m)})
}

func appendEntries(root string, entries []LogEntry) error {
	lock, err := os.OpenFile(filepath.Join(root, ".messages.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	var buf []byte
	for _, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			return err
		}
		buf = append(append(buf, line...), '\n')
	}
	path := MessageLogPath(root)
	if st, err := os.Stat(path); err == nil && st.Size()+int64(len(buf)) > MessageLogMaxBytes {
		rotated := filepath.Join(root, "messages-"+time.Now().UTC().Format("20060102T150405Z")+".jsonl")
		if err := os.Rename(path, rotated); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(buf)
	return err
}

// Reindex appends done/ records that the message log does not contain yet,
// oldest first. It is how an existing install gets its history into the log.
func (q *Queue) Reindex() (int, error) {
	seen := map[string]bool{}
	if f, err := os.Open(MessageLogPath(q.Root)); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 4<<20)
		for sc.Scan() {
			var e LogEntry
			if json.Unmarshal(sc.Bytes(), &e) == nil && e.ID != "" {
				seen[e.ID] = true
			}
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return 0, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	paths, err := filepath.Glob(filepath.Join(q.done(), "*.json"))
	if err != nil {
		return 0, err
	}
	var missing []LogEntry
	for _, path := range paths {
		m, err := read(path)
		if err != nil || m.ID == "" || seen[m.ID] {
			continue
		}
		missing = append(missing, entryOf(m))
	}
	sort.SliceStable(missing, func(i, j int) bool { return missing[i].Finished < missing[j].Finished })
	for start := 0; start < len(missing); start += 500 {
		if err := appendEntries(q.Root, missing[start:min(start+500, len(missing))]); err != nil {
			return start, err
		}
	}
	return len(missing), nil
}
