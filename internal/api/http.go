package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"blueprint/internal/audit"
	"blueprint/internal/identity"
)

// Server is the local HTTP API: A2A (HTTP+JSON and JSON-RPC bindings), MCP
// over streamable HTTP, and a small bp-native REST surface under /v1. It is
// meant for 127.0.0.1 and the per-user unix socket only; the remote gateway
// (gateway.go) is a separate server with its own policy.
type Server struct {
	Core *Core
	// Token is required as "Authorization: Bearer <token>" on TCP. Unix
	// socket connections are checked by uid instead and need none.
	Token string
}

// maxBody bounds a request body; every accepted payload is far smaller.
const maxBody = 1 << 20

type ctxKey int

const socketConn ctxKey = iota

// HTTPServer returns an http.Server for s with conservative timeouts.
func (s *Server) HTTPServer() *http.Server {
	return &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      5 * time.Minute, // a cancel can wait for a delivery pass
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    32 << 10,
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			if _, ok := conn.(*net.UnixConn); ok {
				return context.WithValue(ctx, socketConn, true)
			}
			return ctx
		},
	}
}

// Handler returns the routed, authenticated handler.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serve)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	viaSocket, _ := r.Context().Value(socketConn).(bool)
	transport := "http"
	if viaSocket {
		transport = "socket"
	} else {
		// The listener is loopback-only; this keeps it so if one is ever
		// handed another listener. Local transports (bp-api/http) must never
		// carry a caller from off this machine.
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err != nil || !net.ParseIP(host).IsLoopback() {
			s.reject(w, r, transport, http.StatusForbidden, "only loopback callers")
			return
		}
		if !loopbackHost(r.Host) {
			// DNS rebinding: a page on evil.example resolving to 127.0.0.1
			// arrives with its own Host.
			s.reject(w, r, transport, http.StatusForbidden, "host not allowed")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !loopbackOrigin(origin) {
			s.reject(w, r, transport, http.StatusForbidden, "origin not allowed")
			return
		}
	}
	if r.Method == http.MethodGet && r.URL.Path == "/.well-known/agent-card.json" {
		writeJSONResponse(w, http.StatusOK, AgentCard(baseURL(r), "", ""))
		return
	}
	if !viaSocket && !tokenEqual(bearer(r), s.Token) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="bp"`)
		s.reject(w, r, transport, http.StatusUnauthorized, "missing or wrong bearer token (bp api token)")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	caller := Caller{Name: r.Header.Get("X-BP-Agent"), Transport: transport}
	path := r.URL.Path
	switch {
	case path == "/mcp":
		s.serveMCP(w, r, caller)
	case path == "/a2a" || strings.HasPrefix(path, "/a2a/"):
		s.serveA2A(w, r, caller, strings.TrimPrefix(path, "/a2a"))
	case strings.HasPrefix(path, "/v1/"):
		s.serveREST(w, r, caller, strings.TrimPrefix(path, "/v1"))
	default:
		writeStatusError(w, http.StatusNotFound, "NOT_FOUND", "no such endpoint", "")
	}
}

func (s *Server) reject(w http.ResponseWriter, r *http.Request, transport string, code int, why string) {
	s.Core.audit(audit.Event{Kind: "api.auth.rejected", Severity: audit.Warn, Reason: why + " " + r.Method + " " + r.URL.Path,
		Fields: map[string]string{"transport": transport}})
	writeStatusError(w, code, http.StatusText(code), why, "")
}

func bearer(r *http.Request) string {
	value, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func loopbackOrigin(origin string) bool {
	u, err := url.Parse(origin)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && loopbackHost(u.Host)
}

func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if host == "" {
		host = "localhost"
	}
	return scheme + "://" + host
}

// --- MCP over streamable HTTP (stateless: no session id, JSON responses) ---

func (s *Server) serveMCP(w http.ResponseWriter, r *http.Request, caller Caller) {
	serveMCPRequest(w, r, NewMCPSession(s.Core, caller, nil))
}

func serveMCPRequest(w http.ResponseWriter, r *http.Request, session *MCPSession) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeStatusError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "this MCP endpoint is stateless: POST only", "")
		return
	}
	if v := r.Header.Get("MCP-Protocol-Version"); v != "" && v != mcpStateless && !contains(mcpVersions, v) {
		writeJSONResponse(w, http.StatusBadRequest, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"),
			Error: &rpcError{Code: rpcUnsupportedVersion, Message: "unsupported protocol version",
				Data: map[string]any{"supported": append([]string{mcpStateless}, mcpVersions...), "requested": v}}})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeStatusError(w, http.StatusRequestEntityTooLarge, "INVALID_ARGUMENT", "request body too large", "")
		return
	}
	reply := session.Handle(r.Context(), body)
	if reply == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(reply)
}

// --- A2A ---

// serveA2A handles /a2a[/agents/<name>]{/rpc,/message:send,/tasks/<id>[:cancel]}.
func (s *Server) serveA2A(w http.ResponseWriter, r *http.Request, caller Caller, rest string) {
	version := r.Header.Get("A2A-Version")
	if version == "" {
		version = r.URL.Query().Get("A2A-Version")
	}
	switch version {
	case "", "0.3", "0.3.0":
		version = a2aV03
	case "1.0", "1":
		version = a2aV1
	default:
		writeA2AError(w, errVersion(version))
		return
	}
	agent := ""
	if after, ok := strings.CutPrefix(rest, "/agents/"); ok {
		agent, rest, _ = strings.Cut(after, "/")
		rest = "/" + rest
		if !identity.ValidName(agent) {
			writeStatusError(w, http.StatusNotFound, "NOT_FOUND", "no such agent", "")
			return
		}
		if _, err := s.Core.Lookup(r.Context(), agent); err != nil {
			writeA2AError(w, toA2AError(err))
			return
		}
	}
	switch {
	case r.Method == http.MethodGet && (rest == "/.well-known/agent-card.json" || rest == "/card"):
		if agent == "" {
			writeJSONResponse(w, http.StatusOK, AgentCard(baseURL(r), "", ""))
			return
		}
		info, _ := s.Core.Lookup(r.Context(), agent)
		description := info.Role
		if description == "" {
			description = info.Description
		}
		if description == "" {
			description = "bp agent " + agent
		}
		writeJSONResponse(w, http.StatusOK, AgentCard(baseURL(r), agent, description))
	case r.Method == http.MethodPost && rest == "/rpc":
		s.serveA2ARPC(w, r, caller, agent, version)
	case r.Method == http.MethodPost && rest == "/message:send":
		var req struct {
			Message A2AMessage `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeStatusError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "body must be a SendMessageRequest: "+err.Error(), "")
			return
		}
		task, aerr := s.a2aSend(r.Context(), caller, agent, req.Message, version)
		if aerr != nil {
			writeA2AError(w, aerr)
			return
		}
		writeJSONResponse(w, http.StatusOK, a2aSendResponse(task, version))
	case rest == "/message:stream" || strings.HasSuffix(rest, ":subscribe"):
		writeA2AError(w, errUnsupported("streaming is not supported; poll the task"))
	case r.Method == http.MethodGet && rest == "/tasks":
		writeA2AError(w, errUnsupported("listing tasks is not supported; keep the ids you sent"))
	case strings.HasPrefix(rest, "/tasks/"):
		id := strings.TrimPrefix(rest, "/tasks/")
		if r.Method == http.MethodPost && strings.HasSuffix(id, ":cancel") {
			task, aerr := s.a2aCancel(caller, strings.TrimSuffix(id, ":cancel"), version)
			if aerr != nil {
				writeA2AError(w, aerr)
				return
			}
			writeJSONResponse(w, http.StatusOK, task)
			return
		}
		if r.Method != http.MethodGet {
			writeStatusError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "GET a task", "")
			return
		}
		task, aerr := s.a2aGet(id, agent, version)
		if aerr != nil {
			writeA2AError(w, aerr)
			return
		}
		writeJSONResponse(w, http.StatusOK, task)
	default:
		writeStatusError(w, http.StatusNotFound, "NOT_FOUND", "no such A2A endpoint", "")
	}
}

