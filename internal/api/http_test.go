package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testServer(t *testing.T, terminals ...string) (*Core, *httptest.Server, string) {
	t.Helper()
	core := testCore(t, terminals...)
	token, err := LoadOrCreateToken(core.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Core: core, Token: token}
	ts := httptest.NewUnstartedServer(nil)
	ts.Config = server.HTTPServer()
	ts.Start()
	t.Cleanup(ts.Close)
	return core, ts, token
}

func do(t *testing.T, method, url, token string, headers map[string]string, body any) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	req, _ := http.NewRequest(method, url, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	json.Unmarshal(data, &out)
	if len(data) > 0 && len(out) == 0 {
		out["raw"] = string(data)
	}
	return resp.StatusCode, out
}

func TestHTTPAuthHostAndOrigin(t *testing.T) {
	core, ts, token := testServer(t, "worker")
	info, _ := os.Stat(TokenPath(core.StateDir))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token mode %v", info.Mode())
	}
	if again, _ := LoadOrCreateToken(core.StateDir); again != token {
		t.Fatal("token changed between loads")
	}
	if code, card := do(t, "GET", ts.URL+"/.well-known/agent-card.json", "", nil, nil); code != 200 || card["name"] != "bp" {
		t.Fatalf("hub card %d %v", code, card)
	}
	if code, _ := do(t, "GET", ts.URL+"/v1/agents", "", nil, nil); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := do(t, "GET", ts.URL+"/v1/agents", "bpt_wrong", nil, nil); code != 401 {
		t.Fatalf("wrong token: %d", code)
	}
	if code, _ := do(t, "GET", ts.URL+"/v1/agents", token, map[string]string{"Host": "evil.example"}, nil); code != 403 {
		t.Fatalf("rebinding host: %d", code)
	}
	if code, _ := do(t, "GET", ts.URL+"/v1/agents", token, map[string]string{"Origin": "https://evil.example"}, nil); code != 403 {
		t.Fatalf("foreign origin: %d", code)
	}
	if code, body := do(t, "GET", ts.URL+"/v1/agents", token, map[string]string{"Origin": "http://localhost:3000"}, nil); code != 200 || len(body["agents"].([]any)) != 1 {
		t.Fatalf("agents %d %v", code, body)
	}
	os.Chmod(TokenPath(core.StateDir), 0o644)
	if _, err := LoadOrCreateToken(core.StateDir); err == nil {
		t.Fatal("a world-readable token was accepted")
	}
}

