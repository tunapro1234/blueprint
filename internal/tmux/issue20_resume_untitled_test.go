package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #20/#22: `bp open --resume --thread <id>` refused a conversation whose
// native title was empty ("session pin … missing or title differs"), leaving
// auto-named sessions and never-renamed coordinators with no bp-managed resume.
func TestIssue20_ExplicitPinResumesUntitledConversation(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "-work")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	untitled := "4bd47c4f-e853-4eff-bdd0-f3b8444531f8"
	other := "f9e902f0-1111-1111-1111-111111111111"
	write := func(id, body string) {
		if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(untitled, "{\"type\":\"user\",\"message\":{\"content\":\"hi\"}}\n")
	write(other, "{\"type\":\"custom-title\",\"customTitle\":\"someone-else\"}\n")

	if p, err := ResolveSessionPath(root, "/work", "main", untitled); err != nil || !strings.HasSuffix(p, untitled+".jsonl") {
		t.Fatalf("untitled explicit pin refused: %q %v", p, err)
	}
	// Another agent's title is still protected.
	if _, err := ResolveSessionPath(root, "/work", "main", other); err == nil {
		t.Fatal("a conversation titled for another agent must still refuse")
	}
	// Without an explicit id an untitled conversation is not guessed.
	if _, err := ResolveSessionPath(root, "/work", "main", ""); err == nil {
		t.Fatal("untitled conversation must not be picked without a pin")
	}
}
