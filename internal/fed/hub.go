package fed

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"blueprint/internal/msgq"
)

type Hub struct {
	StateDir string
	PeerName string
	Queue    *msgq.Queue
	Peers    map[string]Peer
	Outbox   *Outbox
	Rate     *RateLimiter
	Now      func() time.Time
	Log      io.Writer
	auditMu  sync.Mutex
	unauthMu sync.Mutex
	unauth   rateWindow
	unauthN  int
}

const (
	unauthenticatedRate = 60
	maxAuditBytes       = 8 * 1024 * 1024
)

func NewHub(stateDir, peerName string, queue *msgq.Queue) (*Hub, error) {
	peers, err := LoadPeers(stateDir)
	if err != nil {
		return nil, err
	}
	return &Hub{
		StateDir: stateDir,
		PeerName: peerName,
		Queue:    queue,
		Peers:    peers,
		Outbox:   NewOutbox(stateDir),
		Rate:     NewRateLimiter(DefaultRate),
		Now:      time.Now,
	}, nil
}

type auditEntry struct {
	TS       string `json:"ts"`
	Peer     string `json:"peer"`
	Endpoint string `json:"endpoint"`
	To       string `json:"to,omitempty"`
	Bytes    int    `json:"bytes,omitempty"`
	Count    int    `json:"count,omitempty"`
	Result   int    `json:"result"`
}

func (h *Hub) Handler() http.Handler {
	return http.HandlerFunc(h.serveHTTP)
}

func (h *Hub) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	if !knownEndpoint(request.URL.Path) {
		if !h.allowUnauthenticated() {
			writeError(writer, http.StatusTooManyRequests, "unauthenticated rate limit exceeded")
			return
		}
		writeError(writer, http.StatusNotFound, "not found")
		return
	}

	peer, ok := h.authenticate(request)
	if !ok {
		if !h.allowUnauthenticated() {
			writeError(writer, http.StatusTooManyRequests, "unauthenticated rate limit exceeded")
			return
		}
		writeError(writer, http.StatusUnauthorized, "unauthorized")
		return
	}

	entry := auditEntry{TS: h.Now().UTC().Format(time.RFC3339Nano), Endpoint: request.URL.Path}
	status := http.StatusOK
	defer func() {
		entry.Result = status
		if err := h.audit(entry); err != nil {
			h.logAuditError(err)
		}
	}()
	entry.Peer = peer

	switch request.URL.Path {
	case "/v1/ping":
		if request.Method != http.MethodGet {
			status = http.StatusMethodNotAllowed
			writeError(writer, status, "method not allowed")
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "peer": h.PeerName})
	case "/v1/send":
		if request.Method != http.MethodPost {
			status = http.StatusMethodNotAllowed
			writeError(writer, status, "method not allowed")
			return
		}
		status = h.handleSend(writer, request, peer, &entry)
	case "/v1/poll":
		if request.Method != http.MethodGet {
			status = http.StatusMethodNotAllowed
			writeError(writer, status, "method not allowed")
			return
		}
		messages, err := h.Outbox.Poll(peer)
		if err != nil {
			status = http.StatusInternalServerError
			writeError(writer, status, err.Error())
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"messages": messages})
	case "/v1/ack":
		if request.Method != http.MethodPost {
			status = http.StatusMethodNotAllowed
			writeError(writer, status, "method not allowed")
			return
		}
		status = h.handleAck(writer, request, peer)
	}
}

func knownEndpoint(path string) bool {
	switch path {
	case "/v1/ping", "/v1/send", "/v1/poll", "/v1/ack":
		return true
	default:
		return false
	}
}

func (h *Hub) allowUnauthenticated() bool {
	h.unauthMu.Lock()
	now := h.Now()
	var summary *auditEntry
	if h.unauth.Started.IsZero() {
		h.unauth.Started = now
	} else if now.Sub(h.unauth.Started) >= time.Minute {
		if h.unauthN > 0 {
			summary = &auditEntry{
				TS:       now.UTC().Format(time.RFC3339Nano),
				Endpoint: "(unauthenticated)",
				Count:    h.unauthN,
				Result:   http.StatusUnauthorized,
			}
		}
		h.unauth = rateWindow{Started: now}
		h.unauthN = 0
	}
	allowed := h.unauth.Count < unauthenticatedRate
	if allowed {
		h.unauth.Count++
		h.unauthN++
	}
	h.unauthMu.Unlock()
	if summary != nil {
		if err := h.audit(*summary); err != nil {
			h.logAuditError(err)
		}
	}
	return allowed
}

