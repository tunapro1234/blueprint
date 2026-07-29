// Package codexrpc reads thread state from a Codex app-server.
package codexrpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

const maxMessageSize = 32 << 20

var ErrClosed = errors.New("codex app-server connection is closed")

type messageConn interface {
	ReadMessage() ([]byte, error)
	WriteMessage([]byte) error
	Close() error
}

// Notification is an app-server notification not associated with a request id.
type Notification struct {
	Method string
	Params json.RawMessage
}

// RPCError is an error returned by the app-server.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("codex app-server error %d: %s", e.Code, e.Message)
}

// Thread is the read-only part of a thread/list entry used by blueprint.
type Thread struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	AgentNickname string            `json:"agentNickname"`
	CWD           string            `json:"cwd"`
	Status        ThreadStatus      `json:"status"`
	TokenUsage    *ThreadTokenUsage `json:"tokenUsage,omitempty"`
}

type ThreadStatus struct {
	Type        string   `json:"type"`
	ActiveFlags []string `json:"activeFlags,omitempty"`
}

type ThreadTokenUsage struct {
	Total              TokenUsage `json:"total"`
	Last               TokenUsage `json:"last"`
	ModelContextWindow *int64     `json:"modelContextWindow"`
}

type TokenUsage struct {
	InputTokens           int64 `json:"inputTokens"`
	CachedInputTokens     int64 `json:"cachedInputTokens"`
	OutputTokens          int64 `json:"outputTokens"`
	ReasoningOutputTokens int64 `json:"reasoningOutputTokens"`
	TotalTokens           int64 `json:"totalTokens"`
}

type callResult struct {
	result json.RawMessage
	err    error
}

type Client struct {
	conn          messageConn
	mu            sync.Mutex
	nextID        uint64
	pending       map[uint64]chan callResult
	closed        bool
	closeErr      error
	notifications chan Notification
	statuses      map[string]ThreadStatus
	tokenUsage    map[string]ThreadTokenUsage
}

// DialUnix connects to an app-server WebSocket on a Unix domain socket and
// completes the initialize handshake.
func DialUnix(ctx context.Context, path string) (*Client, error) {
	conn, err := dialWebSocketUnix(ctx, path)
	if err != nil {
		return nil, err
	}
	return connect(ctx, conn)
}

// ConnectStdio attaches to the stdout and stdin pipes of a running app-server
// process and completes the initialize handshake.
func ConnectStdio(ctx context.Context, stdout io.ReadCloser, stdin io.WriteCloser) (*Client, error) {
	conn := &stdioConn{
		scanner: bufio.NewScanner(stdout),
		writer:  stdin,
		closers: []io.Closer{stdout, stdin},
	}
	conn.scanner.Buffer(make([]byte, 64<<10), maxMessageSize)
	return connect(ctx, conn)
}

func connect(ctx context.Context, conn messageConn) (*Client, error) {
	c := &Client{
		conn:          conn,
		pending:       make(map[uint64]chan callResult),
		notifications: make(chan Notification, 128),
		statuses:      make(map[string]ThreadStatus),
		tokenUsage:    make(map[string]ThreadTokenUsage),
	}
	go c.readLoop()
	var initialized json.RawMessage
	err := c.call(ctx, "initialize", map[string]any{
		"clientInfo": map[string]string{
			"name":    "blueprint",
			"title":   "Blueprint bp CLI",
			"version": "dev",
		},
	}, &initialized)
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialize codex app-server: %w", err)
	}
	return c, nil
}

// Notifications returns server notifications received while the client is open.
func (c *Client) Notifications() <-chan Notification {
	return c.notifications
}

// ThreadList returns all non-archived interactive threads, following pagination.
func (c *Client) ThreadList(ctx context.Context) ([]Thread, error) {
	type page struct {
		Data       []Thread `json:"data"`
		NextCursor *string  `json:"nextCursor"`
	}
	params := map[string]any{"limit": 100}
	var threads []Thread
	seen := make(map[string]bool)
	for {
		var response page
		if err := c.call(ctx, "thread/list", params, &response); err != nil {
			return nil, err
		}
		threads = append(threads, response.Data...)
		if response.NextCursor == nil || *response.NextCursor == "" {
			return c.withNotificationState(threads), nil
		}
		if seen[*response.NextCursor] {
			return nil, fmt.Errorf("thread/list returned repeated cursor")
		}
		seen[*response.NextCursor] = true
		params["cursor"] = *response.NextCursor
	}
}

