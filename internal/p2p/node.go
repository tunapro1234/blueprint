package p2p

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"blueprint/internal/audit"
	"blueprint/internal/guard"
	"blueprint/internal/messagetext"
	"blueprint/internal/msgq"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	relayclient "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	ma "github.com/multiformats/go-multiaddr"
)

type presence struct {
	Addresses []string
	Expires   time.Time
}
type Node struct {
	Host          host.Host
	Root          string
	Config        Config
	Queue         *msgq.Queue
	Log           io.Writer
	lock          *os.File
	mdns          mdns.Service
	relay         io.Closer
	mu            sync.Mutex
	discovery     map[peer.ID]presence
	stepMu        sync.Mutex
	discoveryMu   sync.Mutex
	ctx           context.Context
	inbound       chan struct{}
	inboundRetry  time.Duration
	ResolveLookup func(string) LookupResponse
	limits        *limiter
	watch         *guard.Watch
	channelMu     sync.Mutex
	channels      map[string]*channelLock
}

// channelLock serializes inbound handling of one (peer, channel) pair; refs
// counts holders and waiters so the map entry goes away with the last one.
type channelLock struct {
	sync.Mutex
	refs int
}

// lockChannel makes the "is this channel new" check, the rate-limit token and
// the enqueue one step per channel. Without it, concurrent retries of a
// message whose reply was lost all saw no record, each spent a token, and the
// late ones were refused as rate limited although the message was accepted.
func (n *Node) lockChannel(key string) func() {
	n.channelMu.Lock()
	if n.channels == nil {
		n.channels = map[string]*channelLock{}
	}
	l := n.channels[key]
	if l == nil {
		l = &channelLock{}
		n.channels[key] = l
	}
	l.refs++
	n.channelMu.Unlock()
	l.Lock()
	return func() {
		l.Unlock()
		n.channelMu.Lock()
		if l.refs--; l.refs == 0 {
			delete(n.channels, key)
		}
		n.channelMu.Unlock()
	}
}

