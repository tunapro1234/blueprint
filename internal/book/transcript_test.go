package book

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTranscript lays out a Claude projects directory the way ResumeSessionPath
// expects it: <projectsRoot>/<munged cwd>/<session>.jsonl, titled with the agent
// name so it resolves to that agent.
func writeTranscript(t *testing.T, agent, folder string, records ...string) string {
	t.Helper()
	root := t.TempDir()
	munged := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		}
		return '-'
	}, folder)
	dir := filepath.Join(root, munged)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{fmt.Sprintf(`{"type":"custom-title","customTitle":%q,"sessionId":"sess-1"}`, agent)}
	lines = append(lines, records...)
	if err := os.WriteFile(filepath.Join(dir, "sess-1.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func userRecord(when time.Time, text string) string {
	return fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"role":"user","content":%q}}`,
		when.UTC().Format(time.RFC3339), text)
}

func TestTranscriptDeliveredFindsAnArrivedMessage(t *testing.T) {
	folder := "/srv/kavram-main"
	queued := time.Now().Add(-2 * time.Minute)
	message := "[server-main] roadmap incelemesi: hedef sistemi bolumunu bugun bitirelim\nikinci satir da var"
	root := writeTranscript(t, "kavram-main", folder,
		userRecord(queued.Add(-time.Hour), "cok daha onceki baska bir mesaj tamamen alakasiz"),
		userRecord(queued.Add(30*time.Second), message),
	)
	if !TranscriptDelivered(root, folder, "kavram-main", message, queued) {
		t.Fatal("a message that reached the transcript was not found")
	}
	// A multi-line message is stored with escaped newlines: the probe must handle
	// that, which the case above proves. Now the negative side.
	if TranscriptDelivered(root, folder, "kavram-main", "[server-main] bu mesaj hic gonderilmedi ve transcriptte yok", queued) {
		t.Fatal("a message that never arrived was reported as delivered")
	}
}

func TestTranscriptDeliveredIgnoresOlderCopies(t *testing.T) {
	// The same message really was sent before. A repeat must NOT be closed by its
	// own earlier copy, or the operator's second send is silently dropped.
	folder := "/srv/kavram-main"
	message := "[server-main] ayni mesaji bilerek tekrar gonderiyorum, ilki kayboldu"
	queued := time.Now()
	root := writeTranscript(t, "kavram-main", folder, userRecord(queued.Add(-2*time.Hour), message))
	if TranscriptDelivered(root, folder, "kavram-main", message, queued) {
		t.Fatal("an older copy closed a freshly queued record")
	}
}

func TestTranscriptDeliveredMatchesPastedCarriageReturns(t *testing.T) {
	// The regression that made this witness useless in practice (q163159804,
	// 2026-08-15): a message pasted through tmux arrives in the transcript with
	// its line breaks stored as \r, not \n, so EVERY multi-line message failed to
	// match its own record and the queue kept pasting it again. The record below
	// is written exactly as the live transcript wrote it — CR where the message
	// has LF.
	folder := "/srv/server-main"
	queued := time.Now().Add(-time.Minute)
	message := "[ders-main] tek mesaj uc kere teslim edildi, bunu yazdim.\n\nBu kaydin transcriptteki hali \\r tasiyor."
	stored := strings.ReplaceAll(message, "\n", "\r")
	root := writeTranscript(t, "server-main", folder, userRecord(queued.Add(10*time.Second), stored))
	if !TranscriptDelivered(root, folder, "server-main", message, queued) {
		t.Fatal("a delivered multi-line message stored with \\r was not recognised")
	}
	// The \n form must keep matching: a message typed/queued without going through
	// a bracketed paste is still recorded with escaped newlines.
	newlines := writeTranscript(t, "server-main", folder, userRecord(queued.Add(10*time.Second), message))
	if !TranscriptDelivered(newlines, folder, "server-main", message, queued) {
		t.Fatal("the escaped-newline form stopped matching")
	}
	// The variant must not make the witness careless: an unrelated message is
	// still not found.
	if TranscriptDelivered(root, folder, "server-main", "bambaska bir mesaj, hicbir yerde gecmiyor ve gecmemeli", queued) {
		t.Fatal("a message that never arrived was reported as delivered")
	}
}

func TestTranscriptDeliveredRefusesWhatItCannotProve(t *testing.T) {
	folder := "/srv/kavram-main"
	queued := time.Now().Add(-time.Minute)
	short := "/compact"
	root := writeTranscript(t, "kavram-main", folder,
		userRecord(queued.Add(time.Second), short),
		userRecord(queued.Add(time.Second), "zaman damgasiz kayitlar da olabilir"),
	)
	if TranscriptDelivered(root, folder, "kavram-main", short, queued) {
		t.Fatal("a message too short to identify was reported as delivered")
	}
	// No folder, unknown agent, and a record without a timestamp all read as "not
	// found", which falls back to delivering rather than dropping.
	if TranscriptDelivered(root, "", "kavram-main", "yeterince uzun bir mesaj metni burada", queued) {
		t.Fatal("a missing folder was reported as delivered")
	}
	if TranscriptDelivered(root, folder, "baska-agent", "yeterince uzun bir mesaj metni burada", queued) {
		t.Fatal("an unresolvable transcript was reported as delivered")
	}
	stampless := writeTranscript(t, "kavram-main", folder,
		`{"type":"user","message":{"role":"user","content":"zaman damgasi olmayan yeterince uzun bir kayit"}}`)
	if TranscriptDelivered(stampless, folder, "kavram-main", "zaman damgasi olmayan yeterince uzun bir kayit", queued) {
		t.Fatal("a record without a timestamp was accepted as proof")
	}
}

func TestRecentUserTextsReadsWhatTheAgentWasHanded(t *testing.T) {
	// The delivery-integrity view of a transcript: what arrived, and when. It must
	// return real deliveries only — a Task subagent's own prompts, the session-open
	// reminder, an interrupt marker and a tool result are not messages somebody sent
	// to this agent.
	folder := "/srv/probot/outreach"
	now := time.Now()
	root := writeTranscript(t, "probot-outreach", folder,
		userRecord(now.Add(-40*time.Hour), "[server-main] cok eski, penceresinin disinda"),
		userRecord(now.Add(-2*time.Hour), "[probot-business] BUSINESS → OUTREACH — birinci talimat"),
		`{"type":"user","isMeta":true,"timestamp":"`+now.UTC().Format(time.RFC3339)+`","message":{"role":"user","content":"<system-reminder> The user named this session"}}`,
		`{"type":"user","isSidechain":true,"timestamp":"`+now.UTC().Format(time.RFC3339)+`","message":{"role":"user","content":"subagent kendi promptu"}}`,
		`{"type":"user","timestamp":"`+now.UTC().Format(time.RFC3339)+`","message":{"role":"user","content":"[Request interrupted by user]"}}`,
		`{"type":"user","timestamp":"`+now.UTC().Format(time.RFC3339)+`","message":{"role":"user","content":[{"type":"tool_result","content":"grep cikti"}]}}`,
		`{"type":"assistant","timestamp":"`+now.UTC().Format(time.RFC3339)+`","message":{"role":"assistant","stop_reason":"end_turn","content":"cevap"}}`,
		userRecord(now.Add(-time.Minute), "[server-main] ikinci talimat"),
	)
	records := RecentUserTexts(root, folder, "probot-outreach", now.Add(-24*time.Hour))
	if len(records) != 2 {
		t.Fatalf("read %d deliveries, want 2: %+v", len(records), records)
	}
	if records[0].Text != "[probot-business] BUSINESS → OUTREACH — birinci talimat" || records[1].Text != "[server-main] ikinci talimat" {
		t.Fatalf("records=%+v", records)
	}
	if !records[0].Timestamp.Before(records[1].Timestamp) {
		t.Fatalf("records are not oldest first: %+v", records)
	}
	// Nothing resolvable (a Codex pane has no transcript at all) reads as nothing,
	// never as an error the caller has to handle.
	if got := RecentUserTexts(root, "", "probot-outreach", now.Add(-24*time.Hour)); got != nil {
		t.Fatalf("an unresolvable agent returned %+v", got)
	}
}

// The codex witness, against the shape a live rollout actually writes:
// {"timestamp":…,"type":"response_item","payload":{"type":"message","role":
// "user","content":[{"type":"input_text","text":…}]}}. Measured on
// probot-out-codex, 2026-08-25.
func TestCodexDeliveredReadsRollout(t *testing.T) {
	home := t.TempDir()
	folder := "/srv/probot/out-codex"
	day := filepath.Join(home, "sessions", "2026", "08", "25")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	message := "[blueprint] bp: INCIDENT bp-msg-enter-2026-08-25 — once acil kisim, mesajini yeniden gonder."
	sent := time.Now().Add(-10 * time.Minute)
	lines := []string{
		`{"timestamp":"` + sent.Add(-time.Hour).UTC().Format(time.RFC3339Nano) + `","type":"session_meta","payload":{"cwd":"` + folder + `"}}`,
		`{"timestamp":"` + sent.Add(time.Minute).UTC().Format(time.RFC3339Nano) + `","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":` + jsonString(message) + `}]}}`,
	}
	if err := os.WriteFile(filepath.Join(day, "rollout-2026-08-25T07-18-01-abc.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !CodexTranscriptExists(home, folder) {
		t.Fatal("a codex agent with a rollout was reported as having no record to wait for")
	}
	if !CodexDelivered(home, folder, message, sent) {
		t.Fatal("a message present in the rollout was not witnessed")
	}
	// A record written BEFORE the message was queued cannot settle it: that is
	// what keeps a deliberate re-send from finding its own earlier copy. The
	// margin clears transcriptClockSkew, which is deliberately tolerant.
	if CodexDelivered(home, folder, message, sent.Add(5*time.Minute)) {
		t.Fatal("an older rollout record settled a newer send")
	}
	if CodexDelivered(home, folder, "bambaska bir mesaj, yeterince uzun olsun diye", sent) {
		t.Fatal("a message that never arrived was witnessed")
	}
	// Another agent's folder must not borrow this rollout.
	if CodexDelivered(home, "/srv/probot/egitim-cx", message, sent) {
		t.Fatal("the witness read a rollout belonging to a different folder")
	}
}

func jsonString(s string) string {
	data, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(data)
}
