package main

import (
	"blueprint/internal/messagetext"
	bptmux "blueprint/internal/tmux"
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// List top-level native conversations across projects, never subagent files.
// Read bounded metadata for display; ownership is checked only after selection.
func claudeResumeSessions(projects string) ([]codexResumeSession, error) {
	paths, err := filepath.Glob(filepath.Join(projects, "*", "*.jsonl"))
	if err != nil {
		return nil, err
	}
	rows := map[string]codexResumeSession{}
	for _, path := range paths {
		id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		if !claudeResumeUUID.MatchString(id) {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			continue
		}
		row := codexResumeSession{ID: id, Title: id, Modified: info.ModTime()}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		scan := bufio.NewScanner(io.LimitReader(f, 1024*1024))
		scan.Buffer(make([]byte, 4096), 1024*1024)
		for lines := 0; lines < 200 && scan.Scan(); lines++ {
			var meta struct {
				CWD     string `json:"cwd"`
				Type    string `json:"type"`
				Message struct {
					Content json.RawMessage `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(scan.Bytes(), &meta) != nil {
				continue
			}
			if row.CWD == "" && filepath.IsAbs(meta.CWD) && messagetext.Label(meta.CWD) == nil {
				row.CWD = meta.CWD
			}
			if row.Title == id && meta.Type == "user" {
				var prompt string
				_ = json.Unmarshal(meta.Message.Content, &prompt)
				if prompt != "" {
					prompt = strings.Join(strings.Fields(prompt), " ")
					chars := []rune(prompt)
					if len(chars) > 90 {
						prompt = string(chars[:90]) + "…"
					}
					if messagetext.Label(prompt) == nil {
						row.Title = prompt
					}
				}
			}
		}
		_ = f.Close()
		if title, ok := bptmux.ReadCustomTitle(path); ok && messagetext.Label(title) == nil {
			row.Title = title
		}
		if old, ok := rows[id]; !ok || old.Modified.Before(row.Modified) {
			rows[id] = row
		}
	}
	result := make([]codexResumeSession, 0, len(rows))
	for _, row := range rows {
		result = append(result, row)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Modified.Equal(result[j].Modified) {
			return result[i].ID < result[j].ID
		}
		return result[i].Modified.After(result[j].Modified)
	})
	return result, nil
}
