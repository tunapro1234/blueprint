package release

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDownloadRetriesBodyStallAndReportsAttemptBytes(t *testing.T) {
	payload := []byte("complete binary")
	manifest := Manifest{Version: "1.0.0", SHA256: map[string]string{Platform(): fmt.Sprintf("%x", sha256.Sum256(payload))}}
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			_, _ = w.Write([]byte("part"))
			w.(http.Flusher).Flush()
			time.Sleep(80 * time.Millisecond)
			_, _ = w.Write(payload)
			return
		}
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	checker := &Checker{Base: server.URL, HTTP: server.Client(), StallTimeout: 20 * time.Millisecond, DownloadRetry: 1, RetryBackoff: time.Millisecond}
	got, err := checker.Download(context.Background(), manifest, Platform())
	if err != nil || string(got) != string(payload) {
		t.Fatalf("download=%q err=%v", got, err)
	}
	if hits.Load() != 2 {
		t.Fatalf("requests=%d, want retry after first stalled body", hits.Load())
	}
}

func TestDownloadUsesPerReadStallLimitAndReportsURLAndBytes(t *testing.T) {
	payload := []byte("abc")
	manifest := Manifest{Version: "1.0.0", SHA256: map[string]string{Platform(): fmt.Sprintf("%x", sha256.Sum256(payload))}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher := w.(http.Flusher)
		for _, part := range []string{"a", "b", "c"} {
			_, _ = w.Write([]byte(part))
			flusher.Flush()
			time.Sleep(20 * time.Millisecond)
		}
	}))
	defer server.Close()
	checker := &Checker{Base: server.URL, HTTP: server.Client(), StallTimeout: 60 * time.Millisecond, DownloadRetry: 0}
	got, err := checker.Download(context.Background(), manifest, Platform())
	if err != nil || string(got) != string(payload) {
		t.Fatalf("progressing body failed: %q err=%v", got, err)
	}

	stalling := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("four"))
		w.(http.Flusher).Flush()
		time.Sleep(100 * time.Millisecond)
	}))
	defer stalling.Close()
	checker.Base = stalling.URL
	checker.StallTimeout = 15 * time.Millisecond
	manifest.SHA256[Platform()] = fmt.Sprintf("%x", sha256.Sum256([]byte("wrong")))
	_, err = checker.Download(context.Background(), manifest, Platform())
	if err == nil || !strings.Contains(err.Error(), stalling.URL+"/releases/v1.0.0/"+Platform()) || !strings.Contains(err.Error(), "after 4 bytes") {
		t.Fatalf("stall error=%v, want URL and partial byte count", err)
	}
}