func (s *Server) a2aSend(ctx context.Context, caller Caller, agent string, m A2AMessage, version string) (A2ATask, *a2aError) {
	if caller.Name == "" {
		caller.Name, _ = m.Metadata["bp/from"].(string)
	}
	if agent == "" {
		agent, _ = m.Metadata["bp/to"].(string)
		if agent == "" {
			return A2ATask{}, &a2aError{rpcInvalidParams, 400, "INVALID_ARGUMENT", "name the target: POST to /a2a/agents/<name>, or set message.metadata[\"bp/to\"]"}
		}
	}
	if m.MessageID == "" {
		return A2ATask{}, &a2aError{rpcInvalidParams, 400, "INVALID_ARGUMENT", "message.messageId is required"}
	}
	text, err := a2aText(m)
	if err != nil {
		return A2ATask{}, toA2AError(err)
	}
	result, err := s.Core.Send(ctx, caller, SendRequest{To: agent, Text: text, MessageID: m.MessageID, ContextID: m.ContextID})
	if err != nil {
		return A2ATask{}, toA2AError(err)
	}
	return a2aTask(result, version, s.Core.now()), nil
}

func (s *Server) a2aGet(id, agent, version string) (A2ATask, *a2aError) {
	result, err := s.Core.Status(id)
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalid) || (err == nil && agent != "" && result.To != agent) {
		return A2ATask{}, errTaskNotFound(id)
	}
	if err != nil {
		return A2ATask{}, toA2AError(err)
	}
	return a2aTask(result, version, s.Core.now()), nil
}

