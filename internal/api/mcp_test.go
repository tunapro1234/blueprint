package api

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type rpcReply struct {
	ID     json.RawMessage `json:"id"`
	Result struct {
		ProtocolVersion string `json:"protocolVersion"`
		Tools           []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError           bool           `json:"isError"`
		StructuredContent map[string]any `json:"structuredContent"`
	} `json:"result"`
	Error *rpcError `json:"error"`
}

func call(t *testing.T, s *MCPSession, id int, tool string, args any) rpcReply {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args}})
	var reply rpcReply
	if err := json.Unmarshal(s.Handle(context.Background(), raw), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Error != nil {
		t.Fatalf("%s: protocol error %+v", tool, reply.Error)
	}
	return reply
}

func TestMCPStdioHandshakeAndToolList(t *testing.T) {
	core := testCore(t, "worker")
	session := NewMCPSession(core, Caller{Name: "lead", Verified: true, Transport: "mcp"}, nil)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"nope"}`,
		`not json`,
		``,
	}, "\n")
	var out bytes.Buffer
	if err := ServeStdio(context.Background(), session, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("want 4 replies (the notification gets none), got %d:\n%s", len(lines), out.String())
	}
	var init rpcReply
	json.Unmarshal([]byte(lines[0]), &init)
	if init.Result.ProtocolVersion != "2025-06-18" {
		t.Fatalf("version not negotiated: %s", lines[0])
	}
	var list rpcReply
	json.Unmarshal([]byte(lines[1]), &list)
	names := []string{}
	for _, tool := range list.Result.Tools {
		names = append(names, tool.Name)
		if tool.InputSchema["type"] != "object" {
			t.Errorf("%s: input schema must be an object", tool.Name)
		}
	}
	for _, want := range []string{"bp_agents", "bp_send", "bp_inbox", "bp_status", "bp_room_post", "bp_room_read", "bp_board_get", "bp_board_put"} {
		if !contains(names, want) {
			t.Errorf("tool %s missing from %v", want, names)
		}
	}
	var missing, bad rpcReply
	json.Unmarshal([]byte(lines[2]), &missing)
	json.Unmarshal([]byte(lines[3]), &bad)
	if missing.Error == nil || missing.Error.Code != rpcMethodNotFound || bad.Error == nil || bad.Error.Code != rpcParseError {
		t.Fatalf("errors: %s / %s", lines[2], lines[3])
	}
}

func TestMCPUnnamedSessionRegistersAndReadsInbox(t *testing.T) {
	core := testCore(t, "worker")
	session := NewMCPSession(core, Caller{Transport: "mcp"}, nil)
	if reply := call(t, session, 1, "bp_send", map[string]any{"to": "worker", "text": "x"}); !reply.Result.IsError {
		t.Fatal("an unnamed session sent a message")
	}
	if reply := call(t, session, 2, "bp_register", map[string]any{"name": "cursor-1", "description": "IDE agent"}); reply.Result.IsError {
		t.Fatalf("register: %+v", reply.Result.Content)
	}
	if reply := call(t, session, 3, "bp_register", map[string]any{"name": "someone-else"}); !reply.Result.IsError {
		t.Fatal("a session renamed itself")
	}
	lead := NewMCPSession(core, Caller{Name: "worker", Verified: true, Transport: "mcp"}, nil)
	sent := call(t, lead, 4, "bp_send", map[string]any{"to": "cursor-1", "text": "please review"})
	id, _ := sent.Result.StructuredContent["id"].(string)
	if sent.Result.IsError || id == "" {
		t.Fatalf("send: %+v", sent.Result)
	}
	inbox := call(t, session, 5, "bp_inbox", map[string]any{})
	if !strings.Contains(inbox.Result.Content[0].Text, "please review") || !strings.Contains(inbox.Result.Content[0].Text, `"from": "worker"`) {
		t.Fatalf("inbox: %s", inbox.Result.Content[0].Text)
	}
	status := call(t, lead, 6, "bp_status", map[string]any{"id": id})
	if status.Result.StructuredContent["state"] != StateDelivered {
		t.Fatalf("status: %+v", status.Result.StructuredContent)
	}
}

func TestMCPPolicyLimitsWhatASessionSees(t *testing.T) {
	core := testCore(t, "public", "private")
	core.Register(context.Background(), alice, "chatgpt", "")
	session := NewMCPSession(core, Caller{Name: "chatgpt", Transport: "gateway", Remote: true},
		&Policy{Agents: []string{"public"}, Rooms: []string{"open"}, ReadOnlyBoards: []string{"main"}})
	agents := call(t, session, 1, "bp_agents", nil)
	if text := agents.Result.Content[0].Text; strings.Contains(text, "private") || !strings.Contains(text, "public") {
		t.Fatalf("agents leaked: %s", text)
	}
	if reply := call(t, session, 2, "bp_send", map[string]any{"to": "private", "text": "x"}); !reply.Result.IsError {
		t.Fatal("sent to an unexposed agent")
	}
	if reply := call(t, session, 3, "bp_send", map[string]any{"to": "public", "text": "hello"}); reply.Result.IsError {
		t.Fatalf("send to exposed agent: %+v", reply.Result.Content)
	}
	if reply := call(t, session, 4, "bp_room_join", map[string]any{"room": "secret"}); !reply.Result.IsError {
		t.Fatal("joined an unexposed room")
	}
	if reply := call(t, session, 5, "bp_room_join", map[string]any{"room": "open", "agents": []string{"chatgpt", "private"}}); !reply.Result.IsError {
		t.Fatal("added an unexposed agent to a room")
	}
	if reply := call(t, session, 6, "bp_board_put", map[string]any{"key": "k", "value": "v"}); !reply.Result.IsError {
		t.Fatal("wrote a read-only board")
	}
	if reply := call(t, session, 7, "bp_board_get", map[string]any{}); reply.Result.IsError {
		t.Fatal("could not read a read-only board")
	}
	pending, _ := core.Queue.List()
	if len(pending) != 1 || strings.Contains(pending[0].Msg, "[untrusted ") || pending[0].Origin == nil || pending[0].Origin.PeerAuthenticated {
		t.Fatalf("gateway text must be stored raw with an untrusted origin: %+v", pending)
	}
}

func TestMCPStatelessRevision(t *testing.T) {
	core := testCore(t, "worker")
	session := NewMCPSession(core, Caller{Name: "lead", Verified: true, Transport: "mcp"}, nil)
	meta := `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}`
	var discover struct {
		Result map[string]any `json:"result"`
	}
	json.Unmarshal(session.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{`+meta+`}}`)), &discover)
	if discover.Result["resultType"] != "complete" || discover.Result["supportedVersions"].([]any)[0] != "2026-07-28" {
		t.Fatalf("discover: %+v", discover.Result)
	}
	var list struct {
		Result map[string]any `json:"result"`
	}
	json.Unmarshal(session.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{`+meta+`}}`)), &list)
	if list.Result["resultType"] != "complete" || list.Result["ttlMs"] == nil {
		t.Fatalf("tools/list without a handshake: %+v", list.Result)
	}
	var bad rpcReply
	json.Unmarshal(session.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2099-01-01"}}}`)), &bad)
	if bad.Error == nil || bad.Error.Code != rpcUnsupportedVersion {
		t.Fatalf("unsupported version accepted: %+v", bad)
	}
}