func New(ctx context.Context, root string, cfg Config, q *msgq.Queue) (*Node, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	lock, err := Lock(root)
	if err != nil {
		return nil, err
	}
	key, err := Identity(root)
	if err != nil {
		lock.Close()
		return nil, err
	}
	listen := cfg.Listen
	if len(listen) == 0 {
		listen = []string{"/ip4/0.0.0.0/tcp/0", "/ip4/0.0.0.0/udp/0/quic-v1"}
	}
	opts := []libp2p.Option{libp2p.Identity(key), libp2p.ListenAddrStrings(listen...), libp2p.EnableRelay(), libp2p.EnableHolePunching(), libp2p.DisableMetrics()}
	if cfg.Relay {
		opts = append(opts, libp2p.ForceReachabilityPublic(), libp2p.EnableNATService())
	}
	if len(cfg.Advertise) > 0 {
		var public []ma.Multiaddr
		for _, a := range cfg.Advertise {
			m, _ := ma.NewMultiaddr(a)
			public = append(public, m)
		}
		opts = append(opts, libp2p.AddrsFactory(func([]ma.Multiaddr) []ma.Multiaddr { return public }))
	}
	h, err := libp2p.New(opts...)
	if err != nil {
		lock.Close()
		return nil, err
	}
	n := &Node{Host: h, Root: root, Config: cfg, Queue: q, Log: os.Stderr, lock: lock, discovery: map[peer.ID]presence{}, ctx: ctx,
		inbound: make(chan struct{}, 1), inboundRetry: 30 * time.Second, limits: newLimiter(),
		watch: guard.NewWatch(guard.WatchConfig{TaintSource: guard.MessageLogTaint(msgq.MessageLogPath(q.Root))}, guard.AuditSink{StateDir: root, Errors: os.Stderr})}
	if cfg.Relay {
		r := relay.DefaultResources()
		r.MaxReservations = 128
		r.MaxCircuits = 8
		r.Limit.Data = 2 * 1024 * 1024
		n.relay, err = relay.New(h, relay.WithResources(r))
		if err != nil {
			n.Close()
			return nil, err
		}
	}
	for _, p := range cfg.Peers {
		id, _ := peer.Decode(p.ID)
		n.addAddresses(id, p.Addresses, peerstore.PermanentAddrTTL)
	}
	// Persisted addresses are hints, never grants of permission or identity.
	var saved map[string][]string
	if err := readJSON(statePath(root, "addresses.json"), &saved); err == nil {
		for idstr, addrs := range saved {
			if id, e := peer.Decode(idstr); e == nil {
				n.addAddresses(id, addrs, peerstore.TempAddrTTL)
			}
		}
	} else if !os.IsNotExist(err) {
		n.Close()
		return nil, fmt.Errorf("read saved peer addresses: %w", err)
	}
	for _, p := range []protocol.ID{MessageProtocol, StatusProtocol, DiscoveryProtocol, PingProtocol, LookupProtocol} {
		p := p
		h.SetStreamHandler(p, func(s network.Stream) { n.handle(s, p) })
	}
	if cfg.MDNS {
		n.mdns = mdns.NewMdnsService(h, "_blueprint._udp", n)
		if err := n.mdns.Start(); err != nil {
			n.Close()
			return nil, err
		}
	}
	return n, nil
}
func (n *Node) Close() error {
	if n.mdns != nil {
		_ = n.mdns.Close()
	}
	if n.relay != nil {
		_ = n.relay.Close()
	}
	err := n.Host.Close()
	_ = n.lock.Close()
	return err
}
func (n *Node) HandlePeerFound(p peer.AddrInfo) {
	for _, allowed := range n.Config.Peers {
		if allowed.ID == p.ID.String() {
			n.Host.Peerstore().AddAddrs(p.ID, p.Addrs, peerstore.TempAddrTTL)
			return
		}
	}
}
func (n *Node) addAddresses(id peer.ID, strings []string, ttl time.Duration) {
	for _, s := range strings {
		if a, e := ma.NewMultiaddr(s); e == nil {
			n.Host.Peerstore().AddAddr(id, a, ttl)
		}
	}
}
func (n *Node) peerPolicy(id peer.ID) (string, Peer, bool) {
	for alias, p := range n.Config.Peers {
		if p.ID == id.String() {
			return alias, p, true
		}
	}
	return "", Peer{}, false
}
func queueKey(id peer.ID, channel string) string { return "bp-p2p-v1:" + id.String() + ":" + channel }
func queueID(id peer.ID, channel string) string {
	return fmt.Sprintf("qp%x", sha256.Sum256([]byte(queueKey(id, channel))))
}

