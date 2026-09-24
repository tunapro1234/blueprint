package book

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"blueprint/internal/cache"
)

// AwaitingUser reports whether the most recent completed conversational turn
// handed control back to the user. The timestamp is the reply time used for
// unread tracking. Unknown or mid-turn transcripts return false.
func AwaitingUser(path, runtime string) (bool, time.Time) {
	switch runtime {
	case "claude":
		return awaitingClaude(path)
	case "codex", "codex-remote":
		return awaitingCodex(path)
	default:
		return false, time.Time{}
	}
}

func awaitingClaude(path string) (bool, time.Time) {
	awaiting, latestKnown := false, false
	var repliedAt time.Time
	cache.ScanCodexReverse(path, func(line []byte) bool {
		if !bytes.HasPrefix(bytes.TrimLeft(line, " \t"), []byte("{")) {
			return true
		}
		var record turnRecord
		if json.Unmarshal(line, &record) != nil || record.IsSidechain {
			return true
		}
		ts, _ := time.Parse(time.RFC3339Nano, record.Timestamp)
		switch record.Type {
		case "assistant":
			ended := turnEndingStop[record.Message.StopReason]
			if !latestKnown {
				awaiting, latestKnown = ended, true
			}
			if ended && repliedAt.IsZero() {
				repliedAt = ts
				return false
			}
		case "user":
			if record.IsMeta || record.IsCompactSummary || localCommandEcho(record.Message.Content) || toolResultOnly(record.Message.Content) {
				return true
			}
			if !latestKnown {
				awaiting, latestKnown = false, true
			}
		}
		return true
	})
	return awaiting, repliedAt
}

func toolResultOnly(content json.RawMessage) bool {
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(content, &blocks) != nil || len(blocks) == 0 {
		return false
	}
	for _, block := range blocks {
		if block.Type != "tool_result" {
			return false
		}
	}
	return true
}

func awaitingCodex(path string) (bool, time.Time) {
	completed := false
	awaiting, latestKnown := false, false
	var repliedAt time.Time
	cache.ScanCodexReverse(path, func(line []byte) bool {
		var record struct {
			Type      string    `json:"type"`
			Timestamp time.Time `json:"timestamp"`
			Payload   struct {
				Type    string          `json:"type"`
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &record) != nil {
			return true
		}
		if record.Type == "event_msg" {
			switch record.Payload.Type {
			case "task_complete":
				completed = true
			case "task_started", "turn_aborted":
				if !latestKnown {
					awaiting, latestKnown = false, true
				}
			}
			return true
		}
		if record.Type != "response_item" || record.Payload.Type != "message" {
			return true
		}
		switch strings.ToLower(record.Payload.Role) {
		case "assistant":
			if !latestKnown {
				awaiting, latestKnown = completed, true
			}
			if completed && repliedAt.IsZero() {
				repliedAt = record.Timestamp
				return false
			}
		case "user":
			if !latestKnown {
				awaiting, latestKnown = false, true
			}
		}
		return true
	})
	return awaiting, repliedAt
}
