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

	"blueprint/internal/audit"
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
	// Remote means the request crossed a trust boundary (the remote gateway).
	// Like P2P inbound, the caller is external: its label is
	// external:<name>@<transport> and the shared delivery-time frame applies.
	Remote bool
	// PeerID is a remote caller's stable provenance (gateway:<client id>).
	PeerID string
	// Policy limits what the caller reaches; nil means local trust. Core
	// enforces it wherever a request fans out (room posts) or reveals
	// others (room members, history).
	Policy *Policy
}

// Label is the sender label stored with the message and shown in its
// envelope. An unverified caller is visibly marked by its transport.
func (c Caller) Label() string {
	if c.Remote {
		return "external:" + c.Name + "@" + c.Transport
	}
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
	StateAccepted   = msgq.DeliveryAccepted   // stored; not yet delivered
	StateDelivered  = msgq.DeliveryDelivered  // reached the agent (terminal: verified; inbox: read)
	StateUnverified = msgq.DeliveryUnverified // reached the pane, not confirmed
	StateFailed     = msgq.DeliveryFailed     // final; Reason holds the queue status
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
	Kick func(target string)
	// DeliveryFramed is set once msgq frames non-local origins at delivery
	// (Queue.Render, guard.NeedsFrame). The gateway refuses to serve before
	// that.
	DeliveryFramed bool
	// Audit records decisions; the default appends to <state>/audit.jsonl.
	Audit func(audit.Event)
	Frame Framer
	Now   func() time.Time

	rejects    auditBudget
	roomLimits limiter

	dirMu    sync.Mutex
	dirCache []AgentInfo
	dirAt    time.Time
}

// NewCore returns a Core with default audit and framing for stateDir.
func NewCore(stateDir string, queue *msgq.Queue, directory func(context.Context) ([]AgentInfo, error)) *Core {
	return &Core{StateDir: stateDir, Queue: queue, Directory: directory,
		Audit: func(ev audit.Event) { _ = audit.Append(stateDir, ev) },
		Frame: PassthroughFramer{}, Now: time.Now}
}

func (c *Core) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Core) dir() string { return filepath.Join(c.StateDir, "api") }

func (c *Core) inbox() inboxStore { return inboxStore{dir: c.dir(), now: c.now} }

func (c *Core) audit(ev audit.Event) {
	if c.Audit != nil {
		if ev.Time.IsZero() {
			ev.Time = c.now().UTC()
		}
		if ev.Severity == "" {
			ev.Severity = audit.Info
		}
		c.Audit(ev)
	}
}

// auditRejected records a rejection that an unauthenticated or untrusted
// source can trigger at will. Each source gets a budget per minute; events
// over it are counted and reported as one api.audit.suppressed event.
func (c *Core) auditRejected(source string, ev audit.Event) {
	ok, suppressed := c.rejects.admit(source, c.now())
	if suppressed > 0 {
		c.audit(audit.Event{Kind: "api.audit.suppressed", Severity: audit.Warn, Target: source,
			Reason: fmt.Sprintf("%d rejection events over budget in the last minute", suppressed)})
	}
	if ok {
		c.audit(ev)
	}
}

// originTransport is the msgq Origin transport for an API caller. Local
// callers are bp-api/http, bp-api/socket or bp-api/mcp, which
// guard.LocalTransports treats as local. A remote (gateway) caller is mcp,
// and guard.NeedsFrame frames that at delivery.
func originTransport(caller Caller) string {
	if caller.Remote {
		return "mcp"
	}
	return "bp-api/" + caller.Transport
}

// sourceOf describes a remote caller for framing; nil for a local caller.
func sourceOf(caller Caller, room string) *FrameSource {
	if !caller.Remote {
		return nil
	}
	peerID := caller.PeerID
	if peerID == "" {
		peerID = caller.Transport + ":" + caller.Name
	}
	return &FrameSource{Transport: originTransport(caller), Peer: caller.Transport,
		PeerID: peerID, AgentClaim: caller.Name, Room: room}
}

