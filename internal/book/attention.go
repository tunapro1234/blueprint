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
		if cache.DefinitelyOtherCompactRecordType(line, "assistant", "user") {
			return true
		}
		if !bytes.HasPrefix(bytes.TrimLeft(line, " \t"), []byte("{")) {
			return true
		}
		if len(line) > 4*1024 && !cache.HasCompactRecordTypePrefix(line, "assistant", "user") && !cache.MayHaveRecordType(line, "assistant", "user") {
			return true
		}
		record, ok := decodeTurnRecord(line)
		if !ok || record.IsSidechain {
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
		if cache.DefinitelyOtherCompactRecordType(line, "event_msg", "response_item") {
			return true
		}
		if len(line) > 4*1024 && !cache.HasCompactRecordTypePrefix(line, "event_msg", "response_item") && !cache.MayHaveRecordType(line, "event_msg", "response_item") {
			return true
		}
		record, ok := cache.DecodeCodexRecord(line)
		if !ok {
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