func (c *Client) call(ctx context.Context, method string, params any, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	if c.closed {
		err := c.closeErr
		if err == nil {
			err = ErrClosed
		}
		c.mu.Unlock()
		return err
	}
	c.nextID++
	id := c.nextID
	wait := make(chan callResult, 1)
	c.pending[id] = wait
	c.mu.Unlock()

	request, err := json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		ID      uint64 `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err == nil {
		err = c.conn.WriteMessage(request)
	}
	if err != nil {
		c.removePending(id)
		return fmt.Errorf("send %s request: %w", method, err)
	}

	select {
	case response := <-wait:
		if response.err != nil {
			return response.err
		}
		if result == nil {
			return nil
		}
		if err := json.Unmarshal(response.result, result); err != nil {
			return fmt.Errorf("decode %s response: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		c.removePending(id)
		return ctx.Err()
	}
}

func (c *Client) removePending(id uint64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Client) readLoop() {
	defer close(c.notifications)
	for {
		data, err := c.conn.ReadMessage()
		if err != nil {
			c.shutdown(err)
			return
		}
		var message struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
			Result  json.RawMessage `json:"result"`
			Error   *RPCError       `json:"error"`
		}
		// The app-server omits the "jsonrpc" field on the wire (verified against
		// 0.145.0), so only reject messages that carry a wrong version.
		if err := json.Unmarshal(data, &message); err != nil || (message.JSONRPC != "" && message.JSONRPC != "2.0") {
			c.shutdown(fmt.Errorf("invalid JSON-RPC message"))
			return
		}
		if len(message.ID) > 0 && string(message.ID) != "null" {
			var id uint64
			if err := json.Unmarshal(message.ID, &id); err != nil {
				// Not one of our ids, so this is a server-to-client request
				// (approvals and the like). A read-only client ignores those.
				continue
			}
			c.mu.Lock()
			wait := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if wait != nil {
				if message.Error != nil {
					wait <- callResult{err: message.Error}
				} else {
					wait <- callResult{result: message.Result}
				}
			}
			continue
		}
		if message.Method != "" {
			c.recordNotification(message.Method, message.Params)
			select {
			case c.notifications <- Notification{Method: message.Method, Params: message.Params}:
			default:
			}
		}
	}
}

func (c *Client) recordNotification(method string, params json.RawMessage) {
	switch method {
	case "thread/status/changed":
		var update struct {
			ThreadID string       `json:"threadId"`
			Status   ThreadStatus `json:"status"`
		}
		if json.Unmarshal(params, &update) == nil && update.ThreadID != "" {
			c.mu.Lock()
			c.statuses[update.ThreadID] = update.Status
			c.mu.Unlock()
		}
	case "thread/tokenUsage/updated":
		var update struct {
			ThreadID   string           `json:"threadId"`
			TokenUsage ThreadTokenUsage `json:"tokenUsage"`
		}
		if json.Unmarshal(params, &update) == nil && update.ThreadID != "" {
			c.mu.Lock()
			c.tokenUsage[update.ThreadID] = update.TokenUsage
			c.mu.Unlock()
		}
	}
}

func (c *Client) withNotificationState(threads []Thread) []Thread {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range threads {
		if status, ok := c.statuses[threads[i].ID]; ok {
			threads[i].Status = status
		}
		if usage, ok := c.tokenUsage[threads[i].ID]; ok {
			usage := usage
			threads[i].TokenUsage = &usage
		}
	}
	return threads
}

func (c *Client) shutdown(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	if errors.Is(err, io.EOF) {
		c.closeErr = ErrClosed
	} else {
		c.closeErr = err
	}
	pending := c.pending
	c.pending = make(map[uint64]chan callResult)
	c.mu.Unlock()
	_ = c.conn.Close()
	for _, wait := range pending {
		wait <- callResult{err: c.closeErr}
	}
}

func (c *Client) Close() error {
	c.shutdown(ErrClosed)
	return nil
}

type stdioConn struct {
	scanner *bufio.Scanner
	writer  io.Writer
	closers []io.Closer
	mu      sync.Mutex
}

func (c *stdioConn) ReadMessage() ([]byte, error) {
	if !c.scanner.Scan() {
		if err := c.scanner.Err(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	return append([]byte(nil), c.scanner.Bytes()...), nil
}

func (c *stdioConn) WriteMessage(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.writer.Write(data); err != nil {
		return err
	}
	_, err := c.writer.Write([]byte{'\n'})
	return err
}

func (c *stdioConn) Close() error {
	var result error
	for _, closer := range c.closers {
		if err := closer.Close(); err != nil && result == nil {
			result = err
		}
	}
	return result
}
