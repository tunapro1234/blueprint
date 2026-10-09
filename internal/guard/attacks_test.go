package guard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAttackCorpus(t *testing.T) {
	files, _ := filepath.Glob("testdata/attacks/*.txt")
	if len(files) == 0 {
		t.Fatal("no attack corpus")
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		head, body, _ := strings.Cut(string(raw), "\n")
		f, err := Frame(Source{Transport: "p2p", Peer: "attacker"}, body)
		if err != nil {
			t.Fatal(err)
		}
		got := kinds(f.Findings)
		for _, want := range strings.Split(strings.TrimPrefix(head, "# expect: "), ",") {
			if !got[Kind(strings.TrimSpace(want))] {
				t.Errorf("%s: %s not flagged", file, want)
			}
		}
		lines := strings.Split(f.Text, "\n")
		for _, l := range lines[1 : len(lines)-1] {
			if l != Notice && !strings.HasPrefix(l, "guard flags: ") && !strings.HasPrefix(l, BodyPrefix) {
				t.Errorf("%s: line escaped the frame: %q", file, l)
			}
		}
	}
}
