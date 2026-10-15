package p2p

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"blueprint/internal/audit"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// NewHopID mints a transfer/idempotency channel id in the same shape as a
// message channel ("p" + 32 hex), so validID accepts it on both sides.
func NewHopID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "p" + hex.EncodeToString(buf[:]), nil
}

// HopProtocol carries one agent bundle (internal/hop) from a source machine to
// a receiver. Unlike the message protocols it streams an arbitrary-sized body,
// so it has its own stream handler rather than the framed request/response path.
const HopProtocol protocol.ID = "/bp/hop/1.0.0"

const (
	// hopTimeout bounds one transfer end to end. Bundles can be large and travel
	// over a relayed circuit, so it is far longer than rpcTimeout.
	hopTimeout = 30 * time.Minute
	// HopMaxBytesDefault caps one incoming bundle when the config does not set a
	// limit. It bounds the disk a single authenticated peer can stage at once.
	HopMaxBytesDefault int64 = 2 << 30 // 2 GiB
)

// HopHeader is the small, framed preface a sender writes before the bundle body.
// It lets the receiver authorize and size-check the transfer before accepting
// any payload bytes, and carries the advisory provenance shown to the owner.
// It is the peer's assertion, never local authority: the bundle itself is
// re-validated by internal/hop at accept time.
type HopHeader struct {
	Channel      string `json:"channel"` // unique per transfer; the idempotency key
	Agent        string `json:"agent"`
	Harness      string `json:"harness"`
	Thread       string `json:"thread,omitempty"`
	Session      string `json:"session,omitempty"`
	FolderBase   string `json:"folderBase,omitempty"`
	IncludeFiles bool   `json:"includeFiles"`
	TotalBytes   int64  `json:"totalBytes"` // the sender's declared bundle size
	FileCount    int    `json:"fileCount"`
	Format       int    `json:"format"`
	SourceHost   string `json:"sourceHost,omitempty"`
	SourceAgent  string `json:"sourceAgent,omitempty"`
}

// HopResult is the framed reply the receiver writes after the body. Accepted
// means the bundle was staged for owner review, not that it was materialized.
type HopResult struct {
	Accepted bool   `json:"accepted"`
	ID       string `json:"id,omitempty"`
	Error    string `json:"error,omitempty"`
}

// HopMeta is the staging record written beside a quarantined bundle. The owner
// reviews pending records (`bp hop list`) and explicitly accepts or rejects
// each one; nothing is materialized or launched without that step.
type HopMeta struct {
	ID         string    `json:"id"`
	Header     HopHeader `json:"header"`
	PeerAlias  string    `json:"peerAlias"`
	PeerID     string    `json:"peerId"`
	ReceivedAt string    `json:"receivedAt"`
	State      string    `json:"state"`  // pending
	Bundle     string    `json:"bundle"` // bundle filename within the staging dir
}

const hopMetaName = "meta.json"
const hopBundleName = "bundle.tar.gz"

func (n *Node) hopCap() int64 {
	if n.Config.HopMaxBytes > 0 {
		return n.Config.HopMaxBytes
	}
	return HopMaxBytesDefault
}

func (n *Node) peerIDByAlias(alias string) (peer.ID, bool) {
	p, ok := n.Config.Peers[alias]
	if !ok {
		return "", false
	}
	id, err := peer.Decode(p.ID)
	if err != nil {
		return "", false
	}
	return id, true
}

// SendHop streams a bundle body to a peer over HopProtocol: it writes the
// framed header, copies the body, half-closes the write side so the receiver
// sees EOF, and reads the framed result. The caller provides an already-built
// header (sizes computed up front) and the bundle as a reader.
func (n *Node) SendHop(ctx context.Context, alias string, header HopHeader, body io.Reader) (HopResult, error) {
	id, ok := n.peerIDByAlias(alias)
	if !ok {
		return HopResult{}, fmt.Errorf("unknown p2p peer: %s", alias)
	}
	if header.TotalBytes > n.hopCap() {
		return HopResult{}, fmt.Errorf("bundle is %d bytes, over the %d byte hop limit", header.TotalBytes, n.hopCap())
	}
	ctx, cancel := context.WithTimeout(ctx, hopTimeout)
	defer cancel()
	ctx = network.WithAllowLimitedConn(ctx, "bp hop transfer")
	s, err := n.Host.NewStream(ctx, id, HopProtocol)
	if err != nil {
		return HopResult{}, err
	}
	defer s.Close()
	stop := context.AfterFunc(ctx, func() { _ = s.Reset() })
	defer stop()
	deadline, _ := ctx.Deadline()
	_ = s.SetDeadline(deadline)

	if err := writeFrame(s, header); err != nil {
		_ = s.Reset()
		return HopResult{}, err
	}
	if _, err := io.Copy(s, body); err != nil {
		_ = s.Reset()
		return HopResult{}, err
	}
	// Half-close so the receiver's body copy sees EOF while the read side stays
	// open for the result frame.
	if err := s.CloseWrite(); err != nil {
		_ = s.Reset()
		return HopResult{}, err
	}
	var res HopResult
	if err := readFrame(s, &res); err != nil {
		_ = s.Reset()
		return HopResult{}, err
	}
	return res, nil
}

