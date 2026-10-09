package bpskill

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// docs/agent-hint.md quotes the hint for reviewers; it must be the text bp
// actually installs, or a review approves words no agent ever reads.
func TestAgentHintDocMatchesShippedHint(t *testing.T) {
	doc, err := os.ReadFile("../../docs/agent-hint.md")
	if err != nil {
		t.Fatal(err)
	}
	_, body, ok := bytes.Cut(Hint, []byte(Marker+"\n"))
	if !ok {
		t.Fatal("HINT.md has no bp marker")
	}
	var quoted []string
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if line == "" {
			quoted = append(quoted, ">")
			continue
		}
		quoted = append(quoted, "> "+strings.Replace(line, "# bp — talk", "**bp — talk", 1))
	}
	quoted[0] += "**"
	if want := strings.Join(quoted, "\n") + "\n"; !strings.Contains(string(doc), want) {
		t.Fatalf("docs/agent-hint.md does not quote internal/bpskill/HINT.md; expected block:\n%s", want)
	}
}
