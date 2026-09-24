package book

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// NativeTitle is display metadata, not an agent identity or an authority pin.
// Offset keeps a rename valid after it has scrolled beyond the bounded tail.
type NativeTitle struct {
	ThreadID string `json:"threadId"`
	Path     string `json:"path"`
	Offset   int64  `json:"offset"`
	Text     string `json:"text"`
}

func ReadNativeTitle(path, id string, previous *NativeTitle) (NativeTitle, error) {
	return readNativeTitle(path, id, previous, false)
}

// ReadCodexNativeTitle reads the native append-only session name index by exact thread ID.
func ReadCodexNativeTitle(path, id string, previous *NativeTitle) (NativeTitle, error) {
	return readNativeTitle(path, id, previous, true)
}

// AppendCodexNativeTitle updates Codex's session name using its native
// append-only session_index.jsonl record format. It never truncates or rewrites
// the index file, and creates a missing index only when its parent directory
// already exists.
func AppendCodexNativeTitle(path, id, title string) (NativeTitle, error) {
	if id == "" || title == "" {
		return NativeTitle{}, fmt.Errorf("Codex thread ID and title are required")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return NativeTitle{}, fmt.Errorf("open Codex session index: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return NativeTitle{}, fmt.Errorf("stat Codex session index: %w", err)
	}
	if info.Size() > 0 {
		var last [1]byte
		if _, err := f.ReadAt(last[:], info.Size()-1); err != nil {
			return NativeTitle{}, fmt.Errorf("read Codex session index tail: %w", err)
		}
		if last[0] != '\n' {
			return NativeTitle{}, fmt.Errorf("Codex session index has an incomplete final record")
		}
	}

	record := struct {
		ID         string `json:"id"`
		ThreadName string `json:"thread_name"`
		UpdatedAt  string `json:"updated_at"`
	}{ID: id, ThreadName: title, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	line, err := json.Marshal(record)
	if err != nil {
		return NativeTitle{}, fmt.Errorf("encode Codex title record: %w", err)
	}
	line = append(line, '\n')
	n, err := f.Write(line)
	if err != nil {
		return NativeTitle{}, fmt.Errorf("append Codex title record: %w", err)
	}
	if n != len(line) {
		return NativeTitle{}, fmt.Errorf("append Codex title record: %w", io.ErrShortWrite)
	}
	if err := f.Sync(); err != nil {
		return NativeTitle{}, fmt.Errorf("sync Codex title record: %w", err)
	}

	value, err := ReadCodexNativeTitle(path, id, nil)
	if err != nil {
		return NativeTitle{}, fmt.Errorf("verify Codex title record: %w", err)
	}
	if value.Text != title {
		return NativeTitle{}, fmt.Errorf("Codex session index now reports %q, not %q", value.Text, title)
	}
	return value, nil
}

func readNativeTitle(path, id string, previous *NativeTitle, codex bool) (NativeTitle, error) {
	value := NativeTitle{ThreadID: id, Path: path}
	f, err := os.Open(path)
	if err != nil {
		return value, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return value, err
	}
	if previous != nil && previous.ThreadID == id && previous.Path == path && previous.Offset >= 0 && previous.Offset <= info.Size() {
		value = *previous
	}
	if _, err = f.Seek(value.Offset, io.SeekStart); err != nil {
		return value, err
	}
	r := bufio.NewReader(io.LimitReader(f, info.Size()-value.Offset))
	for {
		line, e := r.ReadBytes('\n')
		if e != nil {
			if e == io.EOF {
				return value, nil
			}
			return value, e
		}
		value.Offset += int64(len(line))
		if codex {
			var record struct {
				ID    string  `json:"id"`
				Title *string `json:"thread_name"`
			}
			if json.Unmarshal(line, &record) == nil && record.ID == id && record.Title != nil {
				value.Text = *record.Title
			}
			continue
		}
		if !bytes.Contains(line, []byte(`"custom-title"`)) {
			continue
		}
		var record struct {
			Type      string `json:"type"`
			Title     string `json:"customTitle"`
			SessionID string `json:"sessionId"`
			Sidechain bool   `json:"isSidechain"`
		}
		if json.Unmarshal(line, &record) == nil && record.Type == "custom-title" && !record.Sidechain && (record.SessionID == "" || record.SessionID == id) {
			value.Text = record.Title
		}
	}
}

func SetNativeTitle(paths []string, name string, value NativeTitle) error {
	return mutate(paths, name, func(agent map[string]any) bool {
		old, _ := json.Marshal(agent["nativeTitle"])
		var previous NativeTitle
		if json.Unmarshal(old, &previous) == nil && previous == value {
			return false
		}
		agent["nativeTitle"] = value
		return true
	})
}
