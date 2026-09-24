package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blueprint/internal/book"
	"blueprint/internal/config"
	"blueprint/internal/msgq"
)

// The two shapes are quoted from the records that were actually measured on
// probot-outreach (2026-08-17), carriage returns included: a bracketed paste's line
// breaks are stored as \r, and a detector that does not fold them sees one long line.
const (
	// truncatedRecord is the 12:53:36 delivery: a "STOP/CANCEL" message whose envelope
	// lost its opening bracket, with the tail of an earlier digest glued underneath.
	truncatedRecord = "\nt-business] BUSINESS → OUTREACH — STOP, CANCEL my previous message: Tuna said 'do not use Codex; wait'." +
		"\no2 accumulated announcements — 16-17 Aug]\r1) (16 Aug 16:41, server-main) new mail: logo boxes\r\r[probot-business] BUSINESS → OUTREACH — Tuna said 'Codex is okay'."
	// piggybackRecord is the 10:43:17 delivery, and it is NOT damaged: bp composes a
	// pending-announcement digest and the message into one paste on purpose
	// (formatDigest + "\n\n[" + sender + "] " + message), which is why the digest
	// shape alone must never raise an alarm.
	piggybackRecord = "[2 accumulated announcements — 16-17 Aug]\r1) (16 Aug 16:41, server-main) new mail: logo boxes" +
		"\r2) (17 Aug 09:17, server-main) regarding: Teknokta Akademi\r\r[probot-business] BUSINESS → OUTREACH — Tuna said 'Codex is okay'; login arrived."
)

func TestMergedShapeCatchesOnlyMeasuredDamage(t *testing.T) {
	known := map[string]bool{"probot-business": true, "server-main": true, "probot-tracking": true, "ada": true}
	cases := []struct {
		name  string
		text  string
		alarm bool
	}{
		{"truncated envelope", truncatedRecord, true},
		{"digest piggybacked by design", piggybackRecord, false},
		{"two envelopes in one record", "[probot-business] first message\n\n[server-main] second message", true},
		{"ordinary envelope", "[server-main] one message, one sender\nthere is a second line too", false},
		{"bare slash command", "/compact", false},
		{"prose with a bracket in it", "I added item 3] too; check the list", false},
		{"quoted envelope inside a line", "the message you received was: [ada] do this", false},
		// The detector's first real output was a false alarm on this shape
		// (probot-studio, 2026-08-20): a sender writing a numbered list. "[1]"
		// is not an agent, and itemized reports are everyday traffic.
		{"numbered list is not a second envelope", "[probot-tracking] probot-studio: TUNA APPROVED - IMPLEMENTATION PACKAGE (4 parts). In priority order:\n[1] WIREFRAME-ADA REGISTRATION flow\n[2] measurement panel\n[3] feedback\n[4] deployment", false},
		// A shape-valid name nobody answers to is prose, not a delivery: only
		// agentbook membership makes a candidate an envelope.
		{"unknown name is not an envelope", "[server-main] message body\n[not] this is a warning label\n[example] this is prose too", false},
		// The measured handover shape (probot-business 15:15, 2026-08-21): an
		// agent forwarding history verbatim, the quoted original starting with
		// its own envelope. Nine of these fired the alarm in one day; envelopes
		// below a quotation marker are somebody's history, not this delivery.
		{"handover quoting an original message", "[server-whatsapp] HANDOVER: the business-card task is moving to you.\n--- ORIGINAL TEXT (17 Aug) ---\n[probot-business] probot-main PLAN: business cards will be printed Friday", false},
		{"handover with the rule flowed mid-line", "[server-whatsapp] HANDOVER summary --- ORIGINAL TEXT (17 Aug) ---\n[probot-business] plan text here", false},
		{"quoted lines are history too", "[server-main] evaluate this message:\n> [probot-business] old instruction text", false},
		// A raw two-message merge carries no separator between the envelopes —
		// the suppression must not blind the detector to it.
		{"two envelopes with no separator still alarm", "[probot-business] body of the first message\n\n[server-main] second message attached to it", true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			shape := mergedShape(testCase.text, known)
			if (shape != "") != testCase.alarm {
				t.Fatalf("mergedShape = %q, want alarm=%v", shape, testCase.alarm)
			}
		})
	}
}

