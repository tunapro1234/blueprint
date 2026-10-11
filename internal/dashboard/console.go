package dashboard

// console.go serves the fleet console SPA (docs/fleet-console.md, WS5): the Vite
// bundle embedded by ui/console, with history-route fallback to index.html. It
// is static files only. It never reads bp state, delivers a message or types
// into a pane, and it adds no auth of its own: the integrator mounts it behind
// the ui module on loopback and delegates /v1 to the API handlers, which keep
// their own token auth. Keeping this handler inert is what lets the console sit
// on the same origin as the token-authed API without becoming a way past it.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"

	consoleui "blueprint/ui/console"
)

// tokenPlaceholder marks where index.html carries the loopback API token. The
// built index.html contains `window.__BP_TOKEN__ = "__BP_CONSOLE_TOKEN__";`; the
// handler replaces the quoted placeholder with a JSON-encoded token (or "" when
// none is injected) so the page can send it as the Bearer header on every /v1
// fetch. The placeholder stays quoted in the source so the page is still valid
// JavaScript under `npm run dev`, where nothing replaces it; the SPA treats the
// literal placeholder value as "no token".
const tokenPlaceholder = "__BP_CONSOLE_TOKEN__"

// ConsoleHandler serves the embedded fleet console with no API token injected.
// The SPA then issues same-origin /v1 requests without an Authorization header,
// which suits a unix-socket or otherwise pre-authenticated mount. Use
// ConsoleHandlerWithToken when the console is served over the token-authed
// loopback TCP API.
func ConsoleHandler() (http.Handler, error) {
	return consoleHandler("")
}

// ConsoleHandlerWithToken serves the embedded fleet console with token injected
// into index.html as window.__BP_TOKEN__, so the SPA can authenticate its /v1
// requests against the loopback TCP API. An empty token behaves like
// ConsoleHandler.
func ConsoleHandlerWithToken(token string) (http.Handler, error) {
	return consoleHandler(token)
}

func consoleHandler(token string) (http.Handler, error) {
	dist, err := fs.Sub(consoleui.Assets, "dist")
	if err != nil {
		return nil, fmt.Errorf("console assets: %w", err)
	}
	return newConsoleSPA(dist, token)
}

// consoleSPA serves one embedded SPA bundle: real asset files straight from the
// bundle, and index.html for the app root and for client-side routes.
type consoleSPA struct {
	dist  fs.FS
	files http.Handler
	index []byte
}

// newConsoleSPA fails closed: a bundle with no index.html is a broken or
// unbuilt console, and returning an error keeps the daemon from serving a blank
// page that looks live. The token is folded into the index bytes once here, so
// every request serves the processed page and never the raw placeholder.
func newConsoleSPA(dist fs.FS, token string) (http.Handler, error) {
	raw, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		return nil, fmt.Errorf("console bundle has no index.html (run the vite build in ui/console): %w", err)
	}
	return &consoleSPA{
		dist:  dist,
		files: http.FileServer(http.FS(dist)),
		index: injectToken(raw, token),
	}, nil
}

func (c *consoleSPA) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	switch {
	case name == "" || name == "." || name == "index.html":
		// The app root and an explicit index.html request both get the processed
		// page, never the raw bundle file (which still holds the token placeholder).
		c.serveIndex(w, r)
	case c.isAsset(name):
		c.files.ServeHTTP(w, r)
	case path.Ext(name) != "":
		// A path that names a static asset but does not exist is genuinely
		// missing; 404 it rather than masking a bad asset URL with HTML.
		http.NotFound(w, r)
	default:
		// An extensionless unknown path is a client route; hand it index.html so
		// the SPA router resolves the deep link (history routing).
		c.serveIndex(w, r)
	}
}

// isAsset reports whether name is a real file in the bundle (not a directory).
func (c *consoleSPA) isAsset(name string) bool {
	info, err := fs.Stat(c.dist, name)
	return err == nil && !info.IsDir()
}

func (c *consoleSPA) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// index.html carries the token and must never be a stale cached copy; the
	// hashed assets beside it are safe to cache and the FileServer sets that.
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(c.index)
}

// injectToken replaces the quoted token placeholder in index with a JSON-encoded
// token, so the value lands inside the page's JavaScript as a safely escaped
// string literal. An empty token becomes "", which the SPA reads as "no token".
// The placeholder is matched with its surrounding quotes so the replacement is a
// complete JS string literal, never nested quotes.
func injectToken(index []byte, token string) []byte {
	encoded, err := json.Marshal(token)
	if err != nil {
		encoded = []byte(`""`)
	}
	quotedPlaceholder := []byte(`"` + tokenPlaceholder + `"`)
	return bytes.Replace(index, quotedPlaceholder, encoded, 1)
}