func TestHTTPNativeRESTFlow(t *testing.T) {
	core, ts, token := testServer(t, "worker")
	alice := map[string]string{"X-BP-Agent": "alice"}
	bot := map[string]string{"X-BP-Agent": "bot"}
	if code, body := do(t, "POST", ts.URL+"/v1/agents", token, nil, map[string]any{"name": "bot", "description": "a script"}); code != 200 || body["name"] != "bot" {
		t.Fatalf("register %d %v", code, body)
	}
	code, sent := do(t, "POST", ts.URL+"/v1/messages", token, alice, map[string]any{"to": "worker", "text": "hi", "messageId": "x1"})
	if code != 200 || sent["route"] != "queue" || sent["state"] != StateAccepted {
		t.Fatalf("send %d %v", code, sent)
	}
	if code, body := do(t, "POST", ts.URL+"/v1/messages", token, nil, map[string]any{"to": "worker", "text": "hi"}); code != 400 {
		t.Fatalf("anonymous send %d %v", code, body)
	}
	if code, body := do(t, "GET", ts.URL+"/v1/messages/"+sent["id"].(string), token, nil, nil); code != 200 || body["state"] != StateAccepted {
		t.Fatalf("status %d %v", code, body)
	}
	if code, body := do(t, "GET", ts.URL+"/v1/messages/qpnope", token, nil, nil); code != 404 {
		t.Fatalf("missing status %d %v", code, body)
	}
	do(t, "POST", ts.URL+"/v1/messages", token, alice, map[string]any{"to": "bot", "text": "for the bot"})
	if code, body := do(t, "GET", ts.URL+"/v1/inbox?agent=bot", token, alice, nil); code != 403 {
		t.Fatalf("foreign inbox %d %v", code, body)
	}
	code, inbox := do(t, "GET", ts.URL+"/v1/inbox", token, bot, nil)
	if msgs := inbox["messages"].([]any); code != 200 || len(msgs) != 1 || msgs[0].(map[string]any)["text"] != "for the bot" {
		t.Fatalf("inbox %d %v", code, inbox)
	}
	if code, body := do(t, "POST", ts.URL+"/v1/rooms/team/join", token, alice, map[string]any{"agents": []string{"alice", "bot"}}); code != 404 {
		t.Fatalf("unknown member alice joined: %d %v", code, body)
	}
	do(t, "POST", ts.URL+"/v1/agents", token, nil, map[string]any{"name": "alice"})
	if code, body := do(t, "POST", ts.URL+"/v1/rooms/team/join", token, alice, map[string]any{"agents": []string{"alice", "bot", "worker"}}); code != 200 {
		t.Fatalf("join %d %v", code, body)
	}
	if code, body := do(t, "POST", ts.URL+"/v1/rooms/team/posts", token, alice, map[string]any{"text": "kickoff"}); code != 200 || len(body["deliveries"].([]any)) != 2 {
		t.Fatalf("post %d %v", code, body)
	}
	if code, body := do(t, "GET", ts.URL+"/v1/rooms/team/posts", token, bot, nil); code != 200 || len(body["posts"].([]any)) != 1 {
		t.Fatalf("room read %d %v", code, body)
	}
	if code, body := do(t, "PUT", ts.URL+"/v1/boards/main/keys/plan/step-1", token, alice, map[string]any{"value": "draft", "expectedVersion": 0}); code != 200 || body["version"].(float64) != 1 {
		t.Fatalf("board put %d %v", code, body)
	}
	if code, body := do(t, "PUT", ts.URL+"/v1/boards/main/keys/plan/step-1", token, bot, map[string]any{"value": "x", "expectedVersion": 0}); code != 409 {
		t.Fatalf("stale put %d %v", code, body)
	}
	if code, body := do(t, "GET", ts.URL+"/v1/boards/main?prefix=plan/", token, bot, nil); code != 200 || len(body["entries"].([]any)) != 1 {
		t.Fatalf("board get %d %v", code, body)
	}
	if code, body := do(t, "DELETE", ts.URL+"/v1/messages/"+sent["id"].(string), token, alice, nil); code != 200 || body["state"] != StateCanceled {
		t.Fatalf("cancel %d %v", code, body)
	}
	pending, _ := core.Queue.List()
	if len(pending) != 1 || pending[0].Msg != "[room team] [http:alice] kickoff" {
		t.Fatalf("queue %+v", pending)
	}
}

