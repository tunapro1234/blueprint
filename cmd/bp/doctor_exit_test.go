package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blueprint/internal/book"
	"blueprint/internal/cache"
	bpconfig "blueprint/internal/config"
	bptmux "blueprint/internal/tmux"
)

func doctorTestTmux(t *testing.T, live bool) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tmux")
	liveValue := "0"
	if live {
		liveValue = "1"
	}
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"list-sessions) if [ \"$LIVE\" = 1 ]; then echo agent; fi; exit 0 ;;\n" +
		"has-session) if [ \"$LIVE\" = 1 ] && [ \"$3\" = =agent ]; then exit 0; fi; exit 1 ;;\n" +
		"list-panes) printf '1\\tclaude\\t42\\n' ;;\n" +
		"capture-pane) printf 'bypass permissions\\n' ;;\n" +
		"show-options) exit 1 ;;\n" +
		"*) exit 0 ;;\n" +
		"esac\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LIVE", liveValue)
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
}

func writeDoctorExitReport(t *testing.T, path, agent, status string, exitedAt *time.Time) []byte {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"agent": agent, "harness": "claude", "status": status}
	if exitedAt != nil {
		report["exitedAt"] = exitedAt.UTC().Format(time.RFC3339Nano)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return data
}

func exitCheck(t *testing.T, checks []doctorCheck, name string) doctorCheck {
	t.Helper()
	for _, check := range checks {
		if check.Name == "native_exit/"+name {
			return check
		}
	}
	t.Fatalf("doctor omitted native exit check for %s: %+v", name, checks)
	return doctorCheck{}
}

func TestDoctorExitEvidenceHistoryClassification(t *testing.T) {
	now := time.Now().UTC()
	for _, test := range []struct {
		name         string
		age          time.Duration
		timestamp    bool
		unregistered bool
		archived     bool
		later        string
		live         bool
		wantWarn     bool
	}{
		{name: "fresh active failure stays fail", age: time.Hour},
		{name: "unregistered failure is historical", age: time.Hour, unregistered: true, wantWarn: true},
		{name: "archived failure is historical", age: time.Hour, archived: true, wantWarn: true},
		{name: "old timestamp is historical", age: 25 * time.Hour, timestamp: true, wantWarn: true},
		{name: "old legacy mtime is historical", age: 25 * time.Hour, wantWarn: true},
		{name: "later clean launch is historical", age: time.Hour, timestamp: true, later: "success", wantWarn: true},
		{name: "later live launch is historical", age: time.Hour, timestamp: true, later: "running", live: true, wantWarn: true},
		{name: "later failed launch remains fail", age: time.Hour, timestamp: true, later: "failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			doctorTestTmux(t, test.live)
			root := t.TempDir()
			stateDir := filepath.Join(root, "state")
			failureAt := now.Add(-test.age)
			failurePath := filepath.Join(stateDir, "local", "run-failed", "exit.json")
			original := writeDoctorExitReport(t, failurePath, "agent", "1", nil)
			if test.timestamp {
				original = writeDoctorExitReport(t, failurePath, "agent", "1", &failureAt)
			} else if err := os.Chtimes(failurePath, failureAt, failureAt); err != nil {
				t.Fatal(err)
			}

			bookPath := filepath.Join(root, "agentbook.json")
			var fleet book.Fleet
			selected := "agent"
			if !test.unregistered && !test.archived {
				entry := book.Agent{Name: "agent", Status: "closed"}
				if test.later != "" {
					currentDir := filepath.Join(stateDir, "local", "run-current")
					entry.StatusAt = failureAt.Add(10 * time.Minute)
					entry.Local = &cache.LocalBinding{Path: filepath.Join(currentDir, "observation.json"), PID: 42, Harness: "claude"}
					laterAt := failureAtTime(failureAt, 20*time.Minute)
					switch test.later {
					case "success":
						writeDoctorExitReport(t, filepath.Join(currentDir, "exit.json"), "agent", "0", &laterAt)
					case "failure":
						writeDoctorExitReport(t, filepath.Join(currentDir, "exit.json"), "agent", "1", &laterAt)
					case "running":
						entry.Status = "open"
					}
				}
				fleet = book.Fleet{Agents: map[string]book.Agent{"agent": entry}}
				bookData, err := json.Marshal(book.File{Agents: []book.Agent{entry}})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(bookPath, append(bookData, '\n'), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if test.archived {
				bookData, err := json.Marshal(book.File{Agents: []book.Agent{{Name: "agent", ArchivedAt: now.Format(time.RFC3339)}}})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(bookPath, append(bookData, '\n'), 0o600); err != nil {
					t.Fatal(err)
				}
				fleet = book.Fleet{Agents: map[string]book.Agent{}}
			}
			if test.unregistered {
				fleet = book.Fleet{Agents: map[string]book.Agent{}}
			}
			checks := doctorRuntimeChecks(bpconfig.Config{StateDir: stateDir, Agentbooks: []string{bookPath}}, fleet, selected)
			got := exitCheck(t, checks, "agent")
			if got.Warning != test.wantWarn || got.OK != test.wantWarn {
				t.Fatalf("exit check=%+v, want historical warning=%t", got, test.wantWarn)
			}
			if test.wantWarn && !strings.Contains(got.Detail, "historical exit") {
				t.Fatalf("historical exit detail is unclear: %+v", got)
			}
			keptPhrase := "kept as evidence"
			if test.wantWarn {
				keptPhrase = "kept as historical evidence"
			}
			if !strings.Contains(got.Next, keptPhrase) || !strings.Contains(got.Next, "cat ") {
				t.Fatalf("exit hint does not preserve and explain how to read evidence: %+v", got)
			}
			after, err := os.ReadFile(failurePath)
			if err != nil || string(after) != string(original) {
				t.Fatalf("doctor changed retained exit evidence: %v", err)
			}
		})
	}
}

func failureAtTime(start time.Time, after time.Duration) time.Time { return start.Add(after) }

func TestRetainSessionLaunchFailureRecordsExitTimestamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exit.json")
	t.Setenv("BP_EXIT_REPORT", path)
	before := time.Now().UTC().Add(-time.Second)
	retainSessionLaunchFailure([]string{"_session", "failed-agent", "claude"}, os.ErrInvalid)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	var stamp time.Time
	if err := json.Unmarshal(fields["exitedAt"], &stamp); err != nil || stamp.Before(before) || stamp.After(time.Now().UTC().Add(time.Second)) {
		t.Fatalf("exit timestamp=%s err=%v report=%s", stamp, err, data)
	}
}

func TestFinishLocalExitRecordsTimestamp(t *testing.T) {
	root := t.TempDir()
	tmux := filepath.Join(root, "tmux")
	if err := os.WriteFile(tmux, []byte("#!/bin/sh\ncase \"$1\" in\ndisplay-message) printf '%%1\\t42\\t1\\t1\\t\\n' ;;\ncapture-pane) printf 'native cli failed\\n' ;;\n*) : ;;\nesac\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "run", "exit.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC().Add(-time.Second)
	a := &app{tmux: &bptmux.Client{Bin: tmux}, err: testOutput(t)}
	a.finishLocalExit("agent", 42, "claude", path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	var stamp time.Time
	if err := json.Unmarshal(fields["exitedAt"], &stamp); err != nil || stamp.Before(before) {
		t.Fatalf("finishLocalExit timestamp=%s err=%v report=%s", stamp, err, data)
	}
}
