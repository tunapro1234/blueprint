package harness

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// matrixPath is the generated machine-readable matrix. Regenerate it with
// BP_UPDATE_MATRIX=1 go test ./internal/harness -run TestMatrixIsGenerated.
var matrixPath = filepath.Join("..", "..", "docs", "harnesses.json")

func TestEveryDescriptorIsComplete(t *testing.T) {
	for _, d := range Catalog() {
		if d.Name == "" || d.Class == "" || d.Checked == "" {
			t.Errorf("%s: name/class/checked missing", d.Kind)
		}
		if len(d.Delivery) == 0 {
			t.Errorf("%s: no delivery path", d.Kind)
		}
		for _, b := range Behaviors {
			status, ok := d.Behaviors[b]
			if !ok || status.Support == "" {
				t.Errorf("%s: behavior %s has no status", d.Kind, b)
			}
		}
		for b := range d.Behaviors {
			known := false
			for _, want := range Behaviors {
				known = known || want == b
			}
			if !known {
				t.Errorf("%s: unknown behavior %s", d.Kind, b)
			}
		}
		if len(d.Sources) == 0 {
			t.Errorf("%s: no sources", d.Kind)
		}
	}
}

func TestForCommandSeparatesBinariesFromWrappers(t *testing.T) {
	if d, _ := ForCommand("claude"); d == nil || d.Kind != Claude {
		t.Fatalf("claude -> %v", d)
	}
	if d, _ := ForCommand("hermes"); d == nil || d.Kind != Hermes {
		t.Fatalf("hermes -> %v", d)
	}
	exact, nominated := ForCommand("node")
	if exact != nil {
		t.Fatalf("node must only nominate, got %s", exact.Kind)
	}
	kinds := map[Kind]bool{}
	for _, d := range nominated {
		kinds[d.Kind] = true
	}
	if !kinds[Codex] || !kinds[Claude] {
		t.Fatalf("node should nominate claude and codex, got %v", kinds)
	}
	if exact, nominated := ForCommand("zsh"); exact != nil || len(nominated) != 0 {
		t.Fatal("a shell is no harness")
	}
}

func TestMatrixIsGenerated(t *testing.T) {
	want, err := Matrix()
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("BP_UPDATE_MATRIX") == "1" {
		if err := os.WriteFile(matrixPath, want, 0644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(matrixPath)
	if err != nil {
		t.Fatalf("%v (regenerate with BP_UPDATE_MATRIX=1)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale; regenerate with BP_UPDATE_MATRIX=1 go test ./internal/harness -run TestMatrixIsGenerated", matrixPath)
	}
	var parsed struct {
		Harnesses []Descriptor `json:"harnesses"`
	}
	if err := json.Unmarshal(got, &parsed); err != nil || len(parsed.Harnesses) != len(Catalog()) {
		t.Fatalf("matrix does not round-trip: %v", err)
	}
}
