package fed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"blueprint/internal/msgq"
)

const (
	testToken      = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	otherTestToken = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
)

func testHub(t *testing.T, expose []string) (*Hub, *msgq.Queue) {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(filepath.Dir(PeersPath(stateDir)), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]Peer{
		"yigit": {Token: testToken, Expose: expose},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(PeersPath(stateDir), data, 0o600); err != nil {
		t.Fatal(err)
	}
	queue := msgq.New(filepath.Join(t.TempDir(), "msgq"))
	hub, err := NewHub(stateDir, "tuna", queue)
	if err != nil {
		t.Fatal(err)
	}
	return hub, queue
}

func hubRequest(t *testing.T, handler http.Handler, method, endpoint, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	request := httptest.NewRequest(method, endpoint, reader)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestHubRejectsUnknownToken(t *testing.T) {
	hub, _ := testHub(t, []string{"ada", "deniz", "oz"})
	response := hubRequest(t, hub.Handler(), http.MethodGet, "/v1/ping", otherTestToken, nil)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s, want 401", response.Code, response.Body.String())
	}
}

func TestHubEnforcesExpose(t *testing.T) {
	hub, queue := testHub(t, []string{"ada"})
	response := hubRequest(t, hub.Handler(), http.MethodPost, "/v1/send", testToken, map[string]string{
		"to": "deniz", "from": "oz", "msg": "hello",
	})
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s, want 403", response.Code, response.Body.String())
	}
	messages, err := queue.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("queue=%v, want empty", messages)
	}
}

func TestHubEnforcesMessageLimit(t *testing.T) {
	hub, _ := testHub(t, []string{"ada", "deniz", "oz"})
	response := hubRequest(t, hub.Handler(), http.MethodPost, "/v1/send", testToken, map[string]string{
		"to": "ada", "from": "oz", "msg": strings.Repeat("x", MaxMessageBytes+1),
	})
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s, want 413", response.Code, response.Body.String())
	}
}

func TestHubEnforcesPeerRateLimit(t *testing.T) {
	hub, _ := testHub(t, []string{"ada", "deniz", "oz"})
	hub.Rate = NewRateLimiter(1)
	body := map[string]string{"to": "ada", "from": "oz", "msg": "hello"}
	first := hubRequest(t, hub.Handler(), http.MethodPost, "/v1/send", testToken, body)
	if first.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	second := hubRequest(t, hub.Handler(), http.MethodPost, "/v1/send", testToken, body)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status=%d body=%s, want 429", second.Code, second.Body.String())
	}
}

func TestHubSendEnqueuesLocalMessage(t *testing.T) {
	hub, queue := testHub(t, []string{"ada", "deniz", "oz"})
	response := hubRequest(t, hub.Handler(), http.MethodPost, "/v1/send", testToken, map[string]string{
		"to": "ada", "from": "oz", "msg": "line\tone\r\nline two",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	messages, err := queue.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("queue length=%d, want 1", len(messages))
	}
	if messages[0].To != "ada" || messages[0].From != "oz@yigit" || messages[0].Msg != "[oz@yigit] lineone\nline two" {
		t.Fatalf("message=%+v", messages[0])
	}
}

func TestHubRejectsUnsafeToAndFromNames(t *testing.T) {
	hub, queue := testHub(t, []string{"ada"})
	for _, test := range []struct {
		to   string
		from string
	}{
		{to: "ada", from: "a@b"},
		{to: "ada", from: "x] [y"},
		{to: "ada", from: "a\tb"},
		{to: "bad target", from: "oz"},
	} {
		response := hubRequest(t, hub.Handler(), http.MethodPost, "/v1/send", testToken, map[string]string{
			"to": test.to, "from": test.from, "msg": "hello",
		})
		if response.Code != http.StatusBadRequest {
			t.Errorf("to=%q from=%q status=%d body=%s", test.to, test.from, response.Code, response.Body.String())
		}
	}
	messages, err := queue.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("unsafe names queued messages: %+v", messages)
	}
}

func TestHubPollEmptyAndDeletesReturnedMessages(t *testing.T) {
	hub, _ := testHub(t, []string{"ada", "deniz", "oz"})
	empty := hubRequest(t, hub.Handler(), http.MethodGet, "/v1/poll", testToken, nil)
	if empty.Code != http.StatusOK || strings.TrimSpace(empty.Body.String()) != `{"messages":[]}` {
		t.Fatalf("empty response=%d %s", empty.Code, empty.Body.String())
	}
	if _, err := hub.Outbox.Enqueue("yigit", "oz", "ada@tuna", "reply"); err != nil {
		t.Fatal(err)
	}
	full := hubRequest(t, hub.Handler(), http.MethodGet, "/v1/poll", testToken, nil)
	if full.Code != http.StatusOK || !strings.Contains(full.Body.String(), `"msg":"reply"`) {
		t.Fatalf("full response=%d %s", full.Code, full.Body.String())
	}
	depth, err := hub.Outbox.Depth("yigit")
	if err != nil {
		t.Fatal(err)
	}
	if depth != 0 {
		t.Fatalf("outbox depth=%d, want 0", depth)
	}
	again := hubRequest(t, hub.Handler(), http.MethodGet, "/v1/poll", testToken, nil)
	if strings.TrimSpace(again.Body.String()) != `{"messages":[]}` {
		t.Fatalf("second poll=%s", again.Body.String())
	}
}

