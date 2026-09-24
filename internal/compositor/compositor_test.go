package compositor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectEnvironmentRequiresSupportedCompositor(t *testing.T) {
	values := map[string]string{}
	getenv := func(key string) string { return values[key] }
	if _, err := DetectEnvironment(getenv); err == nil || !strings.Contains(err.Error(), "no supported compositor detected") {
		t.Fatalf("unsupported environment error=%v", err)
	}
	values["SWAYSOCK"] = "/tmp/sway-ipc.sock"
	if _, ok := mustDetect(t, getenv).(*Sway); !ok {
		t.Fatal("SWAYSOCK did not select Sway")
	}
	values["HYPRLAND_INSTANCE_SIGNATURE"] = "instance"
	if _, ok := mustDetect(t, getenv).(*Hyprland); !ok {
		t.Fatal("Hyprland was not preferred when both compositor markers were set")
	}
}

func mustDetect(t *testing.T, getenv func(string) string) Adapter {
	t.Helper()
	adapter, err := DetectEnvironment(getenv)
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func TestHyprlandAdapterUsesHyprctlFromPATH(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "commands.log")
	writeFakeCommand(t, dir, "hyprctl", `#!/bin/sh
printf '%s\n' "$*" >> "$COMMAND_LOG"
if [ "$1" = "-j" ]; then
  cat <<'JSON'
[{"address":"0x1234","pid":42,"class":"kitty","focused":false,"focusHistoryID":0,"workspace":{"id":7,"name":"dev"}}]
JSON
fi
`)
	t.Setenv("COMMAND_LOG", logPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	adapter := &Hyprland{}
	windows, err := adapter.ListWindows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Window{{ID: "0x1234", PID: 42, Class: "kitty", Workspace: "dev", Focused: true}}
	if len(windows) != len(want) || windows[0] != want[0] {
		t.Fatalf("windows=%+v, want %+v", windows, want)
	}
	if err := adapter.FocusWindow(context.Background(), "0x1234"); err != nil {
		t.Fatal(err)
	}
	if err := adapter.SetBorderColors(context.Background(), "0x1234", "#123456", "#010203"); err != nil {
		t.Fatal(err)
	}
	if err := adapter.FocusWindow(context.Background(), "not-an-address"); err == nil {
		t.Fatal("invalid Hyprland address was accepted")
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"-j clients",
		"dispatch focuswindow address:0x1234",
		"setprop address:0x1234 activebordercolor rgb(123456)",
		"setprop address:0x1234 inactivebordercolor rgb(010203)",
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("hyprctl did not receive %q; log=%s", want, data)
		}
	}
}

func TestSwayAdapterListsAndFocusesWindowsFromPATH(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "commands.log")
	writeFakeCommand(t, dir, "swaymsg", `#!/bin/sh
printf '%s\n' "$*" >> "$COMMAND_LOG"
if [ "$2" = "get_tree" ]; then
  cat <<'JSON'
{"id":1,"type":"root","nodes":[{"id":2,"type":"workspace","name":"2:web","nodes":[{"id":9,"type":"con","pid":55,"app_id":"foot","focused":true}]}]}
JSON
fi
`)
	t.Setenv("COMMAND_LOG", logPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	adapter := &Sway{}
	windows, err := adapter.ListWindows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Window{{ID: "9", PID: 55, Class: "foot", Workspace: "2:web", Focused: true}}
	if len(windows) != len(want) || windows[0] != want[0] {
		t.Fatalf("windows=%+v, want %+v", windows, want)
	}
	if err := adapter.FocusWindow(context.Background(), "9"); err != nil {
		t.Fatal(err)
	}
	if err := adapter.FocusWindow(context.Background(), "-1"); err == nil {
		t.Fatal("negative sway window id was accepted")
	}
	if err := adapter.SetBorderColors(context.Background(), "9", "#ffffff", "#777777"); !errors.Is(err, ErrBorderColorsUnsupported) {
		t.Fatalf("Sway border error=%v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-t get_tree -r", "[con_id=9] focus"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("swaymsg did not receive %q; log=%s", want, data)
		}
	}
}

func writeFakeCommand(t *testing.T, dir, name, script string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}