// handleHop receives a bundle: it authenticates the peer, requires the peer be
// allowed to hop, size-checks the declared and actual bytes, and stages the
// bundle in quarantine with a pending record. It never materializes files or
// launches an agent — the owner does that explicitly after review.
func (n *Node) handleHop(s network.Stream) {
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(hopTimeout))
	remote := s.Conn().RemotePeer()

	alias, policy, ok := n.peerPolicy(remote)
	if !ok {
		n.auditRemote(auditSource(remote, ""), audit.Event{Kind: "p2p.peer.denied", Severity: audit.Warn, PeerID: remote.String(), Reason: "hop from unconfigured peer"})
		_ = writeFrame(s, HopResult{Error: "peer is not allowed"})
		return
	}
	if !policy.AllowHop {
		// Asking to move an agent onto a machine that did not opt this peer in is
		// a clear reach for more than it was granted.
		n.auditRemote(auditSource(remote, alias), audit.Event{Kind: "p2p.hop.denied", Severity: audit.Alert, Peer: alias, PeerID: remote.String(), Reason: "hop is not allowed from this peer"})
		_ = writeFrame(s, HopResult{Error: "hop is not allowed from this peer"})
		return
	}

	var header HopHeader
	if err := readFrame(s, &header); err != nil {
		_ = s.Reset()
		return
	}
	if err := validHopHeader(header, n.hopCap()); err != nil {
		n.auditRemote(auditSource(remote, alias), audit.Event{Kind: "p2p.hop.rejected", Severity: audit.Warn, Peer: alias, PeerID: remote.String(), Actor: header.SourceAgent, Target: header.Agent, ID: header.Channel, Reason: err.Error()})
		_ = writeFrame(s, HopResult{Error: err.Error()})
		return
	}

	id := header.Channel
	final := HopStagingDir(n.Root, id)
	// Idempotent redelivery: the same channel from the same peer returns the
	// existing record instead of staging a second copy.
	if existing, err := ReadHopMeta(n.Root, id); err == nil {
		if existing.PeerID != remote.String() {
			n.auditRemote(auditSource(remote, alias), audit.Event{Kind: "p2p.hop.rejected", Severity: audit.Alert, Peer: alias, PeerID: remote.String(), ID: id, Reason: "hop channel already staged for a different peer"})
			_ = writeFrame(s, HopResult{Error: "channel already in use"})
			return
		}
		_ = writeFrame(s, HopResult{Accepted: true, ID: id})
		return
	}

	cap := n.hopCap()
	tmp := final + ".tmp"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		_ = writeFrame(s, HopResult{Error: "receiver could not stage bundle"})
		return
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(tmp)
		}
	}()

	bundlePath := filepath.Join(tmp, hopBundleName)
	f, err := os.OpenFile(bundlePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		_ = writeFrame(s, HopResult{Error: "receiver could not stage bundle"})
		return
	}
	// Read at most cap+1 so an oversize body is caught without trusting the
	// declared size, then fail closed.
	written, copyErr := io.Copy(f, io.LimitReader(s, cap+1))
	syncErr := f.Sync()
	closeErr := f.Close()
	if copyErr != nil {
		n.auditRemote(auditSource(remote, alias), audit.Event{Kind: "p2p.hop.rejected", Severity: audit.Warn, Peer: alias, PeerID: remote.String(), Target: header.Agent, ID: id, Reason: "transfer interrupted"})
		_ = writeFrame(s, HopResult{Error: "transfer failed"})
		return
	}
	if syncErr != nil || closeErr != nil {
		_ = writeFrame(s, HopResult{Error: "receiver could not write bundle"})
		return
	}
	if written > cap {
		n.auditRemote(auditSource(remote, alias), audit.Event{Kind: "p2p.hop.rejected", Severity: audit.Alert, Peer: alias, PeerID: remote.String(), Target: header.Agent, ID: id, Reason: fmt.Sprintf("bundle exceeds %d byte hop limit", cap)})
		_ = writeFrame(s, HopResult{Error: "bundle over size limit"})
		return
	}

	meta := HopMeta{
		ID:         id,
		Header:     header,
		PeerAlias:  alias,
		PeerID:     remote.String(),
		ReceivedAt: time.Now().UTC().Format(time.RFC3339),
		State:      "pending",
		Bundle:     hopBundleName,
	}
	if err := atomicJSON(filepath.Join(tmp, hopMetaName), meta); err != nil {
		_ = writeFrame(s, HopResult{Error: "receiver could not record bundle"})
		return
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = writeFrame(s, HopResult{Error: "receiver could not publish bundle"})
		return
	}
	cleanup = false

	_ = audit.Append(n.Root, audit.Event{Kind: "p2p.hop.received", Severity: audit.Info, Peer: alias, PeerID: remote.String(), Actor: header.SourceAgent, Target: header.Agent, ID: id,
		Reason: "agent bundle staged for owner review",
		Fields: map[string]string{"bytes": fmt.Sprint(written), "files": fmt.Sprint(header.FileCount), "withFiles": fmt.Sprint(header.IncludeFiles), "accept": "bp hop accept " + id}})
	_ = writeFrame(s, HopResult{Accepted: true, ID: id})
}

