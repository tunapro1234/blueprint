package book

import (
	"blueprint/internal/cache"
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bptmux "blueprint/internal/tmux"
)

type runtimeRPCRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		ThreadID     string `json:"threadId"`
		IncludeTurns bool   `json:"includeTurns"`
	} `json:"params"`
}

func startFakeRuntimeAppServer(t *testing.T, socketPath, threadID, cwd, rolloutPath string, status any) (string, <-chan error) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(socketPath), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		request, err := http.ReadRequest(reader)
		if err != nil {
			done <- err
			return
		}
		key := request.Header.Get("Sec-WebSocket-Key")
		accept := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		if _, err := fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(accept[:])); err != nil {
			done <- err
			return
		}
		for {
			payload, opcode, err := readRuntimeClientFrame(reader)
			if err != nil {
				done <- err
				return
			}
			if opcode != 1 {
				done <- fmt.Errorf("unexpected client websocket opcode %d", opcode)
				return
			}
			var message runtimeRPCRequest
			if err := json.Unmarshal(payload, &message); err != nil {
				done <- err
				return
			}
			switch message.Method {
			case "initialize":
				err = writeRuntimeServerJSON(conn, map[string]any{"id": message.ID, "result": map[string]any{}})
			case "initialized":
				continue
			case "thread/read":
				if message.Params.ThreadID != threadID || message.Params.IncludeTurns {
					done <- fmt.Errorf("unexpected thread/read params: %+v", message.Params)
					return
				}
				result := map[string]any{"thread": map[string]any{
					"id": threadID, "cwd": cwd, "path": rolloutPath, "model": "gpt-6-luna", "status": status,
				}}
				if err := writeRuntimeServerJSON(conn, map[string]any{"id": message.ID, "result": result}); err != nil {
					done <- err
					return
				}
				done <- nil
				return
			default:
				done <- fmt.Errorf("unexpected app-server method %q", message.Method)
				return
			}
			if err != nil {
				done <- err
				return
			}
		}
	}()
	return "unix://" + socketPath, done
}

func readRuntimeClientFrame(reader *bufio.Reader) ([]byte, byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, 0, err
	}
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		var size [2]byte
		if _, err := io.ReadFull(reader, size[:]); err != nil {
			return nil, 0, err
		}
		length = uint64(binary.BigEndian.Uint16(size[:]))
	case 127:
		var size [8]byte
		if _, err := io.ReadFull(reader, size[:]); err != nil {
			return nil, 0, err
		}
		length = binary.BigEndian.Uint64(size[:])
	}
	if header[1]&0x80 == 0 || length > 1<<20 {
		return nil, 0, fmt.Errorf("invalid masked client frame length %d", length)
	}
	var mask [4]byte
	if _, err := io.ReadFull(reader, mask[:]); err != nil {
		return nil, 0, err
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, 0, err
	}
	for i := range payload {
		payload[i] ^= mask[i%len(mask)]
	}
	return payload, header[0] & 0x0f, nil
}

func writeRuntimeServerJSON(writer io.Writer, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	header := []byte{0x81}
	if len(payload) < 126 {
		header = append(header, byte(len(payload)))
	} else if len(payload) <= 65535 {
		header = append(header, 126, byte(len(payload)>>8), byte(len(payload)))
	} else {
		return fmt.Errorf("fake app-server response is too large")
	}
	if err := writeRuntimeAll(writer, header); err != nil {
		return err
	}
	return writeRuntimeAll(writer, payload)
}

func writeRuntimeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func TestReadCodexRuntimeAppServerSystemErrorDelivery(t *testing.T) {
	const threadID = "01a0711e-1b8b-76a2-954a-76f9e1a44ceb"
	const cwd = "/work"
	tests := []struct {
		name          string
		status        any
		remote        bool
		wantState     string
		wantReason    string
		wantLastError bool
		wantTurnBusy  *bool
	}{
		{name: "systemError is idle for delivery", status: map[string]any{"type": "systemError"}, remote: true, wantState: "idle", wantLastError: true, wantTurnBusy: boolPointer(false)},
		{name: "idle stays idle", status: map[string]any{"type": "idle"}, remote: true, wantState: "idle", wantTurnBusy: boolPointer(false)},
		{name: "active flags remain unknown", status: map[string]any{"type": "active", "activeFlags": []string{"waitingOnApproval"}}, remote: true, wantState: "unknown", wantReason: "app-server active flags: waitingOnApproval"},
		{name: "future state remains unknown", status: map[string]any{"type": "weirdState"}, remote: true, wantState: "unknown", wantReason: "app-server state: weirdState"},
		{name: "remote notLoaded remains unknown", status: map[string]any{"type": "notLoaded"}, remote: true, wantState: "unknown", wantReason: "app-server state: notLoaded"},
		{name: "non-remote notLoaded keeps early return", status: map[string]any{"type": "notLoaded"}, wantState: "working", wantTurnBusy: boolPointer(true)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home, err := os.MkdirTemp("", "bp-runtime-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(home) })
			rolloutPath := writeRuntimeCodex(t, home, threadID, cwd, "task_started", time.Now())
			remote := ""
			socketPath := filepath.Join(home, "app-server-control", "app-server-control.sock")
			if test.remote {
				socketPath = filepath.Join(home, "fake-app-server.sock")
			}
			remote, serverDone := startFakeRuntimeAppServer(t, socketPath, threadID, cwd, rolloutPath, test.status)
			if !test.remote {
				remote = ""
			}
			agent := Agent{Name: "agent", Folder: cwd, IdentityThreadID: threadID}
			if test.remote {
				agent.Launch = &bptmux.OpenOptions{Codex: true, Remote: remote}
			}
			activity := &cache.Activity{State: "unknown", LastTurnError: true}
			state := readCodexRuntime(context.Background(), bptmux.CodexProcess{Home: home, Remote: remote}, agent, activity)
			state.Activity = activity
			if activity.State != test.wantState || activity.Reason != test.wantReason || activity.LastTurnError != test.wantLastError {
				t.Fatalf("activity=%+v; want state=%q reason=%q lastTurnError=%v", activity, test.wantState, test.wantReason, test.wantLastError)
			}
			if test.wantTurnBusy == nil {
				if activity.TurnBusy != nil {
					t.Fatalf("TurnBusy=%v, want nil", *activity.TurnBusy)
				}
			} else if activity.TurnBusy == nil || *activity.TurnBusy != *test.wantTurnBusy {
				t.Fatalf("TurnBusy=%v, want %v", activity.TurnBusy, *test.wantTurnBusy)
			}
			if test.name == "systemError is idle for delivery" {
				fleet := Fleet{Agents: map[string]Agent{"agent": agent}}
				if reason := runtimeBlockReason("agent", fleet, state, false); reason != "" {
					t.Fatalf("systemError still blocks delivery: %s", reason)
				}
			}
			select {
			case err := <-serverDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("fake app-server did not finish thread/read")
			}
		})
	}
}

func boolPointer(value bool) *bool { return &value }

