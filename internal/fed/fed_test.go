package fed

import (
	"bytes"
	"context"
	"encoding/json"
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
	hub, _ := testHub(t, []string{"*"})
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
	hub, _ := testHub(t, []string{"*"})
	response := hubRequest(t, hub.Handler(), http.MethodPost, "/v1/send", testToken, map[string]string{
		"to": "ada", "from": "oz", "msg": strings.Repeat("x", MaxMessageBytes+1),
	})
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s, want 413", response.Code, response.Body.String())
	}
}

func TestHubEnforcesPeerRateLimit(t *testing.T) {
	hub, _ := testHub(t, []string{"*"})
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
	hub, queue := testHub(t, []string{"*"})
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

func TestHubPollEmptyAndDeletesReturnedMessages(t *testing.T) {
	hub, _ := testHub(t, []string{"*"})
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
	hub, _ := testHub(t, []string{"*"})
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
}
