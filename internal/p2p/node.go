package p2p

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

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
	Host        host.Host
	Root        string
	Config      Config
	Queue       *msgq.Queue
	Log         io.Writer
	lock        *os.File
	mdns        mdns.Service
	relay       io.Closer
	mu          sync.Mutex
	discovery   map[peer.ID]presence
	stepMu      sync.Mutex
	discoveryMu sync.Mutex
	ctx         context.Context
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
	n := &Node{Host: h, Root: root, Config: cfg, Queue: q, Log: os.Stderr, lock: lock, discovery: map[peer.ID]presence{}, ctx: ctx}
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
	for _, p := range []protocol.ID{MessageProtocol, StatusProtocol, DiscoveryProtocol, PingProtocol} {
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
				res.Error = "target is not exposed to this peer"
				break
			}
			if !ValidName(req.To) || len(req.From) > 256 || len(req.Text) > MaxMessageBytes || strings.TrimSpace(req.Text) == "" {
				res.Error = "invalid message"
				break
			}
			if err = messagetext.Label(req.From); err != nil {
				res.Error = err.Error()
				break
			}
			if err = messagetext.Validate(req.Text); err != nil {
				res.Error = err.Error()
				break
			}
			if len(req.Sender.Thread) > 128 || len(req.Sender.Source) > 128 || messagetext.Label(req.Sender.Thread) != nil || messagetext.Label(req.Sender.Source) != nil {
				res.Error = "invalid sender evidence"
				break
			}
			from := "external:" + req.From + "@" + alias
			origin := &msgq.Origin{Transport: "libp2p", PeerID: remote.String(), PeerAlias: alias, ChannelID: req.ID,
				PeerAuthenticated: true, AgentClaim: req.From, AgentVerified: false,
				ReportedThread: req.Sender.Thread, ReportedSource: req.Sender.Source, ReportedCertain: req.Sender.Certain}
			m, err = n.Queue.EnqueueOnceOrigin(queueKey(remote, req.ID), req.To, from, "["+from+"] "+req.Text, origin)
		} else {
			m, err = n.Queue.Record(queueID(remote, req.ID))
		}
		if err != nil {
			res.Error = err.Error()
			break
		}
		res.QueueID = m.ID
		res.State = "accepted"
		res.Reason = m.Reason
		switch {
		case m.Cleanup:
			res.State = "delivered"
		case m.Status == "delivered (unverified)":
			res.State = "unverified"
		case strings.HasPrefix(m.Status, "delivered"):
			res.State = "delivered"
		case m.Status != "":
			res.State = "failed"
			res.Reason = m.Status
		}
	}
	if err := writeFrame(s, res); err != nil {
		_ = s.Reset()
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
