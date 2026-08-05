package pending

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	maxAge   = 7 * 24 * time.Hour
	maxItems = 20
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

func Load(dir, agent string) ([]Entry, int, error) {
	file, err := os.OpenFile(path(dir, agent), os.O_RDWR, 0644)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return nil, 0, err
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN) //nolint:errcheck

	cutoff := time.Now().Add(-maxAge).Unix()
	var entries []Entry
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
		if entry.TS >= cutoff {
			entries = append(entries, entry)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, err
	}
	total, err := countLines(file)
	if err != nil {
		return nil, 0, err
	}
	dropped := total - len(entries)
	if len(entries) > maxItems {
		dropped += len(entries) - maxItems
		entries = entries[len(entries)-maxItems:]
	}
	if err := rewrite(file, entries); err != nil {
		return nil, 0, err
	}
	return entries, dropped, nil
}

func Clear(dir, agent string) error {
	file, err := os.OpenFile(path(dir, agent), os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN) //nolint:errcheck
	return rewrite(file, nil)
}

func Counts(dir string) (agents int, items int, err error) {
	files, err := filepath.Glob(filepath.Join(dir, "pending", "*.jsonl"))
	if err != nil {
		return 0, 0, err
	}
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".jsonl")
		entries, _, loadErr := Load(dir, name)
		if loadErr != nil {
			return 0, 0, fmt.Errorf("%s: %w", file, loadErr)
		}
		if len(entries) > 0 {
			agents++
			items += len(entries)
		}
	}
	return agents, items, nil
}

func path(dir, agent string) string {
	return filepath.Join(dir, "pending", agent+".jsonl")
}

// countLines counts the records currently on disk so Load can report how many
// it is about to drop. Its error is returned rather than swallowed: a short
// count here would silently understate (or negate) the dropped total.
func countLines(file *os.File) (int, error) {
	if _, err := file.Seek(0, 0); err != nil {
		return 0, err
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxRecordBytes)
	count := 0
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) != "" {
			count++
		}
	}
	return count, scanner.Err()
}

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
