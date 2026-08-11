package book

import (
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
