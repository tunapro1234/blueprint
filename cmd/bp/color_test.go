package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bpconfig "blueprint/internal/config"
	bptmux "blueprint/internal/tmux"
)

func TestColorExportClosedAgentAndUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentbook.json")
	if err := os.WriteFile(path, []byte(`{"agents":[{"name":"test","color":"178"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	output := func() string {
		b, err := os.ReadFile(out.Name())
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	tm := bptmux.New()
	tm.Bin = "/nonexistent-bp-test-tmux"
	a := &app{ctx: context.Background(), config: bpconfig.Config{Agentbooks: []string{path}}, tmux: tm, out: out}
	if err := a.color([]string{"test"}); err != nil || output() != "#d7af00\n" {
		t.Fatalf("%q %v", output(), err)
	}
	out.Truncate(0)
	out.Seek(0, 0)
	if err := a.color([]string{"test", "--json"}); err != nil || !strings.Contains(output(), `"index":178`) {
		t.Fatalf("%q %v", output(), err)
	}
	if err := a.color([]string{"unknown"}); err == nil {
		t.Fatal("invented color for unknown agent")
	}
	for _, tc := range []struct{ value, want string }{{"blue", "33"}, {"205", "205"}, {"auto", "178"}} {
		if err := a.color([]string{"test", tc.value}); err != nil {
			t.Fatal(err)
		}
		if got := a.barAccent("test"); got != tc.want {
			t.Fatalf("%s: accent %s != %s", tc.value, got, tc.want)
		}
	}
	for _, bad := range []string{"256", "-1", "blurple", "#[bg=red]"} {
		if err := a.color([]string{"test", bad}); err == nil {
			t.Fatalf("accepted %q", bad)
		}
		if a.barAccent("test") != "178" {
			t.Fatal("invalid input changed color")
		}
	}
	a.config.Bar.DefaultColor = "purple"
	if got := a.barAccent("test"); got != "135" {
		t.Fatalf("machine default: %s", got)
	}
	if err := a.color([]string{"test", "blue"}); err != nil {
		t.Fatal(err)
	}
	if got := a.barAccent("test"); got != "33" {
		t.Fatalf("explicit override: %s", got)
	}
	if err := a.color([]string{"test", "auto"}); err != nil {
		t.Fatal(err)
	}
	if got := a.barAccent("test"); got != "135" {
		t.Fatalf("auto should return to machine default: %s", got)
	}
}

func TestXtermPaletteBoundaries(t *testing.T) {
	for index, want := range map[int]string{0: "#000000", 15: "#ffffff", 16: "#000000", 21: "#0000ff", 231: "#ffffff", 232: "#080808", 255: "#eeeeee"} {
		if got := xtermColor(index); got != want {
			t.Fatalf("index %d: %s != %s", index, got, want)
		}
	}
}