func (n *Node) handle(s network.Stream, p protocol.ID) {
	if p == LookupProtocol {
		n.handleLookup(s)
		return
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(rpcTimeout))
	var req request
	if err := readFrame(s, &req); err != nil {
		_ = s.Reset()
		return
	}
	remote := s.Conn().RemotePeer()
	var res response
	switch p {
	case PingProtocol:
		res = response{ID: n.Host.ID().String(), State: "online"}
	case DiscoveryProtocol:
		res = n.discover(remote, req)
	case MessageProtocol, StatusProtocol:
		alias, policy, ok := n.peerPolicy(remote)
		if !ok {
			res.Error = "peer is not allowed"
			n.reject(remote, "", req, p, res.Error, audit.Warn)
			break
		}
		if !validID(req.ID) {
			res.Error = "invalid channel ID"
			break
		}
		res.ID = req.ID
		var m msgq.Message
		var err error
		if p == MessageProtocol {
			allowed := false
			for _, target := range policy.Expose {
				if target == req.To {
					allowed = true
				}
			}
			if !allowed {
				// Asking for an agent the owner did not expose is the clearest
				// sign of a peer reaching for what it was not given.
				res.Error = "target is not exposed to this peer"
				n.reject(remote, alias, req, p, res.Error, audit.Alert)
				n.watch.Observe(guard.Event{Kind: guard.EvDenied, Peer: alias, Target: req.To, Channel: req.ID, Detail: "not-exposed"})
				break
			}
			if !ValidName(req.To) || len(req.From) > 256 || len(req.Text) > MaxMessageBytes || strings.TrimSpace(req.Text) == "" {
				res.Error = "invalid message"
				n.reject(remote, alias, req, p, res.Error, audit.Warn)
				break
			}
			if err = messagetext.Label(req.From); err != nil {
				res.Error = err.Error()
				n.reject(remote, alias, req, p, res.Error, audit.Warn)
				break
			}
			if err = messagetext.Validate(req.Text); err != nil {
				res.Error = err.Error()
				n.reject(remote, alias, req, p, res.Error, audit.Warn)
				break
			}
			if len(req.Sender.Thread) > 128 || len(req.Sender.Source) > 128 || messagetext.Label(req.Sender.Thread) != nil || messagetext.Label(req.Sender.Source) != nil {
				res.Error = "invalid sender evidence"
				n.reject(remote, alias, req, p, res.Error, audit.Warn)
				break
			}
			unlock := n.lockChannel(queueKey(remote, req.ID))
			_, missing := n.Queue.Record(queueID(remote, req.ID))
			fresh := missing != nil
			if fresh {
				if err = n.admit(remote, alias, policy, req); err != nil {
					unlock()
					res.Error = err.Error()
					break
				}
			}
			from := "external:" + req.From + "@" + alias
			origin := &msgq.Origin{Transport: "libp2p", PeerID: remote.String(), PeerAlias: alias, ChannelID: req.ID,
				PeerAuthenticated: true, AgentClaim: req.From, AgentVerified: false,
				ReportedThread: req.Sender.Thread, ReportedSource: req.Sender.Source, ReportedCertain: req.Sender.Certain}
			// The stored body stays raw; msgq frames it at delivery from Origin.
			m, err = n.Queue.EnqueueOnceOrigin(queueKey(remote, req.ID), req.To, from, "["+from+"] "+req.Text, origin)
			unlock()
			if err == nil && fresh {
				n.auditAccepted(remote, alias, req, m.ID, guard.Scan(req.Text))
			}
		} else {
			m, err = n.Queue.Record(queueID(remote, req.ID))
		}
		if err != nil {
			res.Error = err.Error()
			break
		}
		res.QueueID = m.ID
		res.State, res.Reason = msgq.DeliveryState(m)
		if p == MessageProtocol && res.State == "accepted" {
			n.signalInbound()
		}
	}
	if err := writeFrame(s, res); err != nil {
		_ = s.Reset()
	}
}

func (n *Node) handleLookup(s network.Stream) {
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(rpcTimeout))
	remote := s.Conn().RemotePeer()
	alias, policy, ok := n.peerPolicy(remote)
	if !ok {
		_ = s.Reset()
		return
	}
	var req LookupRequest
	if err := readFrame(s, &req); err != nil || !ValidLookupQuery(req.Find) {
		_ = s.Reset()
		return
	}
	// Every query counts toward enumeration, including throttled ones.
	n.watch.Observe(guard.Event{Kind: guard.EvLookup, Peer: alias, Target: req.Find})
	if !n.limits.admitLookup(remote.String(), policy, time.Now()) {
		// One line per peer per minute while it keeps hitting the limit.
		if n.limits.onceEvery("lookup-rate:"+remote.String(), auditWindow, time.Now()) {
			n.auditRemote(auditSource(remote, alias), audit.Event{Kind: "p2p.lookup.rejected", Severity: audit.Warn, Peer: alias, PeerID: remote.String(), Reason: "lookup rate limit"})
		}
		_ = s.Reset()
		return
	}
	result := LookupResponse{}
	// With nothing exposed every answer is "unknown": skip the agentbook read
	// and tmux probes entirely.
	if n.ResolveLookup != nil && len(policy.Expose) > 0 {
		result = n.ResolveLookup(req.Find)
	}
	if !result.Found || result.Name == "" || (result.State != "live" && result.State != "closed" && result.State != "archived") {
		result = LookupResponse{}
	}
	// A peer only learns about agents its owner exposed to it; anything else
	// answers exactly like an unknown name, so lookup cannot enumerate.
	if result.Found && !exposed(policy, result.Name) {
		n.auditRemote(auditSource(remote, alias), audit.Event{Kind: "p2p.lookup.hidden", Severity: audit.Warn, Peer: alias, PeerID: remote.String(), Target: result.Name, Reason: "lookup matched an agent that is not exposed to this peer"})
		result = LookupResponse{}
	}
	if err := writeFrame(s, result); err != nil {
		_ = s.Reset()
	}
}

func (n *Node) signalInbound() {
	select {
	case n.inbound <- struct{}{}:
	default:
	}
}

