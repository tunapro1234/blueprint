package msgq

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bptmux "blueprint/internal/tmux"
)

// eraseTarget adds the pane check and the chip erase qclear needs to the fake.
type eraseTarget struct {
	*fakeTarget
	notAgent   bool
	chipKeeps  bool
	chipErases int
}

func (e *eraseTarget) RequireAgentPane(context.Context, string) error {
	if e.notAgent {
		return bptmux.ErrNotAgent
	}
	return nil
}

func (e *eraseTarget) ClearStuckChip(_ context.Context, _ string, text string) (bool, error) {
	if bptmux.Busy(e.pane) || !bptmux.ClaudeChipFor(e.pane, text) {
		return false, nil
	}
	e.chipErases++
	if e.chipKeeps {
		return false, bptmux.ErrNotCleared
	}
	e.pane = composerPane("")
	return true, nil
}

func (e *eraseTarget) keys() int { return len(e.cleared) + e.chipErases }

const ownChip = "[Pasted text #21 +2 lines]"

func allowAll(Message) error { return nil }

// leftoverQueue holds one record bp pasted unverified into "target"; closed,
// when set, is the status it was closed with.
func leftoverQueue(t *testing.T, now *time.Time, text, closed string) (*Queue, string) {
	t.Helper()
	q := heldQueue(t, now)
	q.Witness = func(string, string, time.Time) bool { return false }
	q.TurnOpen = func(string) bool { return false }
	id, err := enqueueBoundUnverified(t, q, "target", "sender", text)
	if err != nil {
		t.Fatal(err)
	}
	if closed != "" {
		path := filepath.Join(q.pending(), id+".json")
		message, err := read(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := q.finish(path, message, closed); err != nil {
			t.Fatal(err)
		}
	}
	return q, id
}

func clearLeftover(t *testing.T, q *Queue, target Target, id string, dryRun bool) LeftoverResult {
	t.Helper()
	result, err := q.ClearLeftover(context.Background(), target, id, LeftoverOptions{DryRun: dryRun, Actor: "operator", Authorize: allowAll})
	if err != nil {
		t.Fatalf("ClearLeftover: %v (%+v)", err, result)
	}
	return result
}

func doneRecord(t *testing.T, q *Queue, id string) Message {
	t.Helper()
	message, err := read(filepath.Join(q.done(), id+".json"))
	if err != nil {
		t.Fatalf("%s is not closed: %v", id, err)
	}
	return message
}

func logStatuses(t *testing.T, q *Queue, id string) []string {
	t.Helper()
	f, err := os.Open(MessageLogPath(q.Root))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var statuses []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var entry LogEntry
		if json.Unmarshal(scanner.Bytes(), &entry) == nil && entry.ID == id {
			statuses = append(statuses, entry.Status)
		}
	}
	return statuses
}

func TestQclearErasesAVerbatimLeftoverAndRewritesTheClosedRecord(t *testing.T) {
	now := time.Date(2026, 11, 28, 20, 0, 0, 0, time.Local)
	q, id := leftoverQueue(t, &now, witnessable, StatusUnconfirmed)
	now = now.Add(time.Hour)
	target := &eraseTarget{fakeTarget: &fakeTarget{alive: true, pane: deepPane(witnessable)}}
	result := clearLeftover(t, q, target, id, false)
	if result.Outcome != LeftoverCleared || result.Chip || result.Pending {
		t.Fatalf("result=%+v", result)
	}
	if len(target.cleared) != 1 || target.chipErases != 0 || len(target.submitted) != 0 || len(target.sent) != 0 {
		t.Fatalf("keys: cleared=%v chip=%d submitted=%v sent=%v", target.cleared, target.chipErases, target.submitted, target.sent)
	}
	if !bptmux.ComposerEmpty(target.pane) {
		t.Fatalf("composer not empty: %s", target.pane)
	}
	record := doneRecord(t, q, id)
	if record.Status != StatusLeftoverCleared || !strings.Contains(record.Reason, "record was "+StatusUnconfirmed) || !strings.Contains(record.Reason, "operator") {
		t.Fatalf("record=%+v", record)
	}
	status, err := q.Status(id)
	if err != nil || !strings.HasPrefix(status, "CANCELED (LEFTOVER PASTE CLEARED BY OPERATOR)") {
		t.Fatalf("qstat=%q err=%v", status, err)
	}
	if got := logStatuses(t, q, id); len(got) != 2 || got[0] != StatusUnconfirmed || got[1] != StatusLeftoverCleared {
		t.Fatalf("message log statuses=%v, want the old and the new final state in order", got)
	}
	// A second run finds nothing left to do and presses nothing.
	again := clearLeftover(t, q, target, id, false)
	if failed, ok := again.Failed(); again.Outcome != LeftoverRefused || !ok || failed.Name != "pasted by bp" {
		t.Fatalf("second run=%+v", again)
	}
	if target.keys() != 1 {
		t.Fatalf("keys after the second run=%d", target.keys())
	}
}

