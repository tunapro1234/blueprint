package p2p

import (
	"context"
	"sort"
	"time"

	"blueprint/internal/audit"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// handleTokens answers a fleet token query. The peer must be configured and
// have AllowTokens set; the response carries per-agent/model token counts only
// (no prompt text, no machine metadata). The asking side labels the machine, so
// this side never reports its own alias.
func (n *Node) handleTokens(s network.Stream) {
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(rpcTimeout))
	remote := s.Conn().RemotePeer()
	alias, policy, ok := n.peerPolicy(remote)
	if !ok || !policy.AllowTokens {
		if ok {
			// A configured peer reaching for usage it was not granted is worth a
			// line; an unconfigured identity is charged to the shared budget.
			n.auditRemote(auditSource(remote, alias), audit.Event{Kind: "p2p.tokens.denied", Severity: audit.Warn, Peer: alias, PeerID: remote.String(), Reason: "tokens read not allowed from this peer"})
		}
		_ = s.Reset()
		return
	}
	var req TokensRequest
	if err := readFrame(s, &req); err != nil {
		_ = s.Reset()
		return
	}
	if req.SinceDays <= 0 || req.SinceDays > MaxTokensSinceDays {
		_ = s.Reset()
		return
	}
	report := TokensReport{SinceDays: req.SinceDays}
	if n.ResolveTokens != nil {
		report = n.ResolveTokens(req.SinceDays)
	}
	report.Machine = ""
	if err := writeFrame(s, report); err != nil {
		_ = s.Reset()
	}
}

func (n *Node) callTokens(ctx context.Context, id peer.ID, since int) (TokensReport, error) {
	ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	ctx = network.WithAllowLimitedConn(ctx, "bp fleet tokens")
	s, err := n.Host.NewStream(ctx, id, TokensProtocol)
	if err != nil {
		return TokensReport{}, err
	}
	defer s.Close()
	stop := context.AfterFunc(ctx, func() { _ = s.Reset() })
	defer stop()
	deadline, _ := ctx.Deadline()
	_ = s.SetDeadline(deadline)
	if err := writeFrame(s, TokensRequest{SinceDays: since}); err != nil {
		_ = s.Reset()
		return TokensReport{}, err
	}
	var rep TokensReport
	if err := readFrame(s, &rep); err != nil {
		_ = s.Reset()
		return TokensReport{}, err
	}
	return rep, nil
}

// fleetTokens builds the aggregated report: this machine's own usage first
// (labelled "local"), then each connected, configured peer's, queried in
// parallel. A peer that is unreachable or refuses is included with its Error
// set, so the caller sees which machines are missing rather than a silent gap.
func (n *Node) fleetTokens(ctx context.Context, since int) FleetTokensReport {
	report := FleetTokensReport{SinceDays: since, Machines: []TokensReport{}}
	if n.ResolveTokens != nil {
		local := n.ResolveTokens(since)
		local.Machine = "local"
		local.SinceDays = since
		report.Machines = append(report.Machines, local)
	}
	type connected struct {
		alias string
		id    peer.ID
	}
	var peers []connected
	for alias, cfg := range n.Config.Peers {
		id, err := peer.Decode(cfg.ID)
		if err == nil && len(n.Host.Network().ConnsToPeer(id)) > 0 {
			peers = append(peers, connected{alias, id})
		}
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].alias < peers[j].alias })
	results := make([]TokensReport, len(peers))
	done := make(chan int, len(peers))
	for index, p := range peers {
		go func(index int, p connected) {
			rep, err := n.callTokens(ctx, p.id, since)
			rep.Machine = p.alias
			rep.SinceDays = since
			if err != nil {
				rep.Rows = nil
				rep.Error = "unreachable or refused"
			}
			results[index] = rep
			done <- index
		}(index, p)
	}
	for range peers {
		<-done
	}
	report.Machines = append(report.Machines, results...)
	return report
}