// render applies the Framer to stored external text as an inbox, room or
// board read hands it to an agent: these stores are read directly, not
// delivered through the queue. Text is always stored raw. channel is the
// stable record id. A record marked untrusted without a source (older
// records) is framed as an external MCP caller.
func (c *Core) render(untrusted bool, src *FrameSource, from, channel, text string) (string, error) {
	if (!untrusted && src == nil) || text == "" {
		return text, nil
	}
	if c.Frame == nil {
		return "", errors.New("no framer configured for external text")
	}
	source := FrameSource{Transport: "mcp", AgentClaim: from}
	if src != nil {
		source = *src
	}
	source.Channel = channel
	return c.Frame.Frame(source, text)
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
	kind, reason := "api.send.", ""
	if req.Room != "" {
		kind, reason = "api.room.deliver.", "room "+req.Room
	}
	event := audit.Event{Actor: caller.Label(), Target: req.To, ID: result.ID,
		Fields: map[string]string{"transport": caller.Transport, "route": result.Route}}
	if err != nil {
		event.Kind, event.Severity, event.Reason = kind+"rejected", audit.Warn, strings.TrimSpace(reason+" "+err.Error())
	} else {
		event.Kind, event.Reason = kind+result.State, reason
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
	if len(req.ContextID) > 128 || (req.ContextID != "" && messagetext.Label(req.ContextID) != nil) {
		return SendResult{}, invalid("contextId must be at most 128 printable characters")
	}
	target, err := c.Lookup(ctx, req.To)
	if err != nil {
		return SendResult{}, err
	}
	from := caller.Label()
	if err := messagetext.Sender(from); err != nil {
		return SendResult{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	// Text is stored raw, never framed here: a frame differs on every call
	// and would break the queue's replay check for a retried messageId.
	body := req.Text
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
			Untrusted: caller.Remote, Source: sourceOf(caller, req.Room), Key: key})
		if err != nil {
			return SendResult{}, err
		}
		return inboxResult(item), nil
	}

	envelope := "[" + from + "] "
	if req.Room != "" {
		envelope = "[room " + req.Room + "] " + envelope
	}
	// The P2P inbound pattern: text is stored raw and the Origin says where
	// it came from, so the shared frame applies at delivery. A remote caller
	// is external, with its gateway as the peer alias. It is token-
	// authenticated, but its name is never verified.
	origin := &msgq.Origin{Transport: originTransport(caller), ChannelID: messageID,
		AgentClaim: caller.Name, AgentVerified: caller.Verified && !caller.Remote, PeerAuthenticated: true}
	if src := sourceOf(caller, req.Room); src != nil {
		origin.PeerAlias, origin.PeerID = src.Peer, src.PeerID
	}
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

// maxInboxRead bounds one inbox read.
const maxInboxRead = 200

// StatusFor is Status for a caller: a remote caller sees only messages it
// sent, and anything else reads as not found.
func (c *Core) StatusFor(caller Caller, id string) (SendResult, error) {
	result, err := c.Status(id)
	if err != nil || !caller.Remote {
		return result, err
	}
	from := ""
	if strings.HasPrefix(id, "ib") {
		if item, err := c.inbox().find(id); err == nil {
			from = item.From
		}
	} else if c.Queue != nil {
		if m, err := c.Queue.Record(id); err == nil {
			from = m.From
		} else if entry, ok := c.loggedMessage(id); ok {
			from = entry.From
		}
	}
	if from != caller.Label() {
		return SendResult{}, fmt.Errorf("%w: message %s", ErrNotFound, id)
	}
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
// loggedMessage finds the last final state of id in msgq's message log.
func (c *Core) loggedMessage(id string) (msgq.LogEntry, bool) {
	var last msgq.LogEntry
	found := false
	_ = readJSONL(msgq.MessageLogPath(c.Queue.Root), func(entry msgq.LogEntry) bool {
		if entry.ID == id {
			last, found = entry, true
		}
		return true
	})
	return last, found
}

func queueResult(m msgq.Message) SendResult {
	// The same mapping as P2P, so both report one state for one record.
	state, reason := msgq.DeliveryState(m)
	return SendResult{ID: m.ID, To: m.To, Route: "queue", State: state, Reason: reason}
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
		// Records are pruned after a while; the message log keeps final states.
		if entry, ok := c.loggedMessage(id); ok {
			return queueResult(msgq.Message{ID: entry.ID, To: entry.To, From: entry.From, Msg: entry.Msg,
				TS: entry.TS, Finished: entry.Finished, Status: entry.Status}), nil
		}
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
	c.audit(audit.Event{Kind: "api.agent.register." + decision, Severity: severity(decision), Actor: caller.Label(), Target: name, Reason: detail, Fields: map[string]string{"transport": caller.Transport}})
	return reg, err
}

func (c *Core) register(ctx context.Context, caller Caller, name, description string) (Registration, error) {
	if caller.Remote {
		// A remote client's name and description would reach agents as
		// trusted text; the gateway registers its clients itself.
		return Registration{}, fmt.Errorf("%w: remote clients cannot register agents", ErrForbidden)
	}
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
	c.audit(audit.Event{Kind: "api.agent.unregister." + decision, Severity: severity(decision), Actor: caller.Label(), Target: name, Reason: detail, Fields: map[string]string{"transport": caller.Transport}})
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
		c.audit(audit.Event{Kind: "api.inbox.read.rejected", Severity: audit.Warn, Actor: caller.Label(), Target: agent, Reason: "not the inbox owner", Fields: map[string]string{"transport": caller.Transport}})
		return InboxResult{}, fmt.Errorf("%w: only %s may read its inbox", ErrForbidden, agent)
	}
	if !identity.ValidName(agent) {
		return InboxResult{}, invalid("invalid agent name")
	}
	if limit <= 0 || limit > maxInboxRead {
		limit = maxInboxRead
	}
	items, remaining, err := c.inbox().take(agent, limit, peek, func(item InboxItem) (string, error) {
		return c.render(item.Untrusted, item.Source, item.From, item.ID, item.Text)
	})
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
		c.audit(audit.Event{Kind: "api.inbox.read", Actor: caller.Label(), Target: agent, ID: strings.Join(ids, ","), Fields: map[string]string{"transport": caller.Transport}})
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