func (h *Hub) authenticate(request *http.Request) (string, bool) {
	header := request.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return "", false
	}
	token := strings.TrimPrefix(header, "Bearer ")
	if !validHexToken(token) {
		return "", false
	}
	candidate := sha256.Sum256([]byte(token))
	match := ""
	for _, name := range PeerNames(h.Peers) {
		expected := sha256.Sum256([]byte(h.Peers[name].Token))
		if subtle.ConstantTimeCompare(candidate[:], expected[:]) == 1 {
			match = name
			break
		}
	}
	return match, match != ""
}

func (h *Hub) handleAck(writer http.ResponseWriter, request *http.Request, peerName string) int {
	body, err := io.ReadAll(io.LimitReader(request.Body, 128*1024+1))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "read request body")
		return http.StatusBadRequest
	}
	if len(body) > 128*1024 {
		writeError(writer, http.StatusRequestEntityTooLarge, "request body too large")
		return http.StatusRequestEntityTooLarge
	}
	var input struct {
		IDs []string `json:"ids"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || ensureJSONEnd(decoder) != nil {
		writeError(writer, http.StatusBadRequest, "invalid JSON body")
		return http.StatusBadRequest
	}
	if len(input.IDs) > MaxPeerMessages {
		writeError(writer, http.StatusBadRequest, fmt.Sprintf("ids exceeds %d entries", MaxPeerMessages))
		return http.StatusBadRequest
	}
	acked, err := h.Outbox.Ack(peerName, input.IDs)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return http.StatusBadRequest
	}
	writeJSON(writer, http.StatusOK, map[string]int{"acked": acked})
	return http.StatusOK
}

func (h *Hub) handleSend(writer http.ResponseWriter, request *http.Request, peerName string, audit *auditEntry) int {
	body, err := io.ReadAll(io.LimitReader(request.Body, 128*1024+1))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "read request body")
		return http.StatusBadRequest
	}
	if len(body) > 128*1024 {
		writeError(writer, http.StatusRequestEntityTooLarge, "request body too large")
		return http.StatusRequestEntityTooLarge
	}
	var input struct {
		To   string `json:"to"`
		From string `json:"from"`
		Msg  string `json:"msg"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid JSON body")
		return http.StatusBadRequest
	}
	if err := ensureJSONEnd(decoder); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid JSON body")
		return http.StatusBadRequest
	}
	audit.Bytes = len([]byte(input.Msg))
	if audit.Bytes > MaxMessageBytes {
		writeError(writer, http.StatusRequestEntityTooLarge, fmt.Sprintf("msg exceeds %d bytes", MaxMessageBytes))
		return http.StatusRequestEntityTooLarge
	}
	audit.To = input.To
	if err := validateName(input.To); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid to: "+err.Error())
		return http.StatusBadRequest
	}
	if err := validateName(input.From); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid from: "+err.Error())
		return http.StatusBadRequest
	}
	input.Msg = sanitize(input.Msg)
	peer := h.Peers[peerName]
	if !exposed(peer, input.To) {
		writeError(writer, http.StatusForbidden, "target is not exposed to this peer")
		return http.StatusForbidden
	}
	if !h.Rate.Allow(peerName) {
		writeError(writer, http.StatusTooManyRequests, "peer rate limit exceeded")
		return http.StatusTooManyRequests
	}
	from := input.From + "@" + peerName
	id, err := h.Queue.Enqueue(input.To, from, "["+from+"] "+input.Msg)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err.Error())
		return http.StatusInternalServerError
	}
	if err := Journal(h.StateDir, "in", id, from, input.To, input.Msg); err != nil {
		h.logAuditError(err)
	}
	writeJSON(writer, http.StatusOK, map[string]string{"id": id})
	return http.StatusOK
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return fmt.Errorf("extra JSON value")
	}
	return err
}

func (h *Hub) audit(entry auditEntry) error {
	h.auditMu.Lock()
	defer h.auditMu.Unlock()
	dir := filepath.Join(h.StateDir, "fed")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := filepath.Join(dir, "log.jsonl")
	if info, statErr := os.Stat(path); statErr == nil && info.Size()+int64(len(data)) > maxAuditBytes {
		backup := path + ".1"
		if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Rename(path, backup); err != nil {
			return err
		}
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(data)
	return err
}

func (h *Hub) logAuditError(err error) {
	if h.Log != nil {
		fmt.Fprintf(h.Log, "fed: audit write failed: %v\n", err)
	}
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, message string) {
	writeJSON(writer, status, map[string]string{"error": message})
}

// Serve starts the hub listener on exactly listen and stops it with ctx.
func (h *Hub) Serve(ctx context.Context, listen string) error {
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: h.Handler(), ReadHeaderTimeout: 5 * time.Second}
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = server.Shutdown(shutdown)
			cancel()
		case <-stopped:
		}
	}()
	err = server.Serve(listener)
	close(stopped)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
