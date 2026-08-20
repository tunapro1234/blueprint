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
	// truncatedRecord is the 12:53:36 delivery: a "DUR/IPTAL" message whose envelope
	// lost its opening bracket, with the tail of an earlier digest glued underneath.
	truncatedRecord = "\nt-business] BUSINESS → OUTREACH — DUR, onceki mesajimi IPTAL ET: Tuna 'codex kullanma, bekle' dedi." +
		"\no2 birikmis duyuru — 16-17 Agu]\r1) (16 Agu 16:41, server-main) yeni mail: logo boxes\r\r[probot-business] BUSINESS → OUTREACH — Tuna 'codex ok' dedi."
	// piggybackRecord is the 10:43:17 delivery, and it is NOT damaged: bp composes a
	// pending-announcement digest and the message into one paste on purpose
	// (formatDigest + "\n\n[" + sender + "] " + message), which is why the digest
	// shape alone must never raise an alarm.
	piggybackRecord = "[2 birikmis duyuru — 16-17 Agu]\r1) (16 Agu 16:41, server-main) yeni mail: logo boxes" +
		"\r2) (17 Agu 09:17, server-main) re zamani: Teknokta Akademi\r\r[probot-business] BUSINESS → OUTREACH — Tuna 'codex ok' dedi, login geldi."
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
		{"two envelopes in one record", "[probot-business] birinci mesaj\n\n[server-main] ikinci mesaj", true},
		{"ordinary envelope", "[server-main] tek mesaj, tek gonderen\nikinci satiri da var", false},
		{"bare slash command", "/compact", false},
		{"prose with a bracket in it", "3] maddesini de ekledim, listeye bak", false},
		{"quoted envelope inside a line", "sana gelen mesaj soyleydi: [ada] bunu yap", false},
		// The detector's first real output was a false alarm on this shape
		// (probot-studio, 2026-08-20): a sender writing a numbered list. "[1]"
		// is not an agent, and itemized reports are everyday traffic.
		{"numbered list is not a second envelope", "[probot-tracking] probot-studio: TUNA ONAYI GELDI - UYGULAMA PAKETI (4 parca). Oncelik sirasiyla:\n[1] WIREFRAME-ADA KAYIT akisi\n[2] olcum paneli\n[3] geri bildirim\n[4] yayina alma", false},
		// A shape-valid name nobody answers to is prose, not a delivery: only
		// agentbook membership makes a candidate an envelope.
		{"unknown name is not an envelope", "[server-main] mesaj govdesi\n[not] bu bir uyari etiketi\n[ornek] bu da prose", false},
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
	if rows[0].To != "server-main" || !strings.Contains(rows[0].Msg, "probot-outreach") || !strings.Contains(rows[0].Msg, "kirpik") {
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
