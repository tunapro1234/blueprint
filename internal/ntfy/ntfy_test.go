package ntfy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSendPostsNotification(t *testing.T) {
	var called bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		called = true
		if request.Method != http.MethodPost || request.URL.Path != "/base/alerts" {
			t.Errorf("request=%s %s, want POST /base/alerts", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("Authorization=%q, want Bearer secret", got)
		}
		if got := request.Header.Get("Content-Type"); got != "text/plain; charset=utf-8" {
			t.Errorf("Content-Type=%q", got)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		if got := string(body); got != "[agent] hello" {
			t.Errorf("body=%q, want %q", got, "[agent] hello")
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	config := &Config{URL: server.URL + "/base/", Topic: "alerts", Token: "secret"}
	if err := Send(context.Background(), config, "[agent] hello"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("ntfy server was not called")
	}
}

func TestSendWithoutConfigDoesNothing(t *testing.T) {
	previous := client
	client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("HTTP client was called without ntfy config")
		return nil, nil
	})}
	t.Cleanup(func() { client = previous })

	if err := Send(context.Background(), nil, "hello"); err != nil {
		t.Fatal(err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