func TestOutboxFIFOOrder(t *testing.T) {
	outbox := NewOutbox(t.TempDir())
	fixed := time.Date(2026, 7, 26, 12, 0, 0, 123, time.UTC)
	outbox.Now = func() time.Time { return fixed }
	if _, err := outbox.Enqueue("yigit", "oz", "ada@tuna", "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := outbox.Enqueue("yigit", "oz", "ada@tuna", "second"); err != nil {
		t.Fatal(err)
	}
	messages, err := outbox.Poll("yigit")
	if err != nil {
		t.Fatal(err)
	}
	got := []string{messages[0].Msg, messages[1].Msg}
	if want := []string{"first", "second"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order=%v, want %v", got, want)
	}
}

func TestParseAddress(t *testing.T) {
	agent, peer, remote, err := ParseAddress("ada@tuna")
	if err != nil || agent != "ada" || peer != "tuna" || !remote {
		t.Fatalf("remote parse=(%q,%q,%v,%v)", agent, peer, remote, err)
	}
	agent, peer, remote, err = ParseAddress("local-agent")
	if err != nil || agent != "local-agent" || peer != "" || remote {
		t.Fatalf("local parse=(%q,%q,%v,%v)", agent, peer, remote, err)
	}
	for _, value := range []string{"@tuna", "ada@", "a@b@c", "a/b@peer"} {
		if _, _, _, err := ParseAddress(value); err == nil {
			t.Errorf("ParseAddress(%q) succeeded, want error", value)
		}
	}
}

func TestClientPollEnqueuesAndRecordsLastPoll(t *testing.T) {
	hub, _ := testHub(t, []string{"ada", "deniz", "oz"})
	if _, err := hub.Outbox.Enqueue("yigit", "oz", "ada@tuna", "reply"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(hub.Handler())
	defer server.Close()
	client := NewClient(server.URL, testToken)
	queue := msgq.New(filepath.Join(t.TempDir(), "msgq"))
	stateDir := filepath.Join(t.TempDir(), "state")
	count, err := client.PollAndEnqueue(context.Background(), stateDir, queue)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count=%d, want 1", count)
	}
	messages, err := queue.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Msg != "[ada@tuna] reply" {
		t.Fatalf("messages=%+v", messages)
	}
	if _, err := os.Stat(LastPollPath(stateDir)); err != nil {
		t.Fatalf("last poll file: %v", err)
	}
	journal, err := ReadJournal(stateDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(journal) != 1 || journal[0].Dir != "in" || journal[0].Msg != "reply" || journal[0].From != "ada@tuna" {
		t.Fatalf("journal=%+v", journal)
	}
}

// A remote hub cannot be trusted to have sanitized: the polling side must
// strip control bytes itself, or a message could smuggle Ctrl-C, ESC
// sequences, or the bracketed-paste terminator into the pane as keystrokes.
func TestClientPollStripsControlBytes(t *testing.T) {
	hub, _ := testHub(t, []string{"ada", "deniz", "oz"})
	hostile := "hi\x03 there\x1b[201~rm -rf\rx"
	if _, err := hub.Outbox.Enqueue("yigit", "oz", "ada@tuna", hostile); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(hub.Handler())
	defer server.Close()
	client := NewClient(server.URL, testToken)
	queue := msgq.New(filepath.Join(t.TempDir(), "msgq"))
	stateDir := filepath.Join(t.TempDir(), "state")
	if _, err := client.PollAndEnqueue(context.Background(), stateDir, queue); err != nil {
		t.Fatal(err)
	}
	messages, err := queue.List()
	if err != nil {
		t.Fatal(err)
	}
	want := "[ada@tuna] hi there[201~rm -rfx"
	if len(messages) != 1 || messages[0].Msg != want {
		t.Fatalf("messages=%+v, want msg %q", messages, want)
	}
}

func TestJournalRecordsOutboundQueue(t *testing.T) {
	stateDir := t.TempDir()
	peers := map[string]Peer{"yigit": {Token: testToken}}
	if _, err := QueueOutbound(stateDir, peers, "yigit", "oz", "ada@tuna", "hello"); err != nil {
		t.Fatal(err)
	}
	journal, err := ReadJournal(stateDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(journal) != 1 || journal[0].Dir != "out" || journal[0].To != "oz@yigit" || journal[0].Msg != "hello" {
		t.Fatalf("journal=%+v", journal)
	}
}

func TestValidateNameStrictAlphabet(t *testing.T) {
	for _, value := range []string{"a@b", "x] [y", "has space", "..", "*", strings.Repeat("a", 65)} {
		if err := validateName(value); err == nil {
			t.Errorf("validateName(%q) succeeded, want error", value)
		}
	}
	for _, value := range []string{"kavram-gate", "a_b.c"} {
		if err := validateName(value); err != nil {
			t.Errorf("validateName(%q)=%v", value, err)
		}
	}
}

func TestUnauthenticatedAuditIsSummarizedOncePerMinute(t *testing.T) {
	hub, _ := testHub(t, []string{"ada"})
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	hub.Now = func() time.Time { return now }

	if got := hubRequest(t, hub.Handler(), http.MethodGet, "/v1/ping", "", nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", got)
	}
	if got := hubRequest(t, hub.Handler(), http.MethodGet, "/wp-login.php", "", nil).Code; got != http.StatusNotFound {
		t.Fatalf("unknown path status=%d", got)
	}
	logPath := filepath.Join(hub.StateDir, "fed", "log.jsonl")
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("unauthenticated requests wrote audit log: %v", err)
	}

	now = now.Add(time.Minute)
	hubRequest(t, hub.Handler(), http.MethodGet, "/v1/ping", "", nil)
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("audit lines=%d, want 1: %s", len(lines), data)
	}
	var entry auditEntry
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Endpoint != "(unauthenticated)" || entry.Count != 2 || entry.Result != http.StatusUnauthorized || entry.Peer != "" {
		t.Fatalf("summary=%+v", entry)
	}
	hubRequest(t, hub.Handler(), http.MethodGet, "/missing", "", nil)
	again, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, again) {
		t.Fatalf("second summary was written in same minute: before=%s after=%s", data, again)
	}
}

