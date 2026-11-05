package book

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Trimmed from server-3dprinter on 2026-11-04 (Claude Code 2.1.318). Claude Code
// writes "api-request" when it sends a request, just before the prompt line.
const (
	recAPIRequest       = `{"type":"api-request","sessionId":"sess-1","querySource":"repl_main_thread","timestamp":"2026-11-04T10:29:16.043Z"}`
	recTaskNotification = `{"type":"user","isSidechain":false,"message":{"role":"user","content":"<task-notification>\n<task-id>biihve8bz</task-id>\n<status>killed</status>\n</task-notification>"},"timestamp":"2026-11-04T10:15:47.386Z"}`
)

// unsentFixture writes records stamped at their own ages (record, age pairs).
func unsentFixture(t *testing.T, agent, folder string, rows ...any) string {
	t.Helper()
	now := time.Now()
	var records []string
	for i := 0; i < len(rows); i += 2 {
		records = append(records, restamp(rows[i].(string), now.Add(-rows[i+1].(time.Duration))))
	}
	root := writeTranscript(t, agent, folder, records...)
	matches, err := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("fixture layout: %v %v", matches, err)
	}
	fresh := now.Add(-time.Second)
	if err := os.Chtimes(matches[0], fresh, fresh); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestTurnOpenUnsentPromptAfterResume(t *testing.T) {
	const folder, agent = "/srv/3dprinter", "server-3dprinter"
	// The earlier turn sent its request and ended; after the resume Claude Code
	// wrote a task notification and never sent it.
	earlier := []any{recAPIRequest, 3 * time.Hour, recPrompt, 3 * time.Hour, recAnswer, 3 * time.Hour, recTurnDone, 3 * time.Hour}

	stale := unsentFixture(t, agent, folder, append(earlier, recTaskNotification, 13*time.Minute)...)
	if TurnOpen(stale, folder, agent, time.Now()) {
		t.Fatal("a prompt with no API request for 13 minutes read as a working turn")
	}
	young := unsentFixture(t, agent, folder, append(earlier, recTaskNotification, 30*time.Second)...)
	if !TurnOpen(young, folder, agent, time.Now()) {
		t.Fatal("a 30-second-old prompt must still read as a turn starting")
	}
	sent := unsentFixture(t, agent, folder, append(earlier, recAPIRequest, 13*time.Minute-time.Second, recTaskNotification, 13*time.Minute)...)
	if !TurnOpen(sent, folder, agent, time.Now()) {
		t.Fatal("a prompt whose request was sent (written before it in file order) must read as open")
	}
}

func TestTurnOpenUnsentRuleNeedsRequestRecords(t *testing.T) {
	const folder, agent = "/srv/3dprinter", "server-3dprinter"
	// An older Claude Code writes no api-request records: a long-thinking turn
	// looks exactly like this and must stay open.
	root := unsentFixture(t, agent, folder, recAnswer, time.Hour, recTurnDone, time.Hour, recPrompt, 10*time.Minute)
	if !TurnOpen(root, folder, agent, time.Now()) {
		t.Fatal("without api-request records the prompt must read as open")
	}
	// A tool result is answered mid-turn; no request after it is not "unsent".
	tool := unsentFixture(t, agent, folder, recAPIRequest, 11*time.Minute, recPrompt, 11*time.Minute, recToolUse, 10*time.Minute, recToolResult, 10*time.Minute)
	if !TurnOpen(tool, folder, agent, time.Now()) {
		t.Fatal("a tool still running must read as open")
	}
}
