package daemon

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"
)

// One finding goes out exactly as before: the same text, no count line.
func TestAlarmFindingsSingleKeepsText(t *testing.T) {
	service, queue, _ := blockedService(t)
	service.alarmFindings("pane-sanity", []string{"bp: one finding"})
	notices := serverMainNotices(t, queue)
	if len(notices) != 1 || notices[0].Msg != "bp: one finding" {
		t.Fatalf("notices=%+v; want the single finding unchanged", notices)
	}
	service.alarmFindings("pane-sanity", nil)
	if got := len(serverMainNotices(t, queue)); got != 1 {
		t.Fatalf("no findings queued a message: %d notices", got)
	}
}

// A sweep of a hundred unrecognised panes is one message to the coordinator,
// every finding is logged, and every finding gets its cooldown stamp.
func TestPaneSanityScanFoldsSweepIntoOneMessage(t *testing.T) {
	service, queue, _ := blockedService(t)
	var logs bytes.Buffer
	service.log = log.New(&logs, "", 0)
	var observations []paneObservation
	for i := 0; i < 100; i++ {
		observations = append(observations, paneObservation{Session: fmt.Sprintf("agent-%03d", i), Open: true, Command: "node"})
	}
	state := busySanityState{}
	now := time.Now()
	service.paneSanityScan(observations, &state, now)

	notices := serverMainNotices(t, queue)
	if len(notices) != 1 {
		t.Fatalf("want one message for the sweep, got %d", len(notices))
	}
	lines := strings.Split(notices[0].Msg, "\n")
	if lines[0] != "pane-sanity: 100 findings in this sweep" {
		t.Fatalf("first line=%q", lines[0])
	}
	if len(lines) != 1+alarmBatchMaxLines+1 {
		t.Fatalf("message has %d lines, want count + %d findings + overflow", len(lines), alarmBatchMaxLines)
	}
	for i, line := range lines[1 : 1+alarmBatchMaxLines] {
		if !strings.Contains(line, fmt.Sprintf("pane agent-%03d ", i)) {
			t.Fatalf("line %d=%q; want findings in stable session order", i+1, line)
		}
	}
	if last := lines[len(lines)-1]; last != "... and 60 more, see the daemon log (journalctl -u blueprint.service)" {
		t.Fatalf("overflow line=%q", last)
	}
	if got := strings.Count(logs.String(), "is NOT RECOGNIZED AS AN AGENT"); got != 100 {
		t.Fatalf("log holds %d findings, want all 100", got)
	}
	if got := len(state.PaneSanityReported); got != 100 {
		t.Fatalf("cooldown stamped for %d findings, want all 100", got)
	}

	// Inside the cooldown the next sweep is silent for all of them.
	service.paneSanityScan(observations, &state, now.Add(time.Minute))
	if got := len(serverMainNotices(t, queue)); got != 1 {
		t.Fatalf("cooldown breached: %d messages", got)
	}
}

func TestBlockedQueueScanFoldsSweepIntoOneMessage(t *testing.T) {
	now := time.Now()
	service, queue, root := blockedService(t)
	var logs bytes.Buffer
	service.log = log.New(&logs, "", 0)
	names := []string{"charlie", "alpha", "bravo"}
	var observations []paneObservation
	busy := map[string]bool{}
	for _, name := range names {
		enqueueStuck(t, root, queue, name, "composer contains an unreadable paste (chip)", blockedQueueThreshold+5*time.Minute, time.Time{}, now)
		observations = append(observations, paneObservation{Session: name, Open: true, IsAgent: true})
		busy[name] = false
	}
	state := busySanityState{}
	service.blockedQueueScan(observations, busy, &state, now)

	notices := serverMainNotices(t, queue)
	if len(notices) != 1 {
		t.Fatalf("want one message for the sweep, got %d", len(notices))
	}
	lines := strings.Split(notices[0].Msg, "\n")
	if len(lines) != 4 || lines[0] != "blocked-queue: 3 findings in this sweep" {
		t.Fatalf("message=%q", notices[0].Msg)
	}
	for i, name := range []string{"alpha", "bravo", "charlie"} {
		if !strings.HasPrefix(lines[i+1], "bp: "+name+" is idle") {
			t.Fatalf("line %d=%q; want %s in stable order", i+1, lines[i+1], name)
		}
		if _, ok := state.BlockedQueueReported[name]; !ok {
			t.Fatalf("%s not stamped: %v", name, state.BlockedQueueReported)
		}
	}
	if got := strings.Count(logs.String(), "is idle but message"); got != 3 {
		t.Fatalf("log holds %d findings, want 3", got)
	}
}

// Long findings hit the byte budget before the line cap.
func TestAlarmBatchTextCapsBytes(t *testing.T) {
	long := strings.Repeat("x", 1000)
	var findings []string
	for i := 0; i < 30; i++ {
		findings = append(findings, fmt.Sprintf("bp: %02d %s", i, long))
	}
	text := alarmBatchText("blocked-queue", findings)
	if len(text) > alarmBatchMaxBytes+200 {
		t.Fatalf("message is %d bytes; want about %d at most", len(text), alarmBatchMaxBytes)
	}
	lines := strings.Split(text, "\n")
	shown := len(lines) - 2
	if shown <= 0 || shown >= 30 || lines[len(lines)-1] != fmt.Sprintf("... and %d more, see the daemon log (journalctl -u blueprint.service)", 30-shown) {
		t.Fatalf("shown=%d last=%q", shown, lines[len(lines)-1])
	}
	// A finding with embedded newlines stays one line in the folded message.
	if got := alarmBatchText("pane-sanity", []string{"a\nb", "c"}); got != "pane-sanity: 2 findings in this sweep\na b\nc" {
		t.Fatalf("folded=%q", got)
	}
}