func TestUnauthenticatedGlobalRateLimit(t *testing.T) {
	hub, _ := testHub(t, []string{"ada"})
	for index := 0; index < unauthenticatedRate; index++ {
		if got := hubRequest(t, hub.Handler(), http.MethodGet, "/v1/ping", "", nil).Code; got != http.StatusUnauthorized {
			t.Fatalf("request %d status=%d", index, got)
		}
	}
	if got := hubRequest(t, hub.Handler(), http.MethodGet, "/v1/ping", "", nil).Code; got != http.StatusTooManyRequests {
		t.Fatalf("over-limit status=%d, want 429", got)
	}
}

func TestAuditRotatesAtEightMiB(t *testing.T) {
	hub, _ := testHub(t, []string{"ada"})
	dir := filepath.Join(hub.StateDir, "fed")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "log.jsonl")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), maxAuditBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := hubauditForTest(hub); err != nil {
		t.Fatal(err)
	}
	backup, err := os.Stat(path + ".1")
	if err != nil {
		t.Fatal(err)
	}
	if backup.Size() != maxAuditBytes {
		t.Fatalf("backup size=%d, want %d", backup.Size(), maxAuditBytes)
	}
	current, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if current.Size() <= 0 || current.Size() >= maxAuditBytes {
		t.Fatalf("current audit size=%d", current.Size())
	}
}

func hubauditForTest(hub *Hub) error {
	return hub.audit(auditEntry{
		TS:       time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		Peer:     "yigit",
		Endpoint: "/v1/ping",
		Result:   http.StatusOK,
	})
}

func TestPollAckAndInflightRedelivery(t *testing.T) {
	hub, _ := testHub(t, []string{"ada"})
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	hub.Outbox.Now = func() time.Time { return now }

	id, err := hub.Outbox.Enqueue("yigit", "oz", "ada@tuna", "acked")
	if err != nil {
		t.Fatal(err)
	}
	first := hubRequest(t, hub.Handler(), http.MethodGet, "/v1/poll", testToken, nil)
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), id) {
		t.Fatalf("poll=%d %s", first.Code, first.Body.String())
	}
	ack := hubRequest(t, hub.Handler(), http.MethodPost, "/v1/ack", testToken, map[string]any{"ids": []string{id}})
	if ack.Code != http.StatusOK || !strings.Contains(ack.Body.String(), `"acked":1`) {
		t.Fatalf("ack=%d %s", ack.Code, ack.Body.String())
	}
	inflight := filepath.Join(hub.StateDir, "fed", "inflight", "yigit", id+".json")
	if _, err := os.Stat(inflight); !os.IsNotExist(err) {
		t.Fatalf("acked file still exists: %v", err)
	}

	redeliverID, err := hub.Outbox.Enqueue("yigit", "oz", "ada@tuna", "redeliver")
	if err != nil {
		t.Fatal(err)
	}
	hubRequest(t, hub.Handler(), http.MethodGet, "/v1/poll", testToken, nil)
	now = now.Add(InflightTimeout - time.Second)
	early := hubRequest(t, hub.Handler(), http.MethodGet, "/v1/poll", testToken, nil)
	if strings.Contains(early.Body.String(), redeliverID) {
		t.Fatalf("message redelivered before timeout: %s", early.Body.String())
	}
	now = now.Add(2 * time.Second)
	late := hubRequest(t, hub.Handler(), http.MethodGet, "/v1/poll", testToken, nil)
	if !strings.Contains(late.Body.String(), redeliverID) {
		t.Fatalf("message was not redelivered: %s", late.Body.String())
	}
}

