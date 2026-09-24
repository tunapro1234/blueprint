package pending

import (
	"blueprint/internal/messagetext"
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"
)

const (
	// maxRecordBytes limits newly appended records. Legacy records over this size
	// are kept as held raw lines so they cannot block later records or be lost.
	maxRecordBytes = 8 << 20
)

const (
	ReasonAnonymousSender = "sender identity unavailable; anonymous delivery blocked"
	ReasonUnsafeText      = "unsafe text"
	ReasonUnparseable     = "unparseable record"
)

type Entry struct {
	TS   int64  `json:"ts"`
	From string `json:"from"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type HeldRecord struct {
	Line   int
	Reason string
	Raw    []byte
}

// Snapshot contains only deliverable entries; held records remain on disk and
// retain their raw bytes so a later acknowledgement cannot erase them.
type Snapshot struct {
	Entries []Entry
	Dropped int
	Held    []HeldRecord
}

type Status struct {
	Items   int
	Dropped int
	Held    []HeldRecord
}

type SpoolStatus struct {
	Agent   string
	Path    string
	Items   int
	Dropped int
	Held    []HeldRecord
	Err     error
}

type CountSummary struct {
	Agents  int
	Items   int
	Dropped int
	Held    int
	Spools  []SpoolStatus
}

type spoolRecord struct {
	Entry Entry
	Held  *HeldRecord
}

type scanResult struct {
	records []spoolRecord
	entries []Entry
	held    []HeldRecord
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
	file, err := os.OpenFile(path(dir, agent), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN) //nolint:errcheck
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 0 {
		last := []byte{0}
		if _, err := file.ReadAt(last, info.Size()-1); err != nil {
			return err
		}
		if last[0] != '\n' {
			if _, err := file.Write([]byte{'\n'}); err != nil {
				return err
			}
		}
	}
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

// Load is the delivery path: it returns valid entries and reports held legacy
// records separately. The caller acknowledges only records whose delivery was
// handled; held bytes remain untouched.
func Load(dir, agent string) (Snapshot, error) {
	file, err := os.OpenFile(path(dir, agent), os.O_RDWR, 0644)
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, nil
	}
	if readOnly(err) {
		// Say WHICH failure this is, because the caller's choice depends on it:
		// pruning is impossible here, so a digest must not be delivered either
		// (it could never be cleared, and would repeat on every later message —
		// the duplicate class this queue was rebuilt to end).
		return Snapshot{}, fmt.Errorf("%w: %s", ErrReadOnly, path(dir, agent))
	}
	if err != nil {
		return Snapshot{}, err
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return Snapshot{}, err
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN) //nolint:errcheck

	result, err := scan(file)
	if err != nil {
		return Snapshot{}, err
	}
	entries, dropped := view(result.entries, len(result.entries))
	return Snapshot{Entries: entries, Dropped: dropped, Held: result.held}, nil
}

// Peek returns the same delivery view as Load without acknowledging anything;
// the file is left exactly as it was found.
//
// It takes a SHARED lock rather than none. Acknowledge rewrites in place
// (truncate, then write) under the exclusive lock, so an unlocked reader can
// catch the file mid-rewrite and report an empty or half-written spool. A
// shared lock excludes exactly those writers and nothing else: concurrent
// Peeks still run together, and no writer is ever blocked longer than one read.
func Peek(dir, agent string) (Snapshot, error) {
	file, err := os.Open(path(dir, agent))
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, nil
	}
	if err != nil {
		return Snapshot{}, err
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_SH); err != nil {
		return Snapshot{}, err
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN) //nolint:errcheck

	result, err := scan(file)
	if err != nil {
		return Snapshot{}, err
	}
	entries, dropped := view(result.entries, len(result.entries))
	return Snapshot{Entries: entries, Dropped: dropped, Held: result.held}, nil
}

// Stat is Peek without valid message payloads. Held records remain available
// to callers that need to report them.
func Stat(dir, agent string) (Status, error) {
	snapshot, err := Peek(dir, agent)
	if err != nil {
		return Status{}, err
	}
	return Status{Items: len(snapshot.Entries), Dropped: snapshot.Dropped, Held: snapshot.Held}, nil
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
	result, err := scan(file)
	if err != nil {
		return err
	}
	counts := map[Entry]int{}
	for _, entry := range delivered {
		counts[entry]++
	}
	var kept []spoolRecord
	var acked []Entry
	for _, record := range result.records {
		if record.Held != nil {
			kept = append(kept, record)
			continue
		}
		entry := record.Entry
		if delivered == nil || counts[entry] > 0 {
			acked = append(acked, entry)
			counts[entry]--
		} else {
			kept = append(kept, record)
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

func Counts(dir string) (CountSummary, error) {
	files, err := filepath.Glob(filepath.Join(dir, "pending", "*.jsonl"))
	if err != nil {
		return CountSummary{}, err
	}
	var summary CountSummary
	var errs []error
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".jsonl")
		// Peek, never Load: bp q reports these numbers and has nowhere to put a
		// drop count, so pruning here would delete over-cap messages with nobody
		// told. Counting a queue must not empty it.
		status, statErr := Stat(dir, name)
		spool := SpoolStatus{Agent: name, Path: file}
		if statErr != nil {
			spool.Err = fmt.Errorf("%s: %w", file, statErr)
			errs = append(errs, spool.Err)
			summary.Spools = append(summary.Spools, spool)
			continue
		}
		spool.Items, spool.Dropped, spool.Held = status.Items, status.Dropped, status.Held
		summary.Spools = append(summary.Spools, spool)
		summary.Items += status.Items
		summary.Dropped += status.Dropped
		summary.Held += len(status.Held)
		if status.Items > 0 || status.Dropped > 0 || len(status.Held) > 0 {
			summary.Agents++
		}
	}
	return summary, errors.Join(errs...)
}

func path(dir, agent string) string {
	return filepath.Join(dir, "pending", agent+".jsonl")
}

// scan reads every line independently. A malformed legacy record is held with
// its exact bytes, so it cannot block later valid entries or disappear during
// acknowledgement.
func scan(file *os.File) (scanResult, error) {
	var result scanResult
	if _, err := file.Seek(0, 0); err != nil {
		return scanResult{}, err
	}
	reader := bufio.NewReaderSize(file, 64*1024)
	lineNo := 0
	for {
		raw, readErr := reader.ReadBytes('\n')
		if len(raw) > 0 {
			lineNo++
			line := bytes.TrimSuffix(raw, []byte{'\n'})
			if len(bytes.TrimSpace(line)) == 0 {
				// Blank lines carry no record; they are skipped as before and
				// compacted away by the next rewrite.
				if readErr != nil {
					if errors.Is(readErr, io.EOF) {
						break
					}
					return scanResult{}, readErr
				}
				continue
			}
			reason := ""
			var entry Entry
			switch {
			case !utf8.Valid(line):
				reason = ReasonUnparseable
			case json.Unmarshal(line, &entry) != nil:
				reason = ReasonUnparseable
			default:
				if err := validate(entry); err != nil {
					if strings.Contains(err.Error(), "sender identity unavailable") {
						reason = ReasonAnonymousSender
					} else {
						reason = ReasonUnsafeText
					}
				}
			}
			if reason != "" {
				held := HeldRecord{Line: lineNo, Reason: reason, Raw: append([]byte(nil), raw...)}
				result.held = append(result.held, held)
				result.records = append(result.records, spoolRecord{Held: &held})
			} else {
				result.entries = append(result.entries, entry)
				result.records = append(result.records, spoolRecord{Entry: entry})
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return scanResult{}, readErr
		}
	}
	return result, nil
}

// view applies the delivery view to the valid entries in a scanned spool.
// Held records are deliberately excluded from the delivery and drop counts.
func view(live []Entry, total int) ([]Entry, int) { return live, 0 }

func rewrite(file *os.File, records []spoolRecord) error {
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	for i, record := range records {
		if record.Held != nil {
			if _, err := file.Write(record.Held.Raw); err != nil {
				return err
			}
			if i+1 < len(records) && (len(record.Held.Raw) == 0 || record.Held.Raw[len(record.Held.Raw)-1] != '\n') {
				if _, err := file.Write([]byte{'\n'}); err != nil {
					return err
				}
			}
			continue
		}
		if err := json.NewEncoder(file).Encode(record.Entry); err != nil {
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
