package delivery

import (
	"testing"

	"blueprint/internal/msgq"
	"blueprint/internal/pending"
)

func TestAnnounceDeferrerSpoolsOnlyAColdTarget(t *testing.T) {
	stateDir := t.TempDir()
	warm := map[string]bool{"worker-1": true}
	deferCold := AnnounceDeferrer(stateDir, func(name string) bool { return warm[name] })
	message := msgq.Message{ID: "q1", To: "worker-1", From: "lead", Msg: AnnouncePrefix("lead") + "release freeze at noon", TS: 1795000000.5, Kind: msgq.KindAnnounce, NotBefore: 1795000010}
	if spooled, err := deferCold(message); err != nil || spooled {
		t.Fatalf("warm target: spooled=%v err=%v", spooled, err)
	}
	if snapshot, err := pending.Load(stateDir, "worker-1"); err != nil || len(snapshot.Entries) != 0 {
		t.Fatalf("warm target spool=%+v err=%v", snapshot.Entries, err)
	}
	message.To = "worker-2"
	if spooled, err := deferCold(message); err != nil || !spooled {
		t.Fatalf("cold target: spooled=%v err=%v", spooled, err)
	}
	snapshot, err := pending.Load(stateDir, "worker-2")
	if err != nil || len(snapshot.Entries) != 1 {
		t.Fatalf("cold target spool=%+v err=%v", snapshot.Entries, err)
	}
	entry := snapshot.Entries[0]
	if entry.Text != "release freeze at noon" || entry.From != "lead" || entry.Kind != "announce" || entry.TS != 1795000000 {
		t.Fatalf("spool entry=%+v", entry)
	}
}