func TestClientPollIdempotency(t *testing.T) {
	message := Message{ID: "q0000000000000000001-000000", To: "ada", From: "oz@tuna", Msg: "once", TS: 1}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/poll":
			writeJSON(writer, http.StatusOK, map[string]any{"messages": []Message{message}})
		case "/v1/ack":
			writeJSON(writer, http.StatusOK, map[string]int{"acked": 1})
		default:
			writeError(writer, http.StatusNotFound, "not found")
		}
	}))
	defer server.Close()
	client := NewClient(server.URL, testToken)
	queue := msgq.New(filepath.Join(t.TempDir(), "msgq"))
	stateDir := filepath.Join(t.TempDir(), "state")
	for iteration := 0; iteration < 2; iteration++ {
		if _, err := client.PollAndEnqueue(context.Background(), stateDir, queue); err != nil {
			t.Fatal(err)
		}
	}
	messages, err := queue.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("queued messages=%d, want 1", len(messages))
	}
}

func TestOutboxQuotaDropsOldestWithWarning(t *testing.T) {
	var warning bytes.Buffer
	outbox := NewOutbox(t.TempDir())
	outbox.Log = &warning
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	outbox.Now = func() time.Time {
		now = now.Add(time.Nanosecond)
		return now
	}
	var firstID string
	for index := 0; index <= MaxPeerMessages; index++ {
		id, err := outbox.Enqueue("yigit", "ada", "oz@tuna", fmt.Sprintf("message-%d", index))
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			firstID = id
		}
	}
	depth, err := outbox.Depth("yigit")
	if err != nil {
		t.Fatal(err)
	}
	if depth != MaxPeerMessages {
		t.Fatalf("depth=%d, want %d", depth, MaxPeerMessages)
	}
	if !strings.Contains(warning.String(), "fed: dropped 1 oldest messages for peer yigit (quota)") {
		t.Fatalf("warning=%q", warning.String())
	}
	if _, err := os.Stat(filepath.Join(outbox.StateDir, "fed", "out", "yigit", firstID+".json")); !os.IsNotExist(err) {
		t.Fatalf("oldest message was not dropped: %v", err)
	}
}

func TestLoadPeersRejectsDuplicateToken(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(PeersPath(stateDir)), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]Peer{
		"first":  {Token: testToken, Expose: []string{"ada"}},
		"second": {Token: strings.ToUpper(testToken), Expose: []string{"ada"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(PeersPath(stateDir), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPeers(stateDir); err == nil || !strings.Contains(err.Error(), "duplicate token") {
		t.Fatalf("LoadPeers error=%v, want duplicate token", err)
	}
}

func TestLoadPeersSkipsInvalidNamesAndExposeEntries(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(PeersPath(stateDir)), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]Peer{
		"good":     {Token: testToken, Expose: []string{"ada"}},
		"../bad":   {Token: otherTestToken, Expose: []string{"ada"}},
		"bad-star": {Token: strings.Repeat("1", 64), Expose: []string{"*"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(PeersPath(stateDir), data, 0o600); err != nil {
		t.Fatal(err)
	}
	var warning bytes.Buffer
	peers, err := loadPeers(stateDir, &warning)
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || peers["good"].Token != testToken {
		t.Fatalf("loaded peers=%v", PeerNames(peers))
	}
	if !strings.Contains(warning.String(), `skipping peer "../bad"`) || !strings.Contains(warning.String(), `skipping peer "bad-star"`) {
		t.Fatalf("warnings=%q", warning.String())
	}
}

func TestClientRejectsPlaintextNonLoopbackHub(t *testing.T) {
	if err := CheckHubURL("http://example.com"); err == nil || !strings.Contains(err.Error(), "refusing to send bearer token over plaintext HTTP") {
		t.Fatalf("non-loopback error=%v", err)
	}
	if err := CheckHubURL("http://127.0.0.1:7877"); err != nil {
		t.Fatalf("loopback rejected: %v", err)
	}
}
