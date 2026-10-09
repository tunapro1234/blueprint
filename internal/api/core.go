// Package api is bp's open surface: a local HTTP API in the A2A shape, an MCP
// server, inboxes for agents bp has no terminal for, rooms and a shared board.
// Core holds the logic; the transports (http.go, mcp*.go, gateway.go) only
// translate requests into Core calls.
package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"blueprint/internal/identity"
	"blueprint/internal/messagetext"
	"blueprint/internal/msgq"
)

// Caller is who is asking, and how much that is worth.
type Caller struct {
	// Name is the label the caller is known by. For local HTTP and for MCP
	// outside a bp terminal it is self-declared.
	Name string
	// Verified means bp proved the name (pane ancestry); only then does a
	// message carry the plain [name] envelope.
	Verified bool
	// Transport is http, socket, mcp or gateway.
	Transport string
	// Remote means the request crossed a trust boundary (the remote gateway):
	// its text is framed as untrusted before any agent sees it.
	Remote bool
}

// Label is the sender label stored with the message and shown in its
// envelope. An unverified caller is visibly marked by its transport.
func (c Caller) Label() string {
	if c.Verified {
		return c.Name
	}
	return c.Transport + ":" + c.Name
}

// AgentInfo is one addressable agent.
type AgentInfo struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"` // terminal or inbox
	State       string `json:"state,omitempty"`
	Parent      string `json:"parent,omitempty"`
	Role        string `json:"role,omitempty"`
	Description string `json:"description,omitempty"`
}

const (
	KindTerminal = "terminal"
	KindInbox    = "inbox"
)

// Delivery states, shared by every transport. They match the P2P states.
const (
	StateAccepted   = "accepted"   // stored; not yet delivered
	StateDelivered  = "delivered"  // reached the agent (terminal: verified; inbox: read)
	StateUnverified = "unverified" // reached the pane, not confirmed
	StateFailed     = "failed"
	StateCanceled   = "canceled"
	StateUnknown    = "unknown"
)

// MaxTextBytes bounds one message body.
const MaxTextBytes = 64 << 10

// Core is the transport-independent API.
type Core struct {
	StateDir string
	Queue    *msgq.Queue
	// Directory lists the terminal agents bp knows (the agentbook with live
	// states). It is read through a short cache.
	Directory func(context.Context) ([]AgentInfo, error)
	// Kick, when set, asks for a delivery pass for one target right away
	// instead of waiting for the daemon's next tick.
	Kick  func(target string)
	Audit func(Event)
	Frame Framer
	Now   func() time.Time

	dirMu    sync.Mutex
	dirCache []AgentInfo
	dirAt    time.Time
}

// NewCore returns a Core with default audit and framing for stateDir.
func NewCore(stateDir string, queue *msgq.Queue, directory func(context.Context) ([]AgentInfo, error)) *Core {
	return &Core{StateDir: stateDir, Queue: queue, Directory: directory,
		Audit: DefaultAudit(stateDir), Frame: InterimFrame, Now: time.Now}
}

func (c *Core) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Core) dir() string { return filepath.Join(c.StateDir, "api") }

func (c *Core) inbox() inboxStore { return inboxStore{dir: c.dir(), now: c.now} }

func (c *Core) audit(e Event) {
	if c.Audit != nil {
		if e.TS == "" {
			e.TS = c.now().UTC().Format(time.RFC3339Nano)
		}
		c.Audit(e)
	}
}

func (c *Core) frame(caller Caller, text string) string {
	if !caller.Remote {
		return text
	}
	frame := c.Frame
	if frame == nil {
		frame = InterimFrame
	}
	return frame(caller.Label(), text)
}

// ErrNotFound reports an unknown agent, message, room or key.
var ErrNotFound = errors.New("not found")

// ErrForbidden reports a request the caller may not make.
var ErrForbidden = errors.New("forbidden")

// ErrInvalid reports a malformed request.
var ErrInvalid = errors.New("invalid request")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// ValidateCaller checks a caller's name has agent-name shape.
func ValidateCaller(caller Caller) error {
	if caller.Name == "" {
		return invalid("caller name required (X-BP-Agent header, metadata bp/from, or --as)")
	}
	if !identity.ValidName(caller.Name) || len(caller.Name) > 64 {
		return invalid("caller name %q is not a valid agent name", caller.Name)
	}
	return nil
}

