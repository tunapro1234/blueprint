package daemon

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blueprint/internal/config"
	"blueprint/internal/msgq"
)

func TestBlockedQueueFindingsThresholdAndContent(t *testing.T) {
	obs := []blockedObservation{
		{Session: "fresh", ID: "q1", Reason: "composer contains an unreadable paste (chip)", Age: blockedQueueThreshold - time.Minute, Count: 1},
		{Session: "stuck", ID: "q2", Reason: "composer contains an unreadable paste (chip)", Age: blockedQueueThreshold + time.Minute, Count: 3},
	}
	findings := blockedQueueFindings(obs)
	if _, ok := findings["fresh"]; ok {
		t.Fatalf("a record below the threshold must not be a finding: %v", findings)
	}
	msg, ok := findings["stuck"]
	if !ok {
		t.Fatalf("a record past the threshold must be a finding: %v", findings)
	}
	for _, want := range []string{"stuck", "q2", "unreadable paste (chip)", "3 message(s)", "bp peek stuck", "bp qstat q2"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("finding %q missing %q", msg, want)
		}
	}
}

// blockedService builds a daemon with a real queue and a discarded log, enough to
// exercise the scan's gating without tmux or an agentbook.
func blockedService(t *testing.T) (*Service, *msgq.Queue, string) {
	t.Helper()
	root := t.TempDir()
	queue := msgq.New(root)
	service := &Service{
		config: config.Config{StateDir: t.TempDir()},
		queue:  queue,
		log:    log.New(io.Discard, "", 0),
	}
	return service, queue, root
}

// enqueueStuck lays down one pending record and patches its on-disk ts/reason/
// nextTry, which Enqueue alone cannot set, so a test can place a record exactly
// where a dispatch skip would have left it.
func enqueueStuck(t *testing.T, root string, q *msgq.Queue, to, reason string, age time.Duration, nextTry time.Time, now time.Time) string {
	t.Helper()
	id, err := q.Enqueue(to, "lead", "message for "+to)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "pending", id+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["ts"] = float64(now.Add(-age).UnixNano()) / 1e9
	if reason != "" {
		m["reason"] = reason
	}
	if !nextTry.IsZero() {
		m["nextTry"] = float64(nextTry.UnixNano()) / 1e9
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
	return id
}

func serverMainNotices(t *testing.T, q *msgq.Queue) []msgq.Message {
	t.Helper()
	rows, err := q.List()
	if err != nil {
		t.Fatal(err)
	}
	var notices []msgq.Message
	for _, r := range rows {
		if r.To == "server-main" {
			notices = append(notices, r)
		}
	}
	return notices
}

func TestBlockedQueueScanAlarmsIdleAgentOnce(t *testing.T) {
	now := time.Now()
	service, queue, root := blockedService(t)
	enqueueStuck(t, root, queue, "bp-term", "composer contains an unreadable paste (chip)", blockedQueueThreshold+5*time.Minute, time.Time{}, now)

	observations := []paneObservation{{Session: "bp-term", Open: true, IsAgent: true}}
	busy := map[string]bool{"bp-term": false}
	state := busySanityState{}

	service.blockedQueueScan(observations, busy, &state, now)
	notices := serverMainNotices(t, queue)
	if len(notices) != 1 {
		t.Fatalf("want exactly one alarm, got %d: %+v", len(notices), notices)
	}
	if !strings.Contains(notices[0].Msg, "bp-term") || !strings.Contains(notices[0].Msg, "unreadable paste") {
		t.Fatalf("alarm text=%q", notices[0].Msg)
	}
	if _, ok := state.BlockedQueueReported["bp-term"]; !ok {
		t.Fatalf("the block must be stamped so it is not repeated: %v", state.BlockedQueueReported)
	}

	// A second sweep inside the cooldown stays silent.
	service.blockedQueueScan(observations, busy, &state, now.Add(time.Hour))
	if got := len(serverMainNotices(t, queue)); got != 1 {
		t.Fatalf("cooldown breached: %d alarms after a second sweep", got)
	}
}

func TestBlockedQueueScanRespectsGates(t *testing.T) {
	now := time.Now()

	cases := []struct {
		name    string
		busy    bool
		open    bool
		isAgent bool
		reason  string
		age     time.Duration
		nextTry time.Time
		alarm   bool
	}{
		{name: "busy agent holds its queue legitimately", busy: true, open: true, isAgent: true, reason: "chip", age: blockedQueueThreshold + time.Minute, alarm: false},
		{name: "not recognised as an agent is panesanity's case", busy: false, open: true, isAgent: false, reason: "chip", age: blockedQueueThreshold + time.Minute, alarm: false},
		{name: "not open in the book", busy: false, open: false, isAgent: true, reason: "chip", age: blockedQueueThreshold + time.Minute, alarm: false},
		{name: "head never tried has no reason", busy: false, open: true, isAgent: true, reason: "", age: blockedQueueThreshold + time.Minute, alarm: false},
		{name: "below the threshold is not yet stuck", busy: false, open: true, isAgent: true, reason: "chip", age: blockedQueueThreshold - time.Minute, alarm: false},
		{name: "scheduled backoff is waiting on a clock", busy: false, open: true, isAgent: true, reason: "usage limit", age: blockedQueueThreshold + time.Minute, nextTry: now.Add(time.Hour), alarm: false},
		{name: "idle with an aged skip reason is stuck", busy: false, open: true, isAgent: true, reason: "chip", age: blockedQueueThreshold + time.Minute, alarm: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service, queue, root := blockedService(t)
			enqueueStuck(t, root, queue, "agent", tc.reason, tc.age, tc.nextTry, now)
			observations := []paneObservation{{Session: "agent", Open: tc.open, IsAgent: tc.isAgent}}
			state := busySanityState{}
			service.blockedQueueScan(observations, map[string]bool{"agent": tc.busy}, &state, now)
			got := len(serverMainNotices(t, queue)) > 0
			if got != tc.alarm {
				t.Fatalf("alarm=%v, want %v", got, tc.alarm)
			}
		})
	}
}

func TestBlockedQueueReportedClearsWhenQueueDrains(t *testing.T) {
	now := time.Now()
	service, queue, root := blockedService(t)
	enqueueStuck(t, root, queue, "agent", "chip", blockedQueueThreshold+time.Minute, time.Time{}, now)
	observations := []paneObservation{{Session: "agent", Open: true, IsAgent: true}}
	busy := map[string]bool{"agent": false}

	state := busySanityState{}
	service.blockedQueueScan(observations, busy, &state, now)
	if _, ok := state.BlockedQueueReported["agent"]; !ok {
		t.Fatalf("expected the block to be stamped")
	}

	// The queue drains: a sweep that finds nothing stuck must forget the stamp so a
	// later relapse alarms again, exactly like paneSanity's reported map.
	_, emptyQueue, _ := blockedService(t)
	service.queue = emptyQueue // no pending records for "agent"
	service.blockedQueueScan(observations, busy, &state, now.Add(2*blockedQueueCooldown))
	if _, ok := state.BlockedQueueReported["agent"]; ok {
		t.Fatalf("a drained queue must drop the stamp so a relapse can alarm again: %v", state.BlockedQueueReported)
	}
}