func TestQclearErasesItsOwnChipWithOneKey(t *testing.T) {
	now := time.Date(2026, 11, 28, 20, 0, 0, 0, time.Local)
	q, id := leftoverQueue(t, &now, chipText, StatusHangingComposer)
	target := &eraseTarget{fakeTarget: &fakeTarget{alive: true, pane: deepPane(ownChip)}}
	result := clearLeftover(t, q, target, id, false)
	if result.Outcome != LeftoverCleared || !result.Chip {
		t.Fatalf("result=%+v", result)
	}
	if target.chipErases != 1 || len(target.cleared) != 0 || len(target.submitted) != 0 || target.chipEnters != 0 {
		t.Fatalf("chip erases=%d cleared=%v submitted=%v enters=%d", target.chipErases, target.cleared, target.submitted, target.chipEnters)
	}
	if status, _ := q.Finished(id); status != StatusLeftoverCleared {
		t.Fatalf("status=%q", status)
	}
}

func TestQclearClosesAPendingRecordAsCanceled(t *testing.T) {
	now := time.Date(2026, 11, 28, 20, 0, 0, 0, time.Local)
	q, id := leftoverQueue(t, &now, witnessable, "")
	target := &eraseTarget{fakeTarget: &fakeTarget{alive: true, pane: deepPane(witnessable)}}
	result := clearLeftover(t, q, target, id, false)
	if result.Outcome != LeftoverCleared || !result.Pending {
		t.Fatalf("result=%+v", result)
	}
	if _, err := os.Stat(filepath.Join(q.pending(), id+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending record still there: %v", err)
	}
	if record := doneRecord(t, q, id); record.Status != StatusLeftoverCleared || !strings.Contains(record.Reason, "record was pending") {
		t.Fatalf("record=%+v", record)
	}
	// It is never delivered later.
	now = now.Add(time.Hour)
	dispatchOnce(t, q, target.fakeTarget)
	if target.calls != 0 || len(target.submitted) != 0 || len(target.sent) != 0 {
		t.Fatalf("a later pass touched the message: %+v", target.fakeTarget)
	}
	if got := logStatuses(t, q, id); len(got) != 1 || got[0] != StatusLeftoverCleared {
		t.Fatalf("message log statuses=%v", got)
	}
}

func TestQclearDryRunReportsEveryCheckAndPressesNothing(t *testing.T) {
	for _, tc := range []struct {
		name, text, pane string
		chip             bool
	}{
		{"verbatim", witnessable, deepPane(witnessable), false},
		{"chip", chipText, deepPane(ownChip), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 11, 28, 20, 0, 0, 0, time.Local)
			q, id := leftoverQueue(t, &now, tc.text, StatusUnconfirmed)
			target := &eraseTarget{fakeTarget: &fakeTarget{alive: true, pane: tc.pane}}
			result := clearLeftover(t, q, target, id, true)
			if result.Outcome != LeftoverDryRun || result.Chip != tc.chip || !strings.Contains(result.Detail, "would press C-u") {
				t.Fatalf("result=%+v", result)
			}
			want := []string{"record", "authority", "pasted by bp", "same session", "transcript witness", "no open turn",
				"agent pane", "no dialog", "idle", "composer complete", "composer holds this message"}
			if len(result.Checks) != len(want) {
				t.Fatalf("checks=%+v", result.Checks)
			}
			for i, check := range result.Checks {
				if check.Name != want[i] || !check.OK {
					t.Fatalf("check %d=%+v, want %s passing", i, check, want[i])
				}
			}
			if target.keys() != 0 {
				t.Fatalf("dry run pressed keys: %d", target.keys())
			}
			if record := doneRecord(t, q, id); record.Status != StatusUnconfirmed {
				t.Fatalf("dry run changed the record: %+v", record)
			}
		})
	}
}

