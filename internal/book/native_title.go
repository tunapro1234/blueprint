package book

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
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
