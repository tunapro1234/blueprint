package dashboard

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// indexHTML is a stand-in for the built bundle's index.html: it carries the
// token placeholder and a root element the tests can look for.
const indexHTML = `<!doctype html><html><head>` +
	`<script>window.__BP_TOKEN__ = "__BP_CONSOLE_TOKEN__";</script>` +
	`</head><body><div id="root"></div></body></html>`

func testBundle() fstest.MapFS {
	return fstest.MapFS{
		"index.html":         {Data: []byte(indexHTML)},
		"assets/app.js":      {Data: []byte("console.log('bp')")},
		"assets/app.css":     {Data: []byte("body{}")},
	}
}

func newTestHandler(t *testing.T, token string) http.Handler {
	t.Helper()
	h, err := newConsoleSPA(testBundle(), token)
	if err != nil {
		t.Fatalf("newConsoleSPA: %v", err)
	}
	return h
}

func get(t *testing.T, h http.Handler, method, target string) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return res, string(body)
}

func TestConsoleServesIndexAtRoot(t *testing.T) {
	h := newTestHandler(t, "")
	res, body := get(t, h, http.MethodGet, "/")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("GET / content-type = %q, want text/html", ct)
	}
	if !strings.Contains(body, `<div id="root">`) {
		t.Fatalf("GET / body did not contain the SPA root: %q", body)
	}
	// With no token, the placeholder is replaced with an empty JSON string, never
	// left as the literal marker.
	if strings.Contains(body, tokenPlaceholder) {
		t.Fatalf("GET / still served the raw token placeholder")
	}
	if !strings.Contains(body, `window.__BP_TOKEN__ = "";`) {
		t.Fatalf("GET / did not inject an empty token: %q", body)
	}
}

func TestConsoleInjectsToken(t *testing.T) {
	h := newTestHandler(t, "sek\"ret")
	// An explicit /index.html request must also get the processed page, not the
	// raw bundle file still holding the placeholder.
	res, body := get(t, h, http.MethodGet, "/index.html")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /index.html status = %d", res.StatusCode)
	}
	if strings.Contains(body, tokenPlaceholder) {
		t.Fatalf("GET /index.html served the raw placeholder")
	}
	// The token is JSON-encoded, so a quote in it is escaped and cannot break out
	// of the JS string literal.
	if !strings.Contains(body, `window.__BP_TOKEN__ = "sek\"ret";`) {
		t.Fatalf("token not injected safely: %q", body)
	}
}

func TestConsoleServesRealAsset(t *testing.T) {
	h := newTestHandler(t, "")
	res, body := get(t, h, http.MethodGet, "/assets/app.js")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /assets/app.js status = %d", res.StatusCode)
	}
	if body != "console.log('bp')" {
		t.Fatalf("asset body = %q, want the embedded file", body)
	}
}

func TestConsoleFallsBackToIndexForClientRoutes(t *testing.T) {
	h := newTestHandler(t, "")
	for _, route := range []string{"/org", "/tasks", "/workflows/my-flow", "/usage"} {
		res, body := get(t, h, http.MethodGet, route)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200 (SPA fallback)", route, res.StatusCode)
		}
		if !strings.Contains(body, `<div id="root">`) {
			t.Fatalf("GET %s did not fall back to index.html", route)
		}
	}
}

func TestConsoleMissingAssetIs404(t *testing.T) {
	h := newTestHandler(t, "")
	// A path that looks like a static asset but is absent must 404, not be masked
	// by the HTML shell (which would hide a broken asset URL).
	res, _ := get(t, h, http.MethodGet, "/assets/missing.js")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /assets/missing.js status = %d, want 404", res.StatusCode)
	}
}

func TestConsoleHeadRootOK(t *testing.T) {
	h := newTestHandler(t, "")
	res, _ := get(t, h, http.MethodHead, "/")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("HEAD / status = %d", res.StatusCode)
	}
}

func TestConsoleRejectsNonGet(t *testing.T) {
	h := newTestHandler(t, "")
	res, _ := get(t, h, http.MethodPost, "/")
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST / status = %d, want 405", res.StatusCode)
	}
	if allow := res.Header.Get("Allow"); !strings.Contains(allow, "GET") {
		t.Fatalf("Allow = %q, want it to list GET", allow)
	}
}

func TestNewConsoleSPAFailsWithoutIndex(t *testing.T) {
	_, err := newConsoleSPA(fstest.MapFS{"assets/app.js": {Data: []byte("x")}}, "")
	if err == nil {
		t.Fatal("newConsoleSPA with no index.html must fail closed")
	}
}

// TestConsoleHandlerEmbedsRealBundle checks the production constructor over the
// committed placeholder bundle, so the go:embed wiring itself is exercised.
func TestConsoleHandlerEmbedsRealBundle(t *testing.T) {
	h, err := ConsoleHandler()
	if err != nil {
		t.Fatalf("ConsoleHandler: %v", err)
	}
	res, body := get(t, h, http.MethodGet, "/")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d", res.StatusCode)
	}
	if strings.Contains(body, tokenPlaceholder) {
		t.Fatalf("committed index.html served with an un-replaced placeholder")
	}
}