func TestQclearLeavesADeliveredMessageAlone(t *testing.T) {
	now := time.Date(2026, 11, 28, 20, 0, 0, 0, time.Local)
	q, id := leftoverQueue(t, &now, witnessable, StatusUnconfirmed)
	q.Witness = func(_, text string, _ time.Time) bool { return text == witnessable }
	target := &eraseTarget{fakeTarget: &fakeTarget{alive: true, pane: deepPane(witnessable)}}
	result := clearLeftover(t, q, target, id, false)
	if result.Outcome != LeftoverDelivered || result.Detail != "already delivered, nothing to clear" {
		t.Fatalf("result=%+v", result)
	}
	if target.keys() != 0 {
		t.Fatalf("keys pressed: %d", target.keys())
	}
	if record := doneRecord(t, q, id); record.Status != StatusUnconfirmed {
		t.Fatalf("record changed: %+v", record)
	}
}

func TestQclearSaysSoWhenTheComposerIsNotEmptyAfterwards(t *testing.T) {
	for _, tc := range []struct {
		name, text, pane string
		setup            func(*eraseTarget)
	}{
		{"verbatim", witnessable, deepPane(witnessable), func(e *eraseTarget) { e.clearErr = errors.New("composer clear stopped") }},
		{"chip", chipText, deepPane(ownChip), func(e *eraseTarget) { e.chipKeeps = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 11, 28, 20, 0, 0, 0, time.Local)
			q, id := leftoverQueue(t, &now, tc.text, StatusHangingComposer)
			target := &eraseTarget{fakeTarget: &fakeTarget{alive: true, pane: tc.pane}}
			tc.setup(target)
			result := clearLeftover(t, q, target, id, false)
			if result.Outcome != LeftoverNotCleared || !strings.Contains(result.Detail, "NOT empty after the erase; nothing more was pressed") {
				t.Fatalf("result=%+v", result)
			}
			if target.keys() != 1 {
				t.Fatalf("erase calls=%d, want exactly one", target.keys())
			}
			if record := doneRecord(t, q, id); record.Status != StatusHangingComposer {
				t.Fatalf("record changed: %+v", record)
			}
			if got := logStatuses(t, q, id); len(got) != 1 {
				t.Fatalf("message log statuses=%v", got)
			}
		})
	}
}

func TestQclearRefusesWithoutAKeyAndNamesTheCheck(t *testing.T) {
	dialog := strings.Replace(deepPane(witnessable), "  -- INSERT -- ⏵⏵ bypass permissions on (shift+tab to cycle)",
		"  Enter to confirm · Esc to cancel", 1)
	scrolled := strings.Join(strings.Split(composerPane(witnessable), "\n")[2:], "\n")
	cases := []struct {
		name   string
		text   string
		closed string
		pane   string
		id     string
		setup  func(*Queue, *eraseTarget, *time.Time)
		auth   func(Message) error
		failed string
	}{
		{name: "unknown id", id: "q000000001", failed: "record"},
		{name: "not authorized", auth: func(Message) error { return errors.New("not the sender") }, failed: "authority"},
		{name: "closed as delivered", closed: "delivered (found in transcript)", failed: "pasted by bp"},
		{name: "closed torn", closed: "not delivered (torn paste)", failed: "pasted by bp"},
		{name: "closed over a day ago", setup: func(_ *Queue, _ *eraseTarget, now *time.Time) { *now = now.Add(25 * time.Hour) }, failed: "pasted by bp"},
		{name: "binding changed", setup: func(q *Queue, _ *eraseTarget, _ *time.Time) {
			q.Binding = func(string) string { return "claude:other-thread:200" }
		}, failed: "same session"},
		{name: "no witness", setup: func(q *Queue, _ *eraseTarget, _ *time.Time) { q.Witness = nil }, failed: "transcript witness"},
		{name: "turn open", setup: func(q *Queue, _ *eraseTarget, _ *time.Time) { q.TurnOpen = func(string) bool { return true } }, failed: "no open turn"},
		{name: "not an agent pane", setup: func(_ *Queue, e *eraseTarget, _ *time.Time) { e.notAgent = true }, failed: "agent pane"},
		{name: "dialog", pane: dialog, failed: "no dialog"},
		{name: "busy", pane: busyComposerPane(witnessable), failed: "idle"},
		{name: "scrolled composer", pane: scrolled, failed: "composer complete"},
		{name: "foreign text", pane: deepPane("a draft somebody is writing"), failed: "composer holds this message"},
		{name: "message plus more", pane: deepPane(witnessable + " and more"), failed: "composer holds this message"},
		{name: "empty composer", pane: deepPane(""), failed: "composer holds this message"},
		{name: "chip with another count", text: chipText, pane: deepPane("[Pasted text #21 +5 lines]"), failed: "composer holds this message"},
		{name: "chip and text", text: chipText, pane: deepPane(ownChip + " mine"), failed: "composer holds this message"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 11, 28, 20, 0, 0, 0, time.Local)
			text, closed, pane := witnessable, StatusUnconfirmed, deepPane(witnessable)
			if tc.text != "" {
				text = tc.text
			}
			if tc.closed != "" {
				closed = tc.closed
			}
			if tc.pane != "" {
				pane = tc.pane
			}
			q, id := leftoverQueue(t, &now, text, closed)
			if tc.id != "" {
				id = tc.id
			}
			target := &eraseTarget{fakeTarget: &fakeTarget{alive: true, pane: pane}}
			if tc.setup != nil {
				tc.setup(q, target, &now)
			}
			auth := allowAll
			if tc.auth != nil {
				auth = tc.auth
			}
			result, err := q.ClearLeftover(context.Background(), target, id, LeftoverOptions{Authorize: auth})
			if err != nil {
				t.Fatal(err)
			}
			failed, ok := result.Failed()
			if result.Outcome != LeftoverRefused || !ok || failed.Name != tc.failed || !strings.Contains(result.Detail, "refused: "+tc.failed) {
				t.Fatalf("result=%+v, want refused at %q", result, tc.failed)
			}
			if target.keys() != 0 || len(target.submitted) != 0 || target.chipEnters != 0 {
				t.Fatalf("keys pressed on a refusal: %+v", target)
			}
			if tc.id == "" {
				if record := doneRecord(t, q, id); record.Status != closed {
					t.Fatalf("record changed: %+v", record)
				}
			}
		})
	}
}