// Discovery is a bounded, authenticated address registry. It is not an agent
// directory or an authorization server; callers must already know a Peer ID.
func (n *Node) discover(remote peer.ID, req request) response {
	if !n.Config.Relay {
		return response{Error: "discovery service is disabled"}
	}
	if len(req.Addresses) > 32 {
		return response{Error: "too many addresses"}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	now := time.Now()
	for id, p := range n.discovery {
		if now.After(p.Expires) {
			delete(n.discovery, id)
		}
	}
	if len(req.Addresses) > 0 {
		for _, a := range req.Addresses {
			if len(a) > 1024 {
				return response{Error: "address too long"}
			}
			if _, err := ma.NewMultiaddr(a); err != nil {
				return response{Error: "invalid address"}
			}
		}
		if _, ok := n.discovery[remote]; !ok && len(n.discovery) >= 4096 {
			return response{Error: "discovery capacity reached"}
		}
		n.discovery[remote] = presence{append([]string{}, req.Addresses...), now.Add(10 * time.Minute)}
	}
	res := response{State: "registered"}
	if req.Find != "" {
		id, err := peer.Decode(req.Find)
		if err != nil {
			return response{Error: "invalid peer ID"}
		}
		res.Addresses = append([]string{}, n.discovery[id].Addresses...)
	}
	return res
}

// ConnectDiscovery refreshes relay reservations and address registration. No
// public DHT/bootstrap nodes are used; all rendezvous addresses are explicit.
func (n *Node) ConnectDiscovery(ctx context.Context) error {
	n.discoveryMu.Lock()
	defer n.discoveryMu.Unlock()
	var first error
	for _, address := range n.Config.Rendezvous {
		ai, _ := peer.AddrInfoFromString(address)
		c, cancel := context.WithTimeout(ctx, rpcTimeout)
		err := n.Host.Connect(c, *ai)
		if err == nil {
			// Refresh even before expiry: a relay restart loses reservations while
			// our local expiration would still look valid.
			_, err = relayclient.Reserve(c, n.Host, *ai)
		}
		cancel()
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		addrs := n.Addresses()
		for _, base := range ai.Addrs {
			addrs = append(addrs, base.String()+"/p2p/"+ai.ID.String()+"/p2p-circuit")
		}
		res, err := n.call(ctx, ai.ID, DiscoveryProtocol, request{Addresses: addrs})
		if err == nil && res.Error != "" {
			err = fmt.Errorf("%s", res.Error)
		}
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		for _, p := range n.Config.Peers {
			id, _ := peer.Decode(p.ID)
			found, e := n.call(ctx, ai.ID, DiscoveryProtocol, request{Find: p.ID})
			if e == nil && found.Error == "" {
				n.addAddresses(id, found.Addresses, 15*time.Minute)
			}
			// A known target may be registered after our first discovery call. Relay
			// routes work as soon as its reservation exists, without a directory race.
			for _, base := range ai.Addrs {
				n.addAddresses(id, []string{base.String() + "/p2p/" + ai.ID.String() + "/p2p-circuit"}, 15*time.Minute)
			}
		}
	}
	saved := map[string][]string{}
	for _, p := range n.Config.Peers {
		id, _ := peer.Decode(p.ID)
		for _, a := range n.Host.Peerstore().Addrs(id) {
			saved[p.ID] = append(saved[p.ID], a.String())
		}
	}
	if err := atomicJSON(statePath(n.Root, "addresses.json"), saved); err != nil && first == nil {
		first = err
	}
	return first
}
func (n *Node) Addresses() []string {
	var a []string
	for _, m := range n.Host.Addrs() {
		a = append(a, m.String())
	}
	return a
}

// Step keeps the outbox on the sender until the receiver reports a terminal
// queue outcome. A timeout never means delivered and never changes the ID.
func (n *Node) Step(ctx context.Context) error {
	n.stepMu.Lock()
	defer n.stepMu.Unlock()
	n.FlushAudit()
	channels, err := Channels(n.Root)
	if err != nil {
		return err
	}
	lanes := map[string][]Channel{}
	for _, c := range channels {
		if !terminal(c.State) {
			key := c.PeerID + "/" + c.To
			lanes[key] = append(lanes[key], c)
		}
	}
	jobs := make(chan []Channel, len(lanes))
	for _, lane := range lanes {
		jobs <- lane
	}
	close(jobs)
	results := make(chan error, len(lanes))
	var workers sync.WaitGroup
	for range min(8, len(lanes)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for lane := range jobs {
				results <- n.stepLane(ctx, lane)
			}
		}()
	}
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			return err
		}
	}
	return nil
}

