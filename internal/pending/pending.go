package pending

import (
	"blueprint/internal/messagetext"
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const (

	// maxRecordBytes bounds one spooled record, on both sides of the store.
	// The spool is line-delimited, so a record longer than the reader's line
	// buffer makes the WHOLE file unreadable: bufio.Scanner returns ErrTooLong
	// and every later Load fails, stranding messages that were accepted. Append
	// refuses anything the reader cannot read back, so "written" always implies
	// "readable". 8 MiB is far above any real bp msg or bp announce text
	// (federated messages are capped at fed.MaxMessageBytes = 16 KiB) and costs
	// nothing until a line actually needs it.
	maxRecordBytes = 8 << 20
)

type Entry struct {
	TS   int64  `json:"ts"`
	From string `json:"from"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

func Append(dir, agent string, entry Entry) error {
	if err := validate(entry); err != nil {
		return err
	}
	// json.Marshal escapes newlines, so one record is always exactly one line
	// however many line breaks the message text carries.
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > maxRecordBytes {
		return fmt.Errorf("message is %d bytes, over the %d byte spool record limit", len(data), maxRecordBytes)
	}
	if err := os.MkdirAll(filepath.Join(dir, "pending"), 0755); err != nil {
		return err
	}
	file, err := os.OpenFile(path(dir, agent), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN) //nolint:errcheck
	_, err = file.Write(data)
	return err
}

// ErrReadOnly reports that this process cannot WRITE the spool — the file, or
// the tree it lives in, is mounted read-only for us. Measured 2026-08-25: a
// Codex agent under bubblewrap has /srv/blueprint mounted read-only, so every
// `bp msg` it sent failed at the spool, not at the delivery. The caller can
// still read; what it must not do is pretend it can prune or clear.
var ErrReadOnly = errors.New("spool is read-only for this process")

// readOnly reports whether an open error means "we may read but not write".
func readOnly(err error) bool {
	return errors.Is(err, syscall.EROFS) || errors.Is(err, os.ErrPermission)
}

// Load is the DELIVERY path: it returns the entries to hand the agent and
// PRUNES the spool to match, so the caller must surface dropped (formatDigest
// does). Anything that only wants to look — a count, a status bar — must use
// Peek or Stat instead, or a mere glance silently deletes messages.
func Load(dir, agent string) ([]Entry, int, error) {
	file, err := os.OpenFile(path(dir, agent), os.O_RDWR, 0644)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if readOnly(err) {
		// Say WHICH failure this is, because the caller's choice depends on it:
		// pruning is impossible here, so a digest must not be delivered either
		// (it could never be cleared, and would repeat on every later message —
		// the duplicate class this queue was rebuilt to end).
		return nil, 0, fmt.Errorf("%w: %s", ErrReadOnly, path(dir, agent))
	}
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return nil, 0, err
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN) //nolint:errcheck

	live, total, err := scan(file)
	if err != nil {
		return nil, 0, err
	}
	entries, dropped := view(live, total)
	return entries, dropped, nil
}

// Peek is Load without the pruning: same age cutoff, same cap, same numbers —
// the file is left exactly as it was found. Use it wherever the drop count has
// nowhere to go, so that reading a queue can never shorten it.
//
// It takes a SHARED lock rather than none. Load and Clear rewrite in place
// (truncate, then write) under the exclusive lock, so an unlocked reader can
// catch the file mid-rewrite and report an empty or half-written spool. A
// shared lock excludes exactly those writers and nothing else: concurrent
// Peeks still run together, and no writer is ever blocked longer than one read.
func Peek(dir, agent string) ([]Entry, int, error) {
	file, err := os.Open(path(dir, agent))
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_SH); err != nil {
		return nil, 0, err
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN) //nolint:errcheck

	live, total, err := scan(file)
	if err != nil {
		return nil, 0, err
	}
	entries, dropped := view(live, total)
	return entries, dropped, nil
}

// Stat is Peek without the payload: items is how many entries a delivery would
// show right now, over how many it would drop. Neither number costs the spool
// anything.
func Stat(dir, agent string) (items int, over int, err error) {
	entries, dropped, err := Peek(dir, agent)
	return len(entries), dropped, err
}

func Clear(dir, agent string) error { return Acknowledge(dir, agent, nil) }

// Acknowledge removes only the delivered snapshot, preserving messages appended
// while delivery was in flight. Archive before clearing, under the spool lock.
func Acknowledge(dir, agent string, delivered []Entry) error {
	file, err := os.OpenFile(path(dir, agent), os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN) //nolint:errcheck
	entries, _, err := scan(file)
	if err != nil {
		return err
	}
	counts := map[Entry]int{}
	for _, entry := range delivered {
		counts[entry]++
	}
	var kept, acked []Entry
	for _, entry := range entries {
		if delivered == nil || counts[entry] > 0 {
			acked = append(acked, entry)
			counts[entry]--
		} else {
			kept = append(kept, entry)
		}
	}
	if len(acked) == 0 {
		return nil
	}
	history := filepath.Join(dir, "pending-history")
	if err := os.MkdirAll(history, 0755); err != nil {
		return err
	}
	archive, err := os.OpenFile(filepath.Join(history, agent+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(archive)
	for _, entry := range acked {
		if err = enc.Encode(entry); err != nil {
			break
		}
	}
	if err == nil {
		err = archive.Sync()
	}
	closeErr := archive.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return rewrite(file, kept)
}

func Counts(dir string) (agents int, items int, err error) {
	files, err := filepath.Glob(filepath.Join(dir, "pending", "*.jsonl"))
	if err != nil {
		return 0, 0, err
	}
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".jsonl")
		// Peek, never Load: bp q reports these numbers and has nowhere to put a
		// drop count, so pruning here would delete over-cap messages with nobody
		// told. Counting a queue must not empty it.
		count, _, statErr := Stat(dir, name)
		if statErr != nil {
			return 0, 0, fmt.Errorf("%s: %w", file, statErr)
		}
		if count > 0 {
			agents++
			items += count
		}
	}
	return agents, items, nil
}

func path(dir, agent string) string {
	return filepath.Join(dir, "pending", agent+".jsonl")
}

// scan reads the whole spool once: live is the records still inside maxAge,
// total is how many records are on disk. Both Load and Peek go through it so
// their views can never drift apart. Errors are returned rather than swallowed:
// a short read here would silently understate (or negate) the dropped total.
func scan(file *os.File) (live []Entry, total int, err error) {
	if _, err := file.Seek(0, 0); err != nil {
		return nil, 0, err
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxRecordBytes)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var entry Entry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return nil, 0, err
		}
		if err := validate(entry); err != nil {
			return nil, 0, err
		}
		total++
		live = append(live, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, err
	}
	return live, total, nil
}

// view applies the delivery view to a scanned spool: the newest maxItems live
// entries, plus how many records a delivery would drop (aged out and over cap).
func view(live []Entry, total int) ([]Entry, int) { return live, 0 }

func rewrite(file *os.File, entries []Entry) error {
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	for _, entry := range entries {
		if err := encoder.Encode(entry); err != nil {
			return err
		}
	}
	return file.Sync()
}

func validate(entry Entry) error {
	if err := messagetext.Sender(entry.From); err != nil {
		return err
	}
	return messagetext.Validate(entry.Text, entry.Kind)
}
