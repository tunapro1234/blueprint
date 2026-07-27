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
)

type Entry struct {
	TS   int64  `json:"ts"`
	From string `json:"from"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

func Append(dir, agent string, entry Entry) error {
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
	return json.NewEncoder(file).Encode(entry)
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
	dropped := countLines(file) - len(entries)
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

func countLines(file *os.File) int {
	_, _ = file.Seek(0, 0)
	scanner := bufio.NewScanner(file)
	count := 0
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) != "" {
			count++
		}
	}
	return count
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
