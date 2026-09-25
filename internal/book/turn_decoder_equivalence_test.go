package book

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTurnDecoderMatchesLegacyFixtureAndSyntheticLargeRow(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "status-transcript.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	large := `{"type":"file-history-snapshot","content":"` + strings.Repeat("x", 128*1024) + `quoted {\"type\":\"assistant\"}"}` + "\n"
	cases := map[string][]byte{
		"transcript fixture":  fixture,
		"large unrelated row": append(append([]byte(nil), fixture...), []byte(large)...),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "transcript.jsonl")
			if err := os.WriteFile(path, content, 0600); err != nil {
				t.Fatal(err)
			}
			newVerdict, newStamp, newErr := scanTurnPhase(path)
			oldVerdict, oldStamp, oldErr := legacyTurnPhase(path)
			if newVerdict != oldVerdict || !newStamp.Equal(oldStamp) || errorText(newErr) != errorText(oldErr) {
				t.Fatalf("new=(%d,%s,%v) legacy=(%d,%s,%v)", newVerdict, newStamp, newErr, oldVerdict, oldStamp, oldErr)
			}
		})
	}
}

func TestTurnRecordDecodeIsSharedAcrossStatusScanners(t *testing.T) {
	path := filepath.Join("testdata", "status-transcript.jsonl")
	turnRecordDecodes.Lock()
	turnRecordDecodes.entries = map[uint64][]turnRecordDecodeEntry{}
	turnRecordDecodes.bytes = 0
	turnRecordDecodes.Unlock()
	originalUnmarshal := unmarshalTurnRecord
	decodeCount := 0
	unmarshalTurnRecord = func(data []byte, target any) error {
		decodeCount++
		return json.Unmarshal(data, target)
	}
	t.Cleanup(func() { unmarshalTurnRecord = originalUnmarshal })
	if _, _, err := scanTurnPhase(path); err != nil {
		t.Fatal(err)
	}
	afterPhaseScan := decodeCount
	_, _ = awaitingClaude(path)
	if decodeCount != afterPhaseScan {
		t.Fatalf("awaiting scan decoded %d additional rows after phase scan (%d to %d)", decodeCount-afterPhaseScan, afterPhaseScan, decodeCount)
	}
}

func legacyTurnPhase(path string) (int, time.Time, error) {
	file, err := os.Open(path)
	if err != nil {
		return turnNone, time.Time{}, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	verdict, stamp := turnNone, time.Time{}
	var manualCompactAt time.Time
	compactSummary := false
	for scanner.Scan() {
		line := scanner.Bytes()
		var row turnRecord
		if json.Unmarshal(line, &row) == nil && !row.IsSidechain {
			ts, _ := time.Parse(time.RFC3339Nano, row.Timestamp)
			if row.Type == "system" && row.Subtype == "compact_boundary" {
				manualCompactAt, compactSummary = time.Time{}, false
				if row.CompactMetadata.Trigger == "manual" {
					manualCompactAt = ts
				}
			} else if !manualCompactAt.IsZero() {
				if row.Type == "user" && row.IsCompactSummary {
					compactSummary = true
				}
				if compactSummary && row.Type == "user" && !row.IsMeta && !ts.Before(manualCompactAt) && compactCommandCompleted(row.Message.Content) {
					verdict, stamp = turnClosedVerdict, ts
					manualCompactAt = time.Time{}
					continue
				}
				if v, _ := legacyClassifyTurnRecord(line); v != turnNone && ts.After(manualCompactAt) {
					manualCompactAt = time.Time{}
				}
			}
		}
		if v, ts := legacyClassifyTurnRecord(line); v != turnNone {
			verdict = v
			stamp, _ = time.Parse(time.RFC3339Nano, ts)
			var row turnRecord
			if json.Unmarshal(line, &row) == nil && row.Type == "assistant" && v == turnOpenVerdict && row.Message.StopReason != "tool_use" {
				verdict = turnUncertainVerdict
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return turnNone, time.Time{}, err
	}
	return verdict, stamp, nil
}

func legacyClassifyTurnRecord(line []byte) (int, string) {
	if !strings.HasPrefix(strings.TrimLeft(string(line), " \t"), "{") {
		return turnNone, ""
	}
	var record turnRecord
	if json.Unmarshal(line, &record) != nil || record.IsSidechain {
		return turnNone, ""
	}
	switch record.Type {
	case "system":
		if record.Subtype == "turn_duration" {
			return turnClosedVerdict, record.Timestamp
		}
	case "user":
		if record.InterruptedMessageID != "" || interruptedText(record.Message.Content) {
			return turnClosedVerdict, record.Timestamp
		}
		if !record.IsMeta && !record.IsCompactSummary && !localCommandEcho(record.Message.Content) {
			return turnOpenVerdict, record.Timestamp
		}
	case "assistant":
		if turnEndingStop[record.Message.StopReason] {
			return turnClosedVerdict, record.Timestamp
		}
		return turnOpenVerdict, record.Timestamp
	}
	return turnNone, ""
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