func TestHTTPA2ABindings(t *testing.T) {
	_, ts, token := testServer(t, "worker")
	v1 := map[string]string{"A2A-Version": "1.0", "X-BP-Agent": "alice"}
	code, card := do(t, "GET", ts.URL+"/a2a/agents/worker/.well-known/agent-card.json", token, nil, nil)
	if code != 200 || card["name"] != "worker" {
		t.Fatalf("agent card %d %v", code, card)
	}
	iface := card["supportedInterfaces"].([]any)[0].(map[string]any)
	if iface["url"] != ts.URL+"/a2a/agents/worker" || iface["protocolBinding"] != "HTTP+JSON" {
		t.Fatalf("interface %v", iface)
	}
	if code, _ := do(t, "GET", ts.URL+"/a2a/agents/ghost/.well-known/agent-card.json", token, nil, nil); code != 404 {
		t.Fatalf("ghost card %d", code)
	}
	message := map[string]any{"message": map[string]any{"messageId": "m-1", "role": "ROLE_USER", "parts": []any{map[string]any{"text": "build it"}}}}
	code, sent := do(t, "POST", ts.URL+"/a2a/agents/worker/message:send", token, v1, message)
	task, _ := sent["task"].(map[string]any)
	if code != 200 || task == nil || task["status"].(map[string]any)["state"] != "TASK_STATE_SUBMITTED" {
		t.Fatalf("v1 send %d %v", code, sent)
	}
	id := task["id"].(string)
	if code, got := do(t, "GET", ts.URL+"/a2a/agents/worker/tasks/"+id, token, v1, nil); code != 200 || got["id"] != id {
		t.Fatalf("v1 get %d %v", code, got)
	}
	if code, got := do(t, "GET", ts.URL+"/a2a/tasks/nope", token, v1, nil); code != 404 {
		t.Fatalf("missing task %d %v", code, got)
	}
	file := map[string]any{"message": map[string]any{"messageId": "m-2", "role": "ROLE_USER", "parts": []any{map[string]any{"url": "https://x/y.png"}}}}
	if code, got := do(t, "POST", ts.URL+"/a2a/agents/worker/message:send", token, v1, file); code != 400 {
		t.Fatalf("file part %d %v", code, got)
	}
	if code, got := do(t, "POST", ts.URL+"/a2a/message:send", token, map[string]string{"A2A-Version": "9.9"}, message); code != 400 {
		t.Fatalf("bad version %d %v", code, got)
	}
	// v0.3 JSON-RPC on the hub endpoint, target and sender in metadata.
	rpc := map[string]any{"jsonrpc": "2.0", "id": 7, "method": "message/send", "params": map[string]any{"message": map[string]any{
		"kind": "message", "messageId": "m-3", "role": "user", "parts": []any{map[string]any{"kind": "text", "text": "hello"}},
		"metadata": map[string]any{"bp/to": "worker", "bp/from": "carol"}}}}
	code, reply := do(t, "POST", ts.URL+"/a2a/rpc", token, nil, rpc)
	result, _ := reply["result"].(map[string]any)
	if code != 200 || result == nil || result["kind"] != "task" || result["status"].(map[string]any)["state"] != "submitted" {
		t.Fatalf("v0.3 rpc %d %v", code, reply)
	}
	cancel := map[string]any{"jsonrpc": "2.0", "id": 8, "method": "tasks/cancel", "params": map[string]any{"id": result["id"]}}
	code, reply = do(t, "POST", ts.URL+"/a2a/rpc", token, map[string]string{"X-BP-Agent": "mallory"}, cancel)
	if errObj, _ := reply["error"].(map[string]any); errObj == nil {
		t.Fatalf("another caller canceled the task: %v", reply)
	}
	code, reply = do(t, "POST", ts.URL+"/a2a/rpc", token, map[string]string{"X-BP-Agent": "carol"}, cancel)
	if result, _ := reply["result"].(map[string]any); result == nil || result["status"].(map[string]any)["state"] != "canceled" {
		t.Fatalf("cancel %d %v", code, reply)
	}
	code, reply = do(t, "POST", ts.URL+"/a2a/rpc", token, v1, map[string]any{"jsonrpc": "2.0", "id": 9, "method": "GetTask", "params": map[string]any{"id": "qpmissing"}})
	if errObj, _ := reply["error"].(map[string]any); errObj == nil || errObj["code"].(float64) != -32001 {
		t.Fatalf("GetTask missing %d %v", code, reply)
	}
}

func TestHTTPMCPEndpoint(t *testing.T) {
	_, ts, token := testServer(t, "worker")
	code, init := do(t, "POST", ts.URL+"/mcp", token, nil, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-11-25"}})
	if code != 200 || init["result"].(map[string]any)["protocolVersion"] != "2025-11-25" {
		t.Fatalf("initialize %d %v", code, init)
	}
	if code, _ := do(t, "POST", ts.URL+"/mcp", token, nil, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); code != 202 {
		t.Fatalf("notification %d", code)
	}
	if code, _ := do(t, "GET", ts.URL+"/mcp", token, nil, nil); code != 405 {
		t.Fatalf("GET %d", code)
	}
	if code, _ := do(t, "POST", ts.URL+"/mcp", token, map[string]string{"MCP-Protocol-Version": "1999-01-01"}, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "ping"}); code != 400 {
		t.Fatalf("bad version %d", code)
	}
	code, sent := do(t, "POST", ts.URL+"/mcp", token, map[string]string{"X-BP-Agent": "alice"}, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{"name": "bp_send", "arguments": map[string]any{"to": "worker", "text": "via mcp http"}}})
	if result := sent["result"].(map[string]any); code != 200 || result["isError"] == true {
		t.Fatalf("tool call %d %v", code, sent)
	}
}

func TestUnixSocketNeedsNoTokenAndChecksUID(t *testing.T) {
	core := testCore(t, "worker")
	dir, err := os.MkdirTemp("", "bpapi")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "api", "api.sock")
	listener, err := ListenUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	server := (&Server{Core: core, Token: "bpt_" + strings.Repeat("x", 40)}).HTTPServer()
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v", info.Mode())
	}
	if _, err := ListenUnix(path); err == nil {
		t.Fatal("a second server took over a live socket")
	}
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}}
	resp, err := client.Get("http://bp/v1/agents")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("socket request %d", resp.StatusCode)
	}
}
