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
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			shape := mergedShape(testCase.text)
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
		deliveryRecord(now.Add(-30*time.Hour), truncatedRecord), // outside the window: history
		deliveryRecord(now.Add(-2*time.Hour), piggybackRecord),  // by design, never an alarm
		deliveryRecord(now.Add(-time.Hour), truncatedRecord),
	)
	state := busySanityState{}
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