func validateText(text string) error {
	if strings.TrimSpace(text) == "" {
		return invalid("empty message")
	}
	if len(text) > MaxTextBytes {
		return invalid("message is %d bytes, over the %d byte limit", len(text), MaxTextBytes)
	}
	if err := messagetext.Validate(text); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return nil
}

// Agents lists terminal agents and inbox agents, sorted by name.
func (c *Core) Agents(ctx context.Context) ([]AgentInfo, error) {
	terminal, err := c.directory(ctx)
	if err != nil {
		return nil, err
	}
	regs, err := c.inbox().registrations()
	if err != nil {
		return nil, err
	}
	out := append([]AgentInfo(nil), terminal...)
	seen := map[string]bool{}
	for _, agent := range terminal {
		seen[agent.Name] = true
	}
	for name, reg := range regs {
		if seen[name] {
			continue
		}
		out = append(out, AgentInfo{Name: name, Kind: KindInbox, State: "inbox", Description: reg.Description})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// directoryTTL keeps repeated tool calls from rescanning the fleet.
const directoryTTL = 2 * time.Second

func (c *Core) directory(ctx context.Context) ([]AgentInfo, error) {
	if c.Directory == nil {
		return nil, nil
	}
	c.dirMu.Lock()
	defer c.dirMu.Unlock()
	if c.dirCache != nil && c.now().Sub(c.dirAt) < directoryTTL {
		return c.dirCache, nil
	}
	agents, err := c.Directory(ctx)
	if err != nil {
		return nil, err
	}
	for i := range agents {
		if agents[i].Kind == "" {
			agents[i].Kind = KindTerminal
		}
	}
	c.dirCache, c.dirAt = agents, c.now()
	return agents, nil
}

// Lookup finds one agent by name.
func (c *Core) Lookup(ctx context.Context, name string) (AgentInfo, error) {
	agents, err := c.Agents(ctx)
	if err != nil {
		return AgentInfo{}, err
	}
	for _, agent := range agents {
		if agent.Name == name {
			return agent, nil
		}
	}
	return AgentInfo{}, fmt.Errorf("%w: agent %s", ErrNotFound, name)
}

// SendRequest is one message to one agent.
type SendRequest struct {
	To   string
	Text string
	// MessageID is the caller's id for this message (A2A messageId). The
	// same caller retrying the same id gets the same record back.
	MessageID string
	ContextID string
	// Room is set when the message is a room fan-out.
	Room string
}

// SendResult says where the message went and how far it got.
type SendResult struct {
	ID        string `json:"id"`
	To        string `json:"to"`
	Route     string `json:"route"` // queue or inbox
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
	ContextID string `json:"contextId,omitempty"`
}

// Send delivers one message through the path the target has: the msgq queue
// for a terminal agent (the same dispatch gates as bp msg: never into a busy
// agent, delivery confirmed by the transcript), the inbox for an inbox agent.
func (c *Core) Send(ctx context.Context, caller Caller, req SendRequest) (SendResult, error) {
	result, err := c.send(ctx, caller, req)
	event := Event{Kind: "send", Transport: caller.Transport, Actor: caller.Label(), Target: req.To, ID: result.ID}
	if req.Room != "" {
		event.Kind, event.Detail = "room.deliver", "room "+req.Room
	}
	if err != nil {
		event.Decision, event.Detail = "rejected", strings.TrimSpace(event.Detail+" "+err.Error())
	} else {
		event.Decision = result.State
	}
	c.audit(event)
	return result, err
}

func (c *Core) send(ctx context.Context, caller Caller, req SendRequest) (SendResult, error) {
	if err := ValidateCaller(caller); err != nil {
		return SendResult{}, err
	}
	if strings.Contains(req.To, "@") {
		return SendResult{}, invalid("federated addresses are not available through the API yet; use bp msg")
	}
	if !identity.ValidName(req.To) {
		return SendResult{}, invalid("target %q is not a valid agent name", req.To)
	}
	if err := validateText(req.Text); err != nil {
		return SendResult{}, err
	}
	// A bare slash command would run in the target CLI. bp msg gates that on
	// the hierarchy; the API carries no hierarchy authority, so every message
	// gets an envelope and can never start with "/".
	if len(req.MessageID) > 128 || messagetext.Label(req.MessageID) != nil {
		return SendResult{}, invalid("messageId must be at most 128 printable characters")
	}
	target, err := c.Lookup(ctx, req.To)
	if err != nil {
		return SendResult{}, err
	}
	from := caller.Label()
	if err := messagetext.Sender(from); err != nil {
		return SendResult{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	body := c.frame(caller, req.Text)
	messageID := req.MessageID
	if messageID == "" {
		messageID = randomID("m")
	}
	key := "bp-api-v1:" + caller.Transport + ":" + from + ":" + messageID
	if req.Room != "" {
		key = "bp-room-v1:" + req.Room + ":" + messageID + ":" + req.To
	}

	if target.Kind == KindInbox {
		item, err := c.inbox().add(InboxItem{ID: randomID("ib"), To: req.To, From: from, Text: body,
			TS: float64(c.now().UnixNano()) / 1e9, ContextID: req.ContextID, Room: req.Room,
			Untrusted: caller.Remote, Key: key})
		if err != nil {
			return SendResult{}, err
		}
		return inboxResult(item), nil
	}

	envelope := "[" + from + "] "
	if req.Room != "" {
		envelope = "[room " + req.Room + "] " + envelope
	}
	origin := &msgq.Origin{Transport: "bp-api/" + caller.Transport, ChannelID: messageID,
		AgentClaim: caller.Name, AgentVerified: caller.Verified, PeerAuthenticated: !caller.Remote}
	if c.Queue == nil {
		return SendResult{}, errors.New("message queue unavailable")
	}
	message, err := c.Queue.EnqueueOnceOrigin(key, req.To, from, envelope+body, origin)
	if err != nil {
		return SendResult{}, err
	}
	if c.Kick != nil {
		go c.Kick(req.To)
	}
	result := queueResult(message)
	result.ContextID = req.ContextID
	return result, nil
}

func inboxResult(item InboxItem) SendResult {
	state := StateAccepted
	if item.ReadAt > 0 {
		state = StateDelivered
	}
	return SendResult{ID: item.ID, To: item.To, Route: "inbox", State: state, ContextID: item.ContextID}
}

// queueResult maps a queue record to a state exactly as the P2P status does.
func queueResult(m msgq.Message) SendResult {
	result := SendResult{ID: m.ID, To: m.To, Route: "queue", State: StateAccepted, Reason: m.Reason}
	switch {
	case m.Cleanup:
		result.State = StateDelivered
	case msgq.IsUnverifiedDelivery(m.Status):
		result.State = StateUnverified
	case msgq.IsVerifiedDelivery(m.Status):
		result.State = StateDelivered
	case strings.HasPrefix(m.Status, "cancel"):
		result.State, result.Reason = StateCanceled, m.Status
	case m.Status != "":
		result.State, result.Reason = StateFailed, m.Status
	}
	return result
}

// Status reports the delivery state of a message id returned by Send.
func (c *Core) Status(id string) (SendResult, error) {
	if id == "" || filepath.Base(id) != id || len(id) > 160 {
		return SendResult{}, invalid("invalid message id")
	}
	if strings.HasPrefix(id, "ib") {
		item, err := c.inbox().find(id)
		if errors.Is(err, os.ErrNotExist) {
			return SendResult{}, fmt.Errorf("%w: message %s", ErrNotFound, id)
		}
		if err != nil {
			return SendResult{}, err
		}
		return inboxResult(item), nil
	}
	if c.Queue == nil {
		return SendResult{}, errors.New("message queue unavailable")
	}
	message, err := c.Queue.Record(id)
	if errors.Is(err, os.ErrNotExist) {
		return SendResult{}, fmt.Errorf("%w: message %s", ErrNotFound, id)
	}
	if err != nil {
		return SendResult{}, err
	}
	return queueResult(message), nil
}

// Register makes name an inbox agent. A name that belongs to a terminal
// agent cannot be registered: messages to it already have a path.
func (c *Core) Register(ctx context.Context, caller Caller, name, description string) (Registration, error) {
	reg, err := c.register(ctx, caller, name, description)
	decision := "accepted"
	detail := ""
	if err != nil {
		decision, detail = "rejected", err.Error()
	}
	c.audit(Event{Kind: "agent.register", Decision: decision, Transport: caller.Transport, Actor: caller.Label(), Target: name, Detail: detail})
	return reg, err
}

func (c *Core) register(ctx context.Context, caller Caller, name, description string) (Registration, error) {
	if !identity.ValidName(name) || len(name) > 64 {
		return Registration{}, invalid("%q is not a valid agent name", name)
	}
	if len(description) > 500 || messagetext.Validate(description) != nil {
		return Registration{}, invalid("description must be at most 500 printable characters")
	}
	terminal, err := c.directory(ctx)
	if err != nil {
		return Registration{}, err
	}
	for _, agent := range terminal {
		if agent.Name == name {
			return Registration{}, fmt.Errorf("%w: %s is a terminal agent; it receives messages in its terminal", ErrForbidden, name)
		}
	}
	reg, _, err := c.inbox().register(Registration{Name: name, Description: description, RegisteredBy: caller.Label(), Transport: caller.Transport})
	return reg, err
}

// Unregister removes an inbox agent. Its unread inbox is kept on disk.
func (c *Core) Unregister(caller Caller, name string) error {
	found, err := c.inbox().unregister(name)
	if err == nil && !found {
		err = fmt.Errorf("%w: inbox agent %s", ErrNotFound, name)
	}
	decision, detail := "accepted", ""
	if err != nil {
		decision, detail = "rejected", err.Error()
	}
	c.audit(Event{Kind: "agent.unregister", Decision: decision, Transport: caller.Transport, Actor: caller.Label(), Target: name, Detail: detail})
	return err
}

// InboxResult is one read of an inbox.
type InboxResult struct {
	Agent     string      `json:"agent"`
	Messages  []InboxItem `json:"messages"`
	Remaining int         `json:"remaining"`
}

// Inbox returns unread messages for agent, oldest first, and marks them read
// unless peek is set. Only the agent itself may read its inbox.
func (c *Core) Inbox(caller Caller, agent string, limit int, peek bool) (InboxResult, error) {
	if err := ValidateCaller(caller); err != nil {
		return InboxResult{}, err
	}
	if agent == "" {
		agent = caller.Name
	}
	if agent != caller.Name {
		c.audit(Event{Kind: "inbox.read", Decision: "rejected", Transport: caller.Transport, Actor: caller.Label(), Target: agent, Detail: "not the inbox owner"})
		return InboxResult{}, fmt.Errorf("%w: only %s may read its inbox", ErrForbidden, agent)
	}
	if !identity.ValidName(agent) {
		return InboxResult{}, invalid("invalid agent name")
	}
	items, remaining, err := c.inbox().take(agent, limit, peek)
	if err != nil {
		return InboxResult{}, err
	}
	if items == nil {
		items = []InboxItem{}
	}
	if len(items) > 0 && !peek {
		ids := make([]string, len(items))
		for i, item := range items {
			ids[i] = item.ID
		}
		c.audit(Event{Kind: "inbox.read", Decision: "read", Transport: caller.Transport, Actor: caller.Label(), Target: agent, ID: strings.Join(ids, ",")})
	}
	return InboxResult{Agent: agent, Messages: items, Remaining: remaining}, nil
}

// Cancel withdraws a queued message that has not been delivered yet. Only its
// sender may cancel it. Inbox items cannot be withdrawn once stored.
func (c *Core) Cancel(caller Caller, id string) (SendResult, error) {
	result, err := c.cancel(caller, id)
	c.auditResult(caller, "cancel", result.To, id, err)
	return result, err
}

func (c *Core) cancel(caller Caller, id string) (SendResult, error) {
	if err := ValidateCaller(caller); err != nil {
		return SendResult{}, err
	}
	current, err := c.Status(id)
	if err != nil {
		return SendResult{}, err
	}
	if current.Route != "queue" {
		return current, fmt.Errorf("%w: inbox messages cannot be withdrawn", ErrNotCancelable)
	}
	record, err := c.Queue.Record(id)
	if err != nil {
		return SendResult{}, err
	}
	if record.From != caller.Label() {
		return SendResult{}, fmt.Errorf("%w: only the sender cancels a message", ErrForbidden)
	}
	if current.State != StateAccepted {
		return current, fmt.Errorf("%w: message is already %s", ErrNotCancelable, current.State)
	}
	if _, err := c.Queue.Cancel(id); err != nil {
		return current, fmt.Errorf("%w: %v", ErrNotCancelable, err)
	}
	return c.Status(id)
}

// ErrNotCancelable reports a message that can no longer be withdrawn.
var ErrNotCancelable = errors.New("not cancelable")
