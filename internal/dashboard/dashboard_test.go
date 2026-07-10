package dashboard

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRemoteHandlerCachesIndexAndProxiesDashboardFiles(t *testing.T) {
	var mutex sync.Mutex
	requests := map[string]int{}
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mutex.Lock()
		requests[request.URL.RequestURI()]++
		mutex.Unlock()
		switch request.URL.Path {
		case "/monitor/index.html":
			writer.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(writer, `<script src="assets/ui.js"></script>`)
		case "/monitor/assets/ui.js":
			_, _ = io.WriteString(writer, `fetch("data.json")`)
		case "/monitor/data.json":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"fresh":true}`)
		case "/monitor/ansiklopedi.json":
			_, _ = io.WriteString(writer, `{"terms":[]}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer upstream.Close()

	missingSite := filepath.Join(t.TempDir(), "missing")
	handler, err := NewHandler(context.Background(), missingSite, upstream.URL+"/monitor", upstream.Client())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	assertBody := func(path, want string) {
		t.Helper()
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK || string(body) != want {
			t.Fatalf("GET %s: status=%d body=%q, want %q", path, response.StatusCode, body, want)
		}
	}

	assertBody("/", `<script src="assets/ui.js"></script>`)
	assertBody("/index.html", `<script src="assets/ui.js"></script>`)
	assertBody("/assets/ui.js", `fetch("data.json")`)
	assertBody("/data.json?t=123", `{"fresh":true}`)
	assertBody("/ansiklopedi.json", `{"terms":[]}`)

	mutex.Lock()
	defer mutex.Unlock()
	if requests["/monitor/index.html"] != 1 {
		t.Fatalf("index fetched %d times, want once", requests["/monitor/index.html"])
	}
	if requests["/monitor/data.json?t=123"] != 1 {
		t.Fatalf("data query was not proxied: requests=%v", requests)
	}
}

func TestHandlerServesExistingSiteWithoutUpstream(t *testing.T) {
	site := t.TempDir()
	if err := os.WriteFile(filepath.Join(site, "index.html"), []byte("local dashboard"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(site, "data.json"), []byte(`{"local":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	handler, err := NewHandler(context.Background(), site, "://invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/data.json", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if got := strings.TrimSpace(recorder.Body.String()); got != `{"local":true}` {
		t.Fatalf("body=%q, want local data", got)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q, want no-store", got)
	}
}

func TestOnlyExplicitDataRequestAsksForRefresh(t *testing.T) {
	for _, test := range []struct {
		path string
		want bool
	}{
		{"/data.json?t=1", false},
		{"/data.json?t=1&refresh=1", true},
		{"/index.html?refresh=1", false},
	} {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		got := explicitDataRefresh(request)
		if got != test.want {
			t.Errorf("path=%q refresh=%v, want %v", test.path, got, test.want)
		}
	}
}

func TestParseURLRejectsUnsafeOrIncompleteValues(t *testing.T) {
	for _, value := range []string{"monitor.example.com", "file:///tmp/site", "https://example.com/?token=x"} {
		if _, err := parseURL(value); err == nil {
			t.Errorf("parseURL(%q) succeeded, want error", value)
		}
	}
}
