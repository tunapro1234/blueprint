package identity

import (
	"strings"
	"testing"
)

func TestCompactionNoteCarriesTheMarkerAndAgentName(t *testing.T) {
	note := CompactionNote("probot-outreach")
	if !strings.HasPrefix(note, CompactionNoteMarker) {
		t.Fatalf("note does not start with the marker:\n%s", note)
	}
	if !strings.Contains(note, `"probot-outreach"`) {
		t.Fatalf("note does not name the agent:\n%s", note)
	}
	if !strings.Contains(note, "bp msg") || !strings.Contains(note, "bp status") {
		t.Fatalf("note is missing the how-to-keep-talking instructions:\n%s", note)
	}
}