func validHopHeader(h HopHeader, cap int64) error {
	if !validID(h.Channel) {
		return fmt.Errorf("invalid hop channel")
	}
	if !ValidName(h.Agent) {
		return fmt.Errorf("invalid agent name")
	}
	if h.Harness == "" || len(h.Harness) > 32 || !ValidName(h.Harness) {
		return fmt.Errorf("invalid harness")
	}
	if h.Format <= 0 {
		return fmt.Errorf("invalid bundle format")
	}
	if h.TotalBytes < 0 || h.TotalBytes > cap {
		return fmt.Errorf("declared bundle size out of range")
	}
	if h.FileCount < 0 {
		return fmt.Errorf("invalid file count")
	}
	return nil
}

// HopIncomingDir is the quarantine directory for staged incoming bundles.
func HopIncomingDir(root string) string { return statePath(root, "hop", "incoming") }

// HopStagingDir is the staging directory for one transfer.
func HopStagingDir(root, id string) string { return filepath.Join(HopIncomingDir(root), id) }

// HopBundlePath is the absolute path of a staged bundle's payload.
func HopBundlePath(root string, m HopMeta) string {
	name := m.Bundle
	if name == "" {
		name = hopBundleName
	}
	return filepath.Join(HopStagingDir(root, m.ID), name)
}

// ReadHopMeta loads one staged record. It returns an error when the id is
// malformed or the record is absent, so callers fail closed.
func ReadHopMeta(root, id string) (HopMeta, error) {
	if !validID(id) {
		return HopMeta{}, fmt.Errorf("invalid hop id")
	}
	var m HopMeta
	if err := readJSON(filepath.Join(HopStagingDir(root, id), hopMetaName), &m); err != nil {
		return HopMeta{}, err
	}
	if m.ID != id {
		return HopMeta{}, fmt.Errorf("staged record id mismatch")
	}
	return m, nil
}

// ListHopPending returns the staged records awaiting owner review, oldest first.
func ListHopPending(root string) ([]HopMeta, error) {
	entries, err := os.ReadDir(HopIncomingDir(root))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var all []HopMeta
	for _, e := range entries {
		if !e.IsDir() || strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		m, err := ReadHopMeta(root, e.Name())
		if err != nil {
			continue
		}
		all = append(all, m)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].ReceivedAt == all[j].ReceivedAt {
			return all[i].ID < all[j].ID
		}
		return all[i].ReceivedAt < all[j].ReceivedAt
	})
	return all, nil
}

// RemoveHopStaging deletes a staged transfer's directory. It is used after a
// bundle is accepted (materialized elsewhere) or rejected by the owner.
func RemoveHopStaging(root, id string) error {
	if !validID(id) {
		return fmt.Errorf("invalid hop id")
	}
	return os.RemoveAll(HopStagingDir(root, id))
}