func (n *Node) stepLane(ctx context.Context, channels []Channel) error {
	for _, c := range channels {
		if terminal(c.State) {
			continue
		}
		if time.Now().Before(c.NextTry) {
			if c.State == "outgoing" {
				return nil
			}
			continue
		}
		stop := false
		p, ok := n.Config.Peers[c.Peer]
		if c.SourcePeerID != n.Host.ID().String() {
			c.LastError = "local peer identity changed; original channel retained"
			stop = true
		} else if !ok || p.ID != c.PeerID {
			c.LastError = "peer removed or identity changed; original channel retained"
			stop = true
		} else {
			id, _ := peer.Decode(c.PeerID)
			proto := StatusProtocol
			req := request{ID: c.ID}
			if c.State == "outgoing" {
				proto = MessageProtocol
				req.To = c.To
				req.From = c.From
				req.Sender = c.Sender
				req.Text = c.Text
				if c.Attempts == 0 {
					// The text leaves this machine now: a tainted agent sending
					// to another peer is a relay (guard.Watch tainted-relay).
					if agent := strings.TrimSuffix(c.From, "?"); ValidName(agent) {
						n.watch.Observe(guard.Event{Kind: guard.EvOutbound, Peer: c.Peer, Agent: agent, Channel: c.ID})
					}
				}
			}
			res, e := n.call(ctx, id, proto, req)
			c.Attempts++
			if e != nil {
				c.LastError = e.Error()
			} else if res.Error != "" {
				c.LastError = res.Error
			} else if res.ID != c.ID || res.QueueID != queueID(n.Host.ID(), c.ID) {
				c.LastError = "invalid channel acknowledgement"
			} else {
				switch res.State {
				case "accepted", "delivered", "unverified", "failed":
					c.State = res.State
					c.QueueID = res.QueueID
					c.Reason = res.Reason
					c.LastError = ""
				default:
					c.LastError = "invalid remote channel state"
				}
			}
			if c.State == "outgoing" {
				stop = true
			}
		}
		c.Updated = time.Now().UTC()
		delay := 3 * time.Second
		if c.LastError != "" {
			delay = time.Duration(1<<min(c.Attempts, 5)) * time.Second
		}
		c.NextTry = c.Updated.Add(delay)
		if err := atomicJSON(channelPath(n.Root, c.ID), c); err != nil {
			return err
		}
		if stop {
			return nil
		}
	}
	return nil
}

func exposed(p Peer, agent string) bool {
	for _, target := range p.Expose {
		if target == agent {
			return true
		}
	}
	return false
}

// admit applies the rate limit and loop cap to a new inbound message.
func (n *Node) admit(remote peer.ID, alias string, policy Peer, req request) error {
	now := time.Now()
	pauses, err := ReadPauses(n.Root)
	if err != nil {
		return fmt.Errorf("read p2p pauses: %w", err)
	}
	paused := pauses[pairKey(remote.String(), req.To)]
	err = n.limits.admit(remote.String(), policy, req.To, now, paused)
	if err == nil {
		return nil
	}
	if errors.Is(err, errLoop) && !now.Before(paused) {
		// The cap was just reached: pause the pair until the owner looks.
		if perr := Pause(n.Root, remote.String(), req.To, now.Add(LoopPause)); perr != nil {
			return perr
		}
		n.limits.resetPair(remote.String(), req.To)
		_ = audit.Append(n.Root, audit.Event{Kind: "p2p.loop.paused", Severity: audit.Alert, Peer: alias, PeerID: remote.String(), Target: req.To, ID: req.ID,
			Reason: err.Error(), Fields: map[string]string{"until": now.Add(LoopPause).UTC().Format(time.RFC3339), "resume": "bp p2p resume " + alias + " " + req.To}})
		n.limits.firstRejection(req.ID, errLoop.Error())
		return errLoop
	}
	sev := audit.Warn
	if errors.Is(err, errLoop) {
		err = errLoop
	}
	n.reject(remote, alias, req, MessageProtocol, err.Error(), sev)
	return err
}