func TestCodexWriterObservationOnlyForEmbeddedPane(t *testing.T) {
	for _, tc := range []struct {
		name  string
		info  bptmux.CodexProcess
		agent Agent
		want  bool
	}{
		{"fresh", bptmux.CodexProcess{}, Agent{}, true},
		{"argv", bptmux.CodexProcess{ThreadID: "thread"}, Agent{}, true},
		{"identity pin", bptmux.CodexProcess{}, Agent{IdentityThreadID: "thread"}, true},
		{"resume pin", bptmux.CodexProcess{}, Agent{Launch: &bptmux.OpenOptions{Codex: true, ResumeID: "thread"}}, true},
		{"remote argv", bptmux.CodexProcess{Remote: "unix://"}, Agent{}, false},
		{"stale remote book with observed embedded CLI", bptmux.CodexProcess{Observed: true}, Agent{Launch: &bptmux.OpenOptions{Codex: true, Remote: "unix://"}}, true},
		{"remote book", bptmux.CodexProcess{}, Agent{Launch: &bptmux.OpenOptions{Codex: true, Remote: "unix://"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := needsCodexWriterBinding(tc.info, tc.agent); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
	// No process/lock is not evidence of an idle agent, even with a familiar cwd.
	a := &cache.Activity{State: "unknown", ObservedAt: time.Now().UTC()}
	codexRuntime(context.Background(), 0, Agent{Folder: "/srv/blueprint"}, a)
	if a.State != "unknown" || a.ThreadID != "" {
		t.Fatalf("invented binding: %+v", a)
	}
}

func TestClaudeRuntimeRejectsForeignPinAndBindsDuplicateTitlesByProcess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	dir := filepath.Join(home, "projects", "-work")
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	id := "69632f85-5244-4ec1-866c-31aa4adef0be"
	row := "{\"type\":\"custom-title\",\"customTitle\":\"agent\"}\n" + `{"type":"assistant","timestamp":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","message":{"role":"assistant","model":"claude-opus-5","stop_reason":"end_turn","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":100}}}` + "\n"
	if e := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(row), 0600); e != nil {
		t.Fatal(e)
	}
	agent := Agent{Name: "agent", Folder: "/work", Launch: &bptmux.OpenOptions{Codex: true, ResumeID: "01a07246-1e79-73b0-9b69-74e791e68208"}}
	observe := func(liveID string) *cache.Activity {
		a := &cache.Activity{State: "unknown", ObservedAt: time.Now()}
		claudeRuntimeBound(agent, a, liveID)
		return a
	}
	if a := observe(""); a.ThreadID != id {
		t.Fatalf("Codex pin filtered Claude: %+v", a)
	}
	other := "aaaaaaaa-1111-1111-1111-111111111111"
	if e := os.WriteFile(filepath.Join(dir, other+".jsonl"), []byte(row), 0600); e != nil {
		t.Fatal(e)
	}
	if a := observe(""); a.ThreadID != "" || !strings.Contains(a.Reason, "ambiguous") {
		t.Fatal(a)
	}
	if a := observe(id); a.ThreadID != id || a.Binding != "claude-process-session" {
		t.Fatal(a)
	}
	agent.Launch = &bptmux.OpenOptions{ResumeID: other}
	agent.Name = "stale-name-before-rename"
	if a := observe(id); a.ThreadID != id || a.State != "idle" {
		t.Fatal(a)
	}
	agent.Launch.ResumeID = "bbbbbbbb-1111-1111-1111-111111111111"
	if a := observe(""); a.ThreadID != "" || !strings.Contains(a.Reason, "pin") {
		t.Fatal(a)
	}
	if got := launchThread(agent, true); got != "" {
		t.Fatal("Claude pin used for Codex", got)
	}
}

func TestRemoteRetiredArgvNeedsExplicitPinAndLiveServer(t *testing.T) {
	const oldID = "01a0617e-29f9-79a3-be66-72ea1dec4718"
	const currentID = "01a0715e-a5c0-7171-adf3-c95343ce6d5b"
	for _, mode := range []string{"remote-pin", "embedded-pin", "remote-launch-only", "conflicting-pins"} {
		t.Run(mode, func(t *testing.T) {
			info := bptmux.CodexProcess{Home: t.TempDir(), ThreadID: oldID, Remote: "unix:///missing-bp-runtime-test.sock"}
			agent := Agent{Name: "server-main", Folder: "/srv", IdentityThreadID: currentID, Launch: &bptmux.OpenOptions{Codex: true, ResumeID: currentID}}
			switch mode {
			case "embedded-pin":
				info.Remote = ""
			case "remote-launch-only":
				agent.IdentityThreadID = ""
			case "conflicting-pins":
				agent.Launch.ResumeID = oldID
			}
			a := &cache.Activity{State: "unknown", DeliveryBlocked: true}
			s := readCodexRuntime(context.Background(), info, agent, a)
			want := "conflicting thread bindings"
			if mode == "remote-pin" {
				want = "app-server unavailable"
			}
			if a.State != "unknown" || a.Reason != want || s.Known {
				t.Fatalf("unverified argv replacement: %+v %+v", s, a)
			}
		})
	}
}

func TestUnboundCodexThreadIncludesRecoveryHintInDeliveryReason(t *testing.T) {
	agent := Agent{Name: "ghost", Folder: "/srv/work"}
	activity := &cache.Activity{State: "unknown"}
	state := readCodexRuntime(context.Background(), bptmux.CodexProcess{Home: t.TempDir()}, agent, activity)
	if activity.Reason != "no valid explicit thread binding" {
		t.Fatalf("machine-readable reason changed: %q", activity.Reason)
	}
	for _, part := range []string{
		"close it first with bp close 'ghost'",
		"reopen with bp open 'ghost' '/srv/work' --codex (omit --no-prompt)",
		"bind a known thread with bp open 'ghost' '/srv/work' --codex --thread THREAD_ID --rebind",
	} {
		if !strings.Contains(activity.RecoveryHint, part) {
			t.Fatalf("recovery hint %q missing %q", activity.RecoveryHint, part)
		}
	}
	fleet := Fleet{Sources: map[string][]string{"ghost": {"/srv/agentbook.json"}}}
	state.Activity = activity
	blocked := runtimeBlockReason("ghost", fleet, state, false)
	if !strings.Contains(blocked, activity.Reason) || !strings.Contains(blocked, activity.RecoveryHint) {
		t.Fatalf("delivery refusal lacks binding cause or recovery hint: %s", blocked)
	}
}

func TestRemoteTranscriptUsesVerifiedPathDespiteBirthCwd(t *testing.T) {
	home := t.TempDir()
	id := "01a0711e-1b8b-76a2-954a-76f9e1a44ceb"
	path := writeRuntimeCodex(t, home, id, "/srv", "task_complete", time.Now())
	if !remoteTranscript(home, id, path) {
		t.Fatal("server-selected rollout with an older birth cwd rejected")
	}
	if remoteTranscript(home, "01a0715e-a5c0-7171-adf3-c95343ce6d5b", path) || remoteTranscript(filepath.Join(home, "other-home"), id, path) {
		t.Fatal("wrong thread or out-of-home path accepted")
	}
}

func TestRuntimeReadsCodexInsteadOfStaleClaude(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	id := "01a070c4-3f56-7213-b8ad-19fa7d6f5e4d"
	day := filepath.Join(home, "sessions/2026/09/05")
	if err := os.MkdirAll(day, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(day, "rollout-"+id+".jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Format(time.RFC3339Nano)
	for _, row := range []any{
		map[string]any{"type": "session_meta", "payload": map[string]any{"id": id, "cwd": "/work", "source": "cli"}},
		map[string]any{"type": "turn_context", "payload": map[string]any{"model": "gpt-6-astra", "effort": "high"}},
		map[string]any{"type": "event_msg", "timestamp": now, "payload": map[string]any{"type": "token_count", "info": map[string]any{"last_token_usage": map[string]any{"total_tokens": 43210}}}},
		map[string]any{"type": "event_msg", "timestamp": now, "payload": map[string]any{"type": "task_started"}},
	} {
		if err := json.NewEncoder(f).Encode(row); err != nil {
			t.Fatal(err)
		}
	}
	_ = f.Close()
	fake := filepath.Join(home, "tmux")
	script := "#!/bin/sh\ncase \"$1\" in\nlist-panes) printf '1\\tnode\\t0\\n';;\ncapture-pane) printf '› Ask Codex to do anything\\n  gpt-6-astra high · /work\\n';;\n*) exit 1;;\nesac\n"
	if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	client := bptmux.New()
	client.Bin = fake
	agent := Agent{Name: "agent", Folder: "/work", Launch: &bptmux.OpenOptions{Codex: true, ResumeID: id}}
	state, codex := RuntimeState(context.Background(), client, agent)
	if !codex || !state.Known || !state.Busy || state.CtxTokens != 43210 || state.Model != "gpt-6-astra" || state.ThreadID != id {
		t.Fatalf("bad embedded runtime: %+v codex=%v", state, codex)
	}
	// A disconnected remote must retain its last measurement and block delivery.
	agent.Launch.Remote = "unix:///nonexistent/bp-test.sock"
	remote, codex := RuntimeState(context.Background(), client, agent)
	if !codex || !remote.Busy || remote.RuntimeError == "" || remote.CtxTokens != state.CtxTokens {
		t.Fatalf("lost remote fallback/protection: %+v", remote)
	}
}