// mergeService builds a Service whose fleet, transcripts and queue all live in
// temporary directories, so a sweep can be run without a tmux or a real agent.
func mergeService(t *testing.T, agent, folder string, records ...string) (*Service, *msgq.Queue) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	munged := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		}
		return '-'
	}, folder)
	dir := filepath.Join(home, ".claude", "projects", munged)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{fmt.Sprintf(`{"type":"custom-title","customTitle":%q,"sessionId":"sess-1"}`, agent)}
	lines = append(lines, records...)
	if err := os.WriteFile(filepath.Join(dir, "sess-1.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bookPath := filepath.Join(t.TempDir(), "agentbook.json")
	data, err := json.Marshal(book.File{Agents: []book.Agent{{Name: agent, Folder: folder, Status: "open"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bookPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTBOOK", bookPath)
	queue := msgq.New(t.TempDir())
	service := &Service{
		config: config.Config{Agentbooks: []string{bookPath}, StateDir: t.TempDir()},
		queue:  queue,
		log:    log.New(io.Discard, "", 0),
	}
	return service, queue
}

func deliveryRecord(when time.Time, text string) string {
	return fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"role":"user","content":%q}}`,
		when.UTC().Format(time.RFC3339), text)
}

func TestMergeScanReportsADamagedDeliveryExactlyOnce(t *testing.T) {
	now := time.Now()
	service, queue := mergeService(t, "probot-outreach", "/srv/probot/outreach",
		deliveryRecord(now.Add(-30*time.Hour), truncatedRecord), // before the watermark: history
		deliveryRecord(now.Add(-2*time.Hour), piggybackRecord),  // by design, never an alarm
		deliveryRecord(now.Add(-time.Hour), truncatedRecord),
	)
	state := busySanityState{MergeWatermark: now.Add(-3 * time.Hour).UTC().Format(time.RFC3339)}
	service.mergeScan([]string{"probot-outreach"}, &state, now)
	rows, err := queue.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("queued %d notices, want 1: %+v", len(rows), rows)
	}
	if rows[0].To != "server-main" || !strings.Contains(rows[0].Msg, "probot-outreach") || !strings.Contains(rows[0].Msg, "torn") {
		t.Fatalf("notice=%+v", rows[0])
	}
	if len(state.MergeSeen) != 1 {
		t.Fatalf("seen=%v", state.MergeSeen)
	}
	// The same sweep an hour later must stay silent: an alarm that repeats every hour
	// is an alarm nobody reads.
	service.mergeScan([]string{"probot-outreach"}, &state, now.Add(time.Hour))
	rows, err = queue.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("the same record was reported again: %+v", rows)
	}
}

// The watermark is what turns the detector's silence into information. Its first
// sweep must set the baseline and report NOTHING — the archive was examined by
// whoever shipped the fix, and a chronic condition reported as an incident is how
// alarms stop being read (server-main, 2026-08-17: the detector's very first
// notice was the pre-deploy 12:53 record, already investigated for hours).
func TestMergeScanFirstSweepBaselinesAndStaysSilent(t *testing.T) {
	now := time.Now()
	service, queue := mergeService(t, "probot-outreach", "/srv/probot/outreach",
		deliveryRecord(now.Add(-time.Hour), truncatedRecord), // damaged, but historical
	)
	state := busySanityState{}
	service.mergeScan([]string{"probot-outreach"}, &state, now)
	if rows, err := queue.List(); err != nil || len(rows) != 0 {
		t.Fatalf("the baseline sweep reported history: rows=%v err=%v", rows, err)
	}
	if state.MergeWatermark == "" {
		t.Fatal("the baseline sweep did not set the watermark")
	}
	// A record NEWER than the baseline is an incident and must be reported.
	second := now.Add(time.Hour)
	appendRecord(t, "/srv/probot/outreach", deliveryRecord(second.Add(-time.Minute), truncatedRecord))
	service.mergeScan([]string{"probot-outreach"}, &state, second)
	if rows, err := queue.List(); err != nil || len(rows) != 1 {
		t.Fatalf("a fresh damaged delivery was not reported: rows=%v err=%v", rows, err)
	}
}

func appendRecord(t *testing.T, folder, record string) {
	t.Helper()
	munged := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		}
		return '-'
	}, folder)
	path := filepath.Join(os.Getenv("HOME"), ".claude", "projects", munged, "sess-1.jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(record + "\n"); err != nil {
		t.Fatal(err)
	}
}

func TestMergeScanKeepsItsMemoryBounded(t *testing.T) {
	state := busySanityState{}
	for index := 0; index < mergeSeenMax+50; index++ {
		state.MergeSeen = append(state.MergeSeen, fmt.Sprintf("agent@%d", index))
	}
	service, _ := mergeService(t, "agent", "/srv/agent")
	service.mergeScan(nil, &state, time.Now())
	if len(state.MergeSeen) != mergeSeenMax {
		t.Fatalf("memory is unbounded: %d keys", len(state.MergeSeen))
	}
	if state.MergeSeen[len(state.MergeSeen)-1] != fmt.Sprintf("agent@%d", mergeSeenMax+49) {
		t.Fatalf("the newest keys were dropped: %v", state.MergeSeen[len(state.MergeSeen)-3:])
	}
}