func (s *Server) a2aCancel(caller Caller, id, version string) (A2ATask, *a2aError) {
	result, err := s.Core.Cancel(caller, id)
	switch {
	case errors.Is(err, ErrNotFound):
		return A2ATask{}, errTaskNotFound(id)
	case errors.Is(err, ErrNotCancelable):
		return A2ATask{}, errNotCancelable(err.Error())
	case err != nil:
		return A2ATask{}, toA2AError(err)
	}
	return a2aTask(result, version, s.Core.now()), nil
}

func a2aSendResponse(task A2ATask, version string) any {
	if version == a2aV1 {
		return map[string]any{"task": task}
	}
	return task
}

// a2aMethods maps v1.0 and v0.3 JSON-RPC method names to operations.
var a2aMethods = map[string]string{
	"SendMessage": "send", "message/send": "send",
	"GetTask": "get", "tasks/get": "get",
	"CancelTask": "cancel", "tasks/cancel": "cancel",
	"SendStreamingMessage": "stream", "message/stream": "stream",
	"SubscribeToTask": "stream", "tasks/resubscribe": "stream",
	"ListTasks": "list",
}

func (s *Server) serveA2ARPC(w http.ResponseWriter, r *http.Request, caller Caller, agent, version string) {
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONResponse(w, http.StatusOK, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: rpcParseError, Message: "parse error"}})
		return
	}
	if len(req.ID) == 0 {
		req.ID = json.RawMessage("null")
	}
	respond := func(result any, aerr *a2aError) {
		resp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result}
		if aerr != nil {
			resp.Result = nil
			resp.Error = &rpcError{Code: aerr.Code, Message: aerr.Msg, Data: []map[string]any{{
				"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": aerr.Reason, "domain": "a2a-protocol.org"}}}
		}
		writeJSONResponse(w, http.StatusOK, resp)
	}
	var params struct {
		Message A2AMessage `json:"message"`
		ID      string     `json:"id"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			respond(nil, &a2aError{rpcInvalidParams, 400, "INVALID_ARGUMENT", "invalid params: " + err.Error()})
			return
		}
	}
	switch a2aMethods[req.Method] {
	case "send":
		task, aerr := s.a2aSend(r.Context(), caller, agent, params.Message, version)
		if aerr != nil {
			respond(nil, aerr)
			return
		}
		respond(a2aSendResponse(task, version), nil)
	case "get":
		task, aerr := s.a2aGet(params.ID, agent, version)
		respond(task, aerr)
	case "cancel":
		task, aerr := s.a2aCancel(caller, params.ID, version)
		respond(task, aerr)
	case "stream", "list":
		respond(nil, errUnsupported(req.Method+" is not supported"))
	default:
		respond(nil, &a2aError{rpcMethodNotFound, 404, "METHOD_NOT_FOUND", "method not found: " + req.Method})
	}
}

func toA2AError(err error) *a2aError {
	var aerr *a2aError
	switch {
	case errors.As(err, &aerr):
		return aerr
	case errors.Is(err, ErrNotFound):
		return &a2aError{rpcInvalidParams, 404, "NOT_FOUND", err.Error()}
	case errors.Is(err, ErrInvalid):
		return &a2aError{rpcInvalidParams, 400, "INVALID_ARGUMENT", err.Error()}
	case errors.Is(err, ErrForbidden):
		return &a2aError{rpcInvalidRequest, 403, "PERMISSION_DENIED", err.Error()}
	case errors.Is(err, ErrConflict):
		return &a2aError{rpcInvalidParams, 409, "ABORTED", err.Error()}
	case errors.Is(err, ErrNotCancelable):
		return errNotCancelable(err.Error())
	}
	return &a2aError{rpcInternalError, 500, "INTERNAL", err.Error()}
}

func writeA2AError(w http.ResponseWriter, aerr *a2aError) {
	writeStatusError(w, aerr.HTTP, http.StatusText(aerr.HTTP), aerr.Msg, aerr.Reason)
}

// writeStatusError writes the google.rpc.Status shape A2A's REST binding uses;
// the bp-native routes use it too, so a client parses one error format.
func writeStatusError(w http.ResponseWriter, code int, status, message, reason string) {
	body := map[string]any{"code": code, "status": strings.ToUpper(strings.ReplaceAll(status, " ", "_")), "message": message}
	if reason != "" {
		body["details"] = []map[string]any{{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": reason, "domain": "a2a-protocol.org"}}
	}
	writeJSONResponse(w, code, map[string]any{"error": body})
}

func writeJSONResponse(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	aerr := toA2AError(err)
	writeStatusError(w, aerr.HTTP, http.StatusText(aerr.HTTP), aerr.Msg, "")
}

// --- bp-native REST under /v1 ---

func (s *Server) serveREST(w http.ResponseWriter, r *http.Request, caller Caller, path string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	query := r.URL.Query()
	method := r.Method
	decodeBody := func(v any) bool {
		if err := json.NewDecoder(r.Body).Decode(v); err != nil && !errors.Is(err, io.EOF) {
			writeStatusError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid JSON body: "+err.Error(), "")
			return false
		}
		return true
	}
	reply := func(v any, err error) {
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSONResponse(w, http.StatusOK, v)
	}
	ctx := r.Context()
	switch {
	case len(parts) == 1 && parts[0] == "agents" && method == http.MethodGet:
		agents, err := s.Core.Agents(ctx)
		reply(map[string]any{"agents": agents}, err)
	case len(parts) == 1 && parts[0] == "agents" && method == http.MethodPost:
		var body struct{ Name, Description string }
		if decodeBody(&body) {
			if caller.Name == "" {
				caller.Name = body.Name
			}
			reply(s.Core.Register(ctx, caller, body.Name, body.Description))
		}
	case len(parts) == 2 && parts[0] == "agents" && method == http.MethodDelete:
		reply(map[string]any{"removed": parts[1]}, s.Core.Unregister(caller, parts[1]))
	case len(parts) == 1 && parts[0] == "messages" && method == http.MethodPost:
		var body struct {
			To        string `json:"to"`
			Text      string `json:"text"`
			MessageID string `json:"messageId"`
			ContextID string `json:"contextId"`
		}
		if decodeBody(&body) {
			reply(s.Core.Send(ctx, caller, SendRequest{To: body.To, Text: body.Text, MessageID: body.MessageID, ContextID: body.ContextID}))
		}
	case len(parts) == 2 && parts[0] == "messages" && method == http.MethodGet:
		reply(s.Core.Status(parts[1]))
	case len(parts) == 2 && parts[0] == "messages" && method == http.MethodDelete:
		reply(s.Core.Cancel(caller, parts[1]))
	case len(parts) == 1 && parts[0] == "inbox" && method == http.MethodGet:
		limit, _ := strconv.Atoi(query.Get("limit"))
		reply(s.Core.Inbox(caller, query.Get("agent"), limit, query.Get("peek") == "true" || query.Get("peek") == "1"))
	case len(parts) == 1 && parts[0] == "rooms" && method == http.MethodGet:
		rooms, err := s.Core.Rooms()
		reply(map[string]any{"rooms": rooms}, err)
	case len(parts) == 2 && parts[0] == "rooms" && method == http.MethodGet:
		reply(s.Core.Room(parts[1]))
	case len(parts) == 3 && parts[0] == "rooms" && parts[2] == "join" && method == http.MethodPost:
		var body struct {
			Topic  string   `json:"topic"`
			Agents []string `json:"agents"`
		}
		if decodeBody(&body) {
			reply(s.Core.Join(ctx, caller, parts[1], body.Topic, body.Agents))
		}
	case len(parts) == 3 && parts[0] == "rooms" && parts[2] == "leave" && method == http.MethodPost:
		var body struct {
			Agent string `json:"agent"`
		}
		if decodeBody(&body) {
			reply(s.Core.Leave(caller, parts[1], body.Agent))
		}
	case len(parts) == 3 && parts[0] == "rooms" && parts[2] == "posts" && method == http.MethodPost:
		var body struct {
			Text string `json:"text"`
		}
		if decodeBody(&body) {
			reply(s.Core.Post(ctx, caller, parts[1], body.Text))
		}
	case len(parts) == 3 && parts[0] == "rooms" && parts[2] == "posts" && method == http.MethodGet:
		limit, _ := strconv.Atoi(query.Get("limit"))
		room, posts, err := s.Core.RoomRead(caller, parts[1], query.Get("after"), limit)
		reply(map[string]any{"room": room, "posts": posts}, err)
	case len(parts) == 1 && parts[0] == "boards" && method == http.MethodGet:
		boards, err := s.Core.Boards()
		reply(map[string]any{"boards": boards}, err)
	case len(parts) == 2 && parts[0] == "boards" && method == http.MethodGet:
		entries, err := s.Core.BoardGet(parts[1], query.Get("key"), query.Get("prefix"))
		reply(map[string]any{"entries": entries}, err)
	case len(parts) == 3 && parts[0] == "boards" && parts[2] == "history" && method == http.MethodGet:
		limit, _ := strconv.Atoi(query.Get("limit"))
		changes, err := s.Core.BoardHistory(parts[1], query.Get("key"), limit)
		reply(map[string]any{"changes": changes}, err)
	case len(parts) >= 4 && parts[0] == "boards" && parts[2] == "keys" && (method == http.MethodPut || method == http.MethodDelete):
		// The key is the rest of the path, so keys may contain "/".
		key, err := url.PathUnescape(strings.Join(parts[3:], "/"))
		if err != nil {
			writeStatusError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "bad key", "")
			return
		}
		var body struct {
			Value           string `json:"value"`
			ExpectedVersion *int   `json:"expectedVersion"`
		}
		if method == http.MethodPut && !decodeBody(&body) {
			return
		}
		expect := -1
		if body.ExpectedVersion != nil {
			expect = *body.ExpectedVersion
		} else if v := query.Get("expectedVersion"); v != "" {
			expect, _ = strconv.Atoi(v)
		}
		reply(s.Core.BoardPut(caller, parts[1], key, body.Value, expect, method == http.MethodDelete))
	default:
		writeStatusError(w, http.StatusNotFound, "NOT_FOUND", "no such endpoint", "")
	}
}