func TestQclearWaitsOutTheScreenSettleGraceOnAPendingRecord(t *testing.T) {
	now := time.Date(2026, 11, 28, 20, 0, 0, 0, time.Local)
	q, id := leftoverQueue(t, &now, witnessable, "")
	path := filepath.Join(q.pending(), id+".json")
	record := pendingRecord(t, q, id)
	record.UnverifiedAt = float64(now.UnixNano()) / 1e9
	if err := writePending(path, record); err != nil {
		t.Fatal(err)
	}
	target := &eraseTarget{fakeTarget: &fakeTarget{alive: true, pane: deepPane(witnessable)}}
	now = now.Add(5 * time.Second)
	result := clearLeftover(t, q, target, id, false)
	if failed, ok := result.Failed(); result.Outcome != LeftoverRefused || !ok || failed.Name != "pasted by bp" || !strings.Contains(failed.Detail, "retry in") {
		t.Fatalf("result=%+v", result)
	}
	if target.keys() != 0 {
		t.Fatalf("keys=%d", target.keys())
	}
	now = now.Add(screenSettleGrace)
	if result := clearLeftover(t, q, target, id, false); result.Outcome != LeftoverCleared {
		t.Fatalf("after the grace: %+v", result)
	}
}

func TestQclearRefusesAPendingRecordBpNeverPastedUnverified(t *testing.T) {
	now := time.Date(2026, 11, 28, 20, 0, 0, 0, time.Local)
	q := heldQueue(t, &now)
	q.Witness = func(string, string, time.Time) bool { return false }
	q.TurnOpen = func(string) bool { return false }
	id, err := q.Enqueue("target", "sender", witnessable)
	if err != nil {
		t.Fatal(err)
	}
	target := &eraseTarget{fakeTarget: &fakeTarget{alive: true, pane: deepPane(witnessable)}}
	result := clearLeftover(t, q, target, id, false)
	if failed, ok := result.Failed(); result.Outcome != LeftoverRefused || !ok || failed.Name != "pasted by bp" || !strings.Contains(failed.Detail, "bp qcancel") {
		t.Fatalf("result=%+v", result)
	}
	if target.keys() != 0 {
		t.Fatalf("keys=%d", target.keys())
	}
	if record := pendingRecord(t, q, id); record.Status != "" {
		t.Fatalf("record=%+v", record)
	}
}