func (n *Node) reject(remote peer.ID, alias string, req request, p protocol.ID, reason, severity string) {
	if !n.limits.firstRejection(req.ID, reason) {
		return
	}
	kind := "p2p.msg.rejected"
	if p == StatusProtocol {
		kind = "p2p.status.rejected"
	}
	if alias == "" {
		kind = "p2p.peer.denied"
	}
	n.auditRemote(auditSource(remote, alias), audit.Event{Kind: kind, Severity: severity, Peer: alias, PeerID: remote.String(), Actor: req.From, Target: req.To, ID: req.ID, Reason: reason})
}

// auditSource is the audit budget a remote request is charged to: its own
// for a configured peer, one shared budget for every other identity.
func auditSource(remote peer.ID, alias string) string {
	if alias == "" {
		return unconfiguredSource
	}
	return remote.String()
}

// auditRemote writes an event a remote request caused, within the source's
// audit budget, and reports windows that went over it. The audit log stays
// append-only: over-budget events are counted, never written and removed.
func (n *Node) auditRemote(source string, ev audit.Event) {
	ok, reports := n.limits.auditBudget(source, ev.Severity, time.Now())
	n.reportSuppressed(reports)
	if ok {
		_ = audit.Append(n.Root, ev)
	}
}

// FlushAudit reports audit windows that suppressed events and have ended, so
// a flood that stopped still leaves its count. The service loop calls it.
func (n *Node) FlushAudit() { n.reportSuppressed(n.limits.flushAudits(time.Now())) }

func (n *Node) reportSuppressed(reports []suppressedReport) {
	for _, r := range reports {
		ev := audit.Event{Time: r.end, Kind: "p2p.audit.suppressed", Severity: audit.Warn,
			Reason: fmt.Sprintf("%d rejection events over the audit budget (%d per minute) were not written", r.count, AuditBudgetPerMinute),
			Fields: map[string]string{"count": fmt.Sprint(r.count), "since": r.start.UTC().Format(time.RFC3339), "worst": r.worst}}
		if r.worst == audit.Alert {
			ev.Severity = audit.Alert
		}
		if r.source == unconfiguredSource {
			ev.Peer = "(unconfigured peers)"
		} else {
			ev.PeerID = r.source
			for alias, p := range n.Config.Peers {
				if p.ID == r.source {
					ev.Peer = alias
				}
			}
		}
		_ = audit.Append(n.Root, ev)
	}
}

// auditAccepted records a new inbound message once, with the guard flags
// its body raised. Flags never block delivery; a high flag raises the
// event to warn so the owner can find it.
func (n *Node) auditAccepted(remote peer.ID, alias string, req request, queueID string, findings []guard.Finding) {
	fields := map[string]string{"queue": queueID, "bytes": fmt.Sprint(len(req.Text))}
	severity := audit.Info
	if len(findings) > 0 {
		fields["guard.flags"] = guard.Summary(findings)
		fields["guard.severity"] = string(guard.MaxSeverity(findings))
		if guard.MaxSeverity(findings) == guard.High {
			severity = audit.Warn
		}
	}
	_ = audit.Append(n.Root, audit.Event{Kind: "p2p.msg.accepted", Severity: severity, Peer: alias, PeerID: remote.String(), Actor: req.From, Target: req.To, ID: req.ID,
		Fields: fields})
	if severity == audit.Warn {
		// High flags are the owner-facing signal; warn-level flags (an IP URL,
		// a slash at line start) stay as fields so ordinary text never pages.
		_ = audit.Append(n.Root, audit.Event{Kind: "guard.finding", Severity: audit.Alert, Peer: alias, PeerID: remote.String(), Actor: req.From, Target: req.To, ID: req.ID,
			Reason: "inbound text raised high guard flags: " + guard.Summary(highOnly(findings)), Fields: map[string]string{"queue": queueID, "rules": rules(findings)}})
	}
}

func highOnly(fs []guard.Finding) []guard.Finding {
	var out []guard.Finding
	for _, f := range fs {
		if f.Severity == guard.High {
			out = append(out, f)
		}
	}
	return out
}

func rules(fs []guard.Finding) string {
	seen := map[string]bool{}
	var out []string
	for _, f := range fs {
		if !seen[f.Rule] {
			seen[f.Rule] = true
			out = append(out, f.Rule)
		}
	}
	return strings.Join(out, ",")
}
