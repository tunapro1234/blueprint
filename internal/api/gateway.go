package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The remote gateway lets web chat apps (Claude.ai custom connectors,
// ChatGPT developer-mode connectors) reach bp over streamable HTTP MCP. It
// is off by default, listens on loopback only and is published by a TLS
// reverse proxy the owner sets up.
//
// Every connection belongs to a profile the owner configured (a "client" in
// api.gateway.clients). The profile's expose lists are the session Policy:
// nothing else is visible. The profile name is also the caller's agent name;
// it is registered as an inbox agent so local agents can answer it.
//
// Authentication is a bearer token. Tokens come from either
//   - bp api gateway token <profile>: a static token, for clients that can
//     send a fixed Authorization header, or
//   - the built-in OAuth 2.1 server (dynamic client registration, PKCE S256),
//     which is what Claude.ai and ChatGPT use. The authorize page asks for a
//     one-time pairing code the owner gets from bp api gateway pair <profile>,
//     so only the owner can connect a web app.
// Only token hashes are stored.

// GatewayProfile is one configured client: what it may reach.
type GatewayProfile struct {
	Name   string
	Policy Policy
}

// Gateway serves the remote MCP endpoint and its OAuth server.
type Gateway struct {
	Core *Core
	// PublicURL is the MCP endpoint as clients reach it, e.g.
	// https://bp.example/mcp. The issuer and resource derive from it.
	PublicURL string
	Profiles  map[string]GatewayProfile
	// AllowedOrigins lists browser origins accepted besides the public one.
	AllowedOrigins []string
	// RatePerMinute bounds requests per token (default 120).
	RatePerMinute int

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

var errBadPairing = errors.New("wrong or expired pairing code")

const (
	accessTTL  = time.Hour
	refreshTTL = 30 * 24 * time.Hour
	codeTTL    = 5 * time.Minute
	pairTTL    = 10 * time.Minute
)

// gatewayState is <state>/api/gateway/state.json.
type gatewayState struct {
	// Tokens maps sha256(token) to its grant.
	Tokens map[string]tokenGrant `json:"tokens"`
	// Clients are OAuth clients registered dynamically.
	Clients map[string]oauthClient `json:"clients"`
	// Codes are pending authorization codes, by sha256(code).
	Codes map[string]authCode `json:"codes"`
	// Pairings are one-time pairing codes, by sha256(code).
	Pairings map[string]pairing `json:"pairings"`
}

type tokenGrant struct {
	Profile  string `json:"profile"`
	Kind     string `json:"kind"` // static, access, refresh
	ClientID string `json:"clientId,omitempty"`
	Expires  int64  `json:"expires,omitempty"` // unix seconds; 0 = never
	Created  int64  `json:"created"`
	Resource string `json:"resource,omitempty"`
}

type oauthClient struct {
	Name         string   `json:"name"`
	RedirectURIs []string `json:"redirectUris"`
	Created      int64    `json:"created"`
}

type authCode struct {
	ClientID    string `json:"clientId"`
	RedirectURI string `json:"redirectUri"`
	Challenge   string `json:"challenge"`
	Profile     string `json:"profile"`
	Resource    string `json:"resource,omitempty"`
	Expires     int64  `json:"expires"`
}

type pairing struct {
	Profile string `json:"profile"`
	Expires int64  `json:"expires"`
}

func (g *Gateway) statePath() string { return filepath.Join(g.Core.dir(), "gateway", "state.json") }

// update loads, changes and saves the gateway state under its lock.
func (g *Gateway) update(fn func(*gatewayState) error) error {
	path := g.statePath()
	return withLock(path, func() error {
		state := gatewayState{}
		if err := readJSON(path, &state); err != nil {
			return err
		}
		if state.Tokens == nil {
			state.Tokens = map[string]tokenGrant{}
		}
		if state.Clients == nil {
			state.Clients = map[string]oauthClient{}
		}
		if state.Codes == nil {
			state.Codes = map[string]authCode{}
		}
		if state.Pairings == nil {
			state.Pairings = map[string]pairing{}
		}
		now := g.Core.now().Unix()
		for k, v := range state.Tokens {
			if v.Expires != 0 && v.Expires < now {
				delete(state.Tokens, k)
			}
		}
		for k, v := range state.Codes {
			if v.Expires < now {
				delete(state.Codes, k)
			}
		}
		for k, v := range state.Pairings {
			if v.Expires < now {
				delete(state.Pairings, k)
			}
		}
		if err := fn(&state); err != nil {
			return err
		}
		return writeJSON(path, state)
	})
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func newSecret(prefix string) string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b[:])
}

func (g *Gateway) profile(name string) (GatewayProfile, error) {
	profile, ok := g.Profiles[name]
	if !ok {
		return GatewayProfile{}, fmt.Errorf("%w: no gateway client %q in api.gateway.clients", ErrNotFound, name)
	}
	return profile, nil
}

// IssueStaticToken creates a static bearer token for a profile. Only its
// hash is kept; the caller shows the token to the owner once.
func (g *Gateway) IssueStaticToken(profile string) (string, error) {
	if _, err := g.profile(profile); err != nil {
		return "", err
	}
	token := newSecret("bpg_")
	err := g.update(func(s *gatewayState) error {
		s.Tokens[hashSecret(token)] = tokenGrant{Profile: profile, Kind: "static", Created: g.Core.now().Unix()}
		return nil
	})
	g.Core.audit(Event{Kind: "gateway.token", Decision: "accepted", Transport: "gateway", Actor: "owner", Target: profile, Detail: "static token issued"})
	return token, err
}

// Pair creates a one-time pairing code that authorizes one OAuth connection
// for profile within ten minutes.
func (g *Gateway) Pair(profile string) (string, error) {
	if _, err := g.profile(profile); err != nil {
		return "", err
	}
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	code := strings.ToUpper(hex.EncodeToString(b[:]))
	code = code[:5] + "-" + code[5:]
	err := g.update(func(s *gatewayState) error {
		s.Pairings[hashSecret(code)] = pairing{Profile: profile, Expires: g.Core.now().Add(pairTTL).Unix()}
		return nil
	})
	g.Core.audit(Event{Kind: "gateway.pair", Decision: "accepted", Transport: "gateway", Actor: "owner", Target: profile})
	return code, err
}

// Revoke removes every token of a profile (all, when profile is "").
func (g *Gateway) Revoke(profile string) (int, error) {
	removed := 0
	err := g.update(func(s *gatewayState) error {
		for k, v := range s.Tokens {
			if profile == "" || v.Profile == profile {
				delete(s.Tokens, k)
				removed++
			}
		}
		return nil
	})
	g.Core.audit(Event{Kind: "gateway.revoke", Decision: "accepted", Transport: "gateway", Actor: "owner", Target: profile, Detail: fmt.Sprintf("%d tokens", removed)})
	return removed, err
}

// --- HTTP ---

func (g *Gateway) public() (*url.URL, error) {
	u, err := url.Parse(g.PublicURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("api.gateway.publicUrl is not set")
	}
	return u, nil
}

func (g *Gateway) issuer() string {
	u, err := g.public()
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func (g *Gateway) mcpPath() string {
	u, err := g.public()
	if err != nil || u.Path == "" {
		return "/mcp"
	}
	return u.Path
}

// HTTPServer returns the gateway's http.Server.
func (g *Gateway) HTTPServer() *http.Server {
	return &http.Server{
		Handler:           http.HandlerFunc(g.serve),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    32 << 10,
	}
}

func (g *Gateway) serve(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	path := r.URL.Path
	switch {
	case strings.HasPrefix(path, "/.well-known/oauth-protected-resource"):
		g.resourceMetadata(w)
	case path == "/.well-known/oauth-authorization-server" || path == "/.well-known/openid-configuration":
		g.serverMetadata(w)
	case path == "/oauth/register" && r.Method == http.MethodPost:
		g.register(w, r)
	case path == "/oauth/authorize":
		g.authorize(w, r)
	case path == "/oauth/token" && r.Method == http.MethodPost:
		g.token(w, r)
	case path == g.mcpPath():
		g.serveMCP(w, r)
	default:
		writeStatusError(w, http.StatusNotFound, "NOT_FOUND", "no such endpoint", "")
	}
}

func (g *Gateway) resourceMetadata(w http.ResponseWriter) {
	writeJSONResponse(w, http.StatusOK, map[string]any{
		"resource":                 g.PublicURL,
		"authorization_servers":    []string{g.issuer()},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         []string{"bp"},
		"resource_name":            "bp",
	})
}

func (g *Gateway) serverMetadata(w http.ResponseWriter) {
	issuer := g.issuer()
	writeJSONResponse(w, http.StatusOK, map[string]any{
		"issuer":                                issuer,
		"authorization_endpoint":                issuer + "/oauth/authorize",
		"token_endpoint":                        issuer + "/oauth/token",
		"registration_endpoint":                 issuer + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{"bp"},
	})
}

func (g *Gateway) unauthorized(w http.ResponseWriter, r *http.Request, why string) {
	g.Core.audit(Event{Kind: "auth", Decision: "rejected", Transport: "gateway", Detail: why + " " + clientIP(r)})
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata=%q, scope="bp"`, g.issuer()+"/.well-known/oauth-protected-resource"+g.mcpPath()))
	writeStatusError(w, http.StatusUnauthorized, "UNAUTHENTICATED", why, "")
}

func clientIP(r *http.Request) string {
	// Behind the reverse proxy RemoteAddr is the proxy; the forwarded address
	// is recorded for the audit log only, never trusted for a decision.
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		return "from " + strings.TrimSpace(strings.Split(forwarded, ",")[0])
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return "from " + host
}

// authenticate resolves a bearer token to its profile.
func (g *Gateway) authenticate(r *http.Request) (string, tokenGrant, error) {
	token := bearer(r)
	if token == "" {
		return "", tokenGrant{}, errors.New("missing bearer token")
	}
	var grant tokenGrant
	found := false
	err := g.update(func(s *gatewayState) error {
		grant, found = s.Tokens[hashSecret(token)]
		return nil
	})
	if err != nil {
		return "", tokenGrant{}, err
	}
	if !found || grant.Kind == "refresh" {
		return "", tokenGrant{}, errors.New("unknown or expired token")
	}
	if grant.Resource != "" && grant.Resource != g.PublicURL {
		return "", tokenGrant{}, errors.New("token was issued for another resource")
	}
	if _, ok := g.Profiles[grant.Profile]; !ok {
		return "", tokenGrant{}, errors.New("token's client is no longer configured")
	}
	return hashSecret(token), grant, nil
}

func (g *Gateway) allow(key string) bool {
	rate := g.RatePerMinute
	if rate <= 0 {
		rate = 120
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.buckets == nil {
		g.buckets = map[string]*bucket{}
	}
	now := g.Core.now()
	b, ok := g.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(rate), at: now}
		g.buckets[key] = b
	}
	b.tokens += now.Sub(b.at).Minutes() * float64(rate)
	if b.tokens > float64(rate) {
		b.tokens = float64(rate)
	}
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (g *Gateway) originAllowed(origin string) bool {
	if origin == "" {
		return true
	}
	if origin == g.issuer() {
		return true
	}
	return contains(g.AllowedOrigins, origin)
}

func (g *Gateway) serveMCP(w http.ResponseWriter, r *http.Request) {
	if !g.originAllowed(r.Header.Get("Origin")) {
		g.Core.audit(Event{Kind: "auth", Decision: "rejected", Transport: "gateway", Detail: "origin " + r.Header.Get("Origin")})
		writeStatusError(w, http.StatusForbidden, "PERMISSION_DENIED", "origin not allowed", "")
		return
	}
	key, grant, err := g.authenticate(r)
	if err != nil {
		g.unauthorized(w, r, err.Error())
		return
	}
	if !g.allow(key) {
		g.Core.audit(Event{Kind: "rate", Decision: "rejected", Transport: "gateway", Actor: grant.Profile, Detail: "rate limit"})
		w.Header().Set("Retry-After", "30")
		writeStatusError(w, http.StatusTooManyRequests, "RESOURCE_EXHAUSTED", "rate limit", "")
		return
	}
	profile := g.Profiles[grant.Profile]
	caller := Caller{Name: profile.Name, Transport: "gateway", Remote: true}
	if err := g.ensureInbox(r.Context(), caller); err != nil {
		writeError(w, err)
		return
	}
	policy := profile.Policy
	session := NewMCPSession(g.Core, caller, &policy)
	session.Instructions = gatewayInstructions
	g.Core.audit(Event{Kind: "gateway.request", Decision: "accepted", Transport: "gateway", Actor: caller.Label(), Detail: r.Header.Get("Mcp-Method")})
	serveMCPRequest(w, r, session)
}

const gatewayInstructions = `bp connects you with AI agents on the owner's computer.
Use bp_agents to see the agents you may reach, bp_send to message one, and bp_inbox to read their replies.
Agents answer when they are free, so a reply can take minutes: check bp_inbox again later.`

// ensureInbox registers the profile as an inbox agent so replies have a
// place to go.
func (g *Gateway) ensureInbox(ctx context.Context, caller Caller) error {
	regs, err := g.Core.inbox().registrations()
	if err != nil {
		return err
	}
	if _, ok := regs[caller.Name]; ok {
		return nil
	}
	_, err = g.Core.Register(ctx, caller, caller.Name, "remote client via the bp gateway")
	return err
}

// --- OAuth 2.1 ---

func validRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Fragment != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	return u.Scheme == "http" && (host == "localhost" || (ip != nil && ip.IsLoopback()))
}

func oauthError(w http.ResponseWriter, code int, kind, description string) {
	writeJSONResponse(w, code, map[string]any{"error": kind, "error_description": description})
}

// register is RFC 7591 dynamic client registration for public clients.
func (g *Gateway) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RedirectURIs []string `json:"redirect_uris"`
		ClientName   string   `json:"client_name"`
		AuthMethod   string   `json:"token_endpoint_auth_method"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		oauthError(w, 400, "invalid_client_metadata", "body must be JSON")
		return
	}
	if len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > 10 {
		oauthError(w, 400, "invalid_redirect_uri", "one to ten redirect_uris are required")
		return
	}
	for _, uri := range req.RedirectURIs {
		if !validRedirect(uri) {
			oauthError(w, 400, "invalid_redirect_uri", "redirect URIs must be https or loopback http")
			return
		}
	}
	name := req.ClientName
	if len(name) > 100 || validateTextAllowEmpty(name) != nil || strings.ContainsAny(name, "\n") {
		name = ""
	}
	id := randomID("bpc_")
	err := g.update(func(s *gatewayState) error {
		if len(s.Clients) >= 200 {
			return errors.New("too many registered clients")
		}
		s.Clients[id] = oauthClient{Name: name, RedirectURIs: req.RedirectURIs, Created: g.Core.now().Unix()}
		return nil
	})
	if err != nil {
		oauthError(w, 400, "invalid_client_metadata", err.Error())
		return
	}
	g.Core.audit(Event{Kind: "gateway.register", Decision: "accepted", Transport: "gateway", ID: id, Detail: name + " " + clientIP(r)})
	writeJSONResponse(w, http.StatusCreated, map[string]any{
		"client_id": id, "client_name": name, "redirect_uris": req.RedirectURIs,
		"token_endpoint_auth_method": "none", "grant_types": []string{"authorization_code", "refresh_token"},
		"response_types": []string{"code"}, "client_id_issued_at": g.Core.now().Unix(),
	})
}

var authorizePage = template.Must(template.New("authorize").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Connect to bp</title>
<style>body{font:16px system-ui,sans-serif;max-width:28rem;margin:3rem auto;padding:0 1rem;color:#111;background:#fff}
input,button{font:inherit;padding:.5rem;width:100%;box-sizing:border-box;margin:.25rem 0}
button{background:#2b5cd9;color:#fff;border:0;border-radius:4px}.err{color:#b00020}</style></head>
<body><h1>Connect to bp</h1>
<p><b>{{.Client}}</b> asks to reach agents on this computer.</p>
<p>On the computer, run <code>bp api gateway pair &lt;client&gt;</code> and enter the code it prints.</p>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<form method="post">{{range $k, $v := .Params}}<input type="hidden" name="{{$k}}" value="{{$v}}">{{end}}
<input name="pairing_code" autocomplete="one-time-code" placeholder="XXXXX-XXXXX" required autofocus>
<button type="submit">Connect</button></form></body></html>`))

func (g *Gateway) authorize(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, 400, "invalid_request", "bad form")
		return
	}
	q := r.Form
	clientID, redirect := q.Get("client_id"), q.Get("redirect_uri")
	var client oauthClient
	found := false
	g.update(func(s *gatewayState) error {
		client, found = s.Clients[clientID]
		return nil
	})
	// Errors before the redirect URI is validated must not redirect.
	if !found || !contains(client.RedirectURIs, redirect) {
		oauthError(w, 400, "invalid_request", "unknown client_id or unregistered redirect_uri")
		return
	}
	fail := func(kind, description string) {
		u, _ := url.Parse(redirect)
		values := u.Query()
		values.Set("error", kind)
		values.Set("error_description", description)
		if state := q.Get("state"); state != "" {
			values.Set("state", state)
		}
		u.RawQuery = values.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	}
	if q.Get("response_type") != "code" {
		fail("unsupported_response_type", "only code is supported")
		return
	}
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		fail("invalid_request", "PKCE with S256 is required")
		return
	}
	if resource := q.Get("resource"); resource != "" && resource != g.PublicURL {
		fail("invalid_target", "unknown resource")
		return
	}
	params := map[string]string{}
	for _, key := range []string{"response_type", "client_id", "redirect_uri", "code_challenge", "code_challenge_method", "state", "scope", "resource"} {
		if v := q.Get(key); v != "" {
			params[key] = v
		}
	}
	page := struct {
		Client, Error string
		Params        map[string]string
	}{Client: client.Name, Params: params}
	if page.Client == "" {
		page.Client = "An application"
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	if r.Method != http.MethodPost {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		authorizePage.Execute(w, page)
		return
	}
	code := strings.ToUpper(strings.TrimSpace(q.Get("pairing_code")))
	if !g.allow("pair:" + clientID) {
		page.Error = "Too many attempts. Wait a minute."
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusTooManyRequests)
		authorizePage.Execute(w, page)
		return
	}
	var profile string
	var authCodeValue string
	err := g.update(func(s *gatewayState) error {
		p, ok := s.Pairings[hashSecret(code)]
		if !ok {
			return errBadPairing
		}
		delete(s.Pairings, hashSecret(code)) // one use only
		profile = p.Profile
		authCodeValue = newSecret("bpa_")
		s.Codes[hashSecret(authCodeValue)] = authCode{ClientID: clientID, RedirectURI: redirect,
			Challenge: q.Get("code_challenge"), Profile: profile, Resource: q.Get("resource"),
			Expires: g.Core.now().Add(codeTTL).Unix()}
		return nil
	})
	if err != nil {
		g.Core.audit(Event{Kind: "gateway.authorize", Decision: "rejected", Transport: "gateway", ID: clientID, Detail: "bad pairing code " + clientIP(r)})
		page.Error = "That code is wrong or expired."
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		authorizePage.Execute(w, page)
		return
	}
	g.Core.audit(Event{Kind: "gateway.authorize", Decision: "accepted", Transport: "gateway", Target: profile, ID: clientID})
	u, _ := url.Parse(redirect)
	values := u.Query()
	values.Set("code", authCodeValue)
	if state := q.Get("state"); state != "" {
		values.Set("state", state)
	}
	values.Set("iss", g.issuer())
	u.RawQuery = values.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (g *Gateway) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, 400, "invalid_request", "bad form")
		return
	}
	form := r.PostForm
	now := g.Core.now()
	var grant tokenGrant
	var refreshOld string
	switch form.Get("grant_type") {
	case "authorization_code":
		var code authCode
		found := false
		err := g.update(func(s *gatewayState) error {
			key := hashSecret(form.Get("code"))
			code, found = s.Codes[key]
			delete(s.Codes, key) // single use, even on failure
			return nil
		})
		if err != nil || !found {
			oauthError(w, 400, "invalid_grant", "unknown or expired code")
			return
		}
		sum := sha256.Sum256([]byte(form.Get("code_verifier")))
		challenge := base64.RawURLEncoding.EncodeToString(sum[:])
		if code.ClientID != form.Get("client_id") || code.RedirectURI != form.Get("redirect_uri") ||
			subtle.ConstantTimeCompare([]byte(challenge), []byte(code.Challenge)) != 1 {
			g.Core.audit(Event{Kind: "gateway.token", Decision: "rejected", Transport: "gateway", ID: form.Get("client_id"), Detail: "code exchange mismatch"})
			oauthError(w, 400, "invalid_grant", "client, redirect_uri or code_verifier does not match")
			return
		}
		if resource := form.Get("resource"); resource != "" && resource != g.PublicURL {
			oauthError(w, 400, "invalid_target", "unknown resource")
			return
		}
		grant = tokenGrant{Profile: code.Profile, ClientID: code.ClientID, Resource: g.PublicURL}
	case "refresh_token":
		refreshOld = hashSecret(form.Get("refresh_token"))
		var old tokenGrant
		found := false
		g.update(func(s *gatewayState) error {
			old, found = s.Tokens[refreshOld]
			return nil
		})
		if !found || old.Kind != "refresh" || old.ClientID != form.Get("client_id") {
			oauthError(w, 400, "invalid_grant", "unknown or expired refresh token")
			return
		}
		grant = tokenGrant{Profile: old.Profile, ClientID: old.ClientID, Resource: g.PublicURL}
	default:
		oauthError(w, 400, "unsupported_grant_type", "authorization_code or refresh_token")
		return
	}
	if _, ok := g.Profiles[grant.Profile]; !ok {
		oauthError(w, 400, "invalid_grant", "client is no longer configured")
		return
	}
	access, refresh := newSecret("bpg_"), newSecret("bpr_")
	err := g.update(func(s *gatewayState) error {
		if refreshOld != "" {
			if _, ok := s.Tokens[refreshOld]; !ok {
				return errors.New("refresh token already used")
			}
			delete(s.Tokens, refreshOld) // rotation
		}
		a, rf := grant, grant
		a.Kind, a.Created, a.Expires = "access", now.Unix(), now.Add(accessTTL).Unix()
		rf.Kind, rf.Created, rf.Expires = "refresh", now.Unix(), now.Add(refreshTTL).Unix()
		s.Tokens[hashSecret(access)] = a
		s.Tokens[hashSecret(refresh)] = rf
		return nil
	})
	if err != nil {
		oauthError(w, 400, "invalid_grant", err.Error())
		return
	}
	g.Core.audit(Event{Kind: "gateway.token", Decision: "accepted", Transport: "gateway", Target: grant.Profile, ID: grant.ClientID, Detail: form.Get("grant_type")})
	w.Header().Set("Pragma", "no-cache")
	writeJSONResponse(w, http.StatusOK, map[string]any{
		"access_token": access, "token_type": "Bearer", "expires_in": int(accessTTL.Seconds()),
		"refresh_token": refresh, "scope": "bp",
	})
}
