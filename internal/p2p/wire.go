package p2p

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

const (
	MessageProtocol   protocol.ID = "/bp/msg/1.0.0"
	StatusProtocol    protocol.ID = "/bp/qstat/1.0.0"
	DiscoveryProtocol protocol.ID = "/bp/discovery/1.0.0"
	PingProtocol      protocol.ID = "/bp/ping/1.0.0"
	LookupProtocol    protocol.ID = "/bp/lookup/1.0.0"
	maxFrame                      = 128 * 1024
	rpcTimeout                    = 12 * time.Second
)

const MaxLookupQueryBytes = 256

type LookupRequest struct {
	Find string `json:"find"`
}

// LookupResponse deliberately contains no machine or conversation metadata.
type LookupResponse struct {
	Found bool   `json:"found"`
	Name  string `json:"name,omitempty"`
	State string `json:"state,omitempty"`
}

type LookupPeerResult struct {
	Peer  string `json:"peer"`
	Found bool   `json:"found"`
	Name  string `json:"name,omitempty"`
	State string `json:"state,omitempty"`
	Error string `json:"error,omitempty"`
}

type LookupReport struct {
	Query string             `json:"query"`
	Peers []LookupPeerResult `json:"peers"`
}

func ValidLookupQuery(query string) bool {
	if strings.TrimSpace(query) == "" || len(query) > MaxLookupQueryBytes {
		return false
	}
	for _, r := range query {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

type request struct {
	ID        string      `json:"id,omitempty"`
	To        string      `json:"to,omitempty"`
	From      string      `json:"from,omitempty"`
	Sender    SenderClaim `json:"sender"`
	Text      string      `json:"text,omitempty"`
	Addresses []string    `json:"addresses,omitempty"`
	Find      string      `json:"find,omitempty"`
}
type response struct {
	ID        string   `json:"id,omitempty"`
	QueueID   string   `json:"queue_id,omitempty"`
	State     string   `json:"state,omitempty"`
	Reason    string   `json:"reason,omitempty"`
	Error     string   `json:"error,omitempty"`
	Addresses []string `json:"addresses,omitempty"`
}

func readFrame(r io.Reader, v any) error {
	var size uint32
	if err := binary.Read(r, binary.BigEndian, &size); err != nil {
		return err
	}
	if size == 0 || size > maxFrame {
		return fmt.Errorf("invalid frame length")
	}
	b := make([]byte, size)
	if _, err := io.ReadFull(r, b); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("trailing frame content")
	}
	return nil
}
func writeFrame(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > maxFrame {
		return fmt.Errorf("frame too large")
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(b)))
	_, err = io.Copy(w, bytes.NewReader(append(prefix[:], b...)))
	return err
}
func (n *Node) call(ctx context.Context, id peer.ID, p protocol.ID, req request) (response, error) {
	ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	// Relay connections are limited by design; each bounded RPC fits in a circuit.
	ctx = network.WithAllowLimitedConn(ctx, "bp channel RPC")
	s, err := n.Host.NewStream(ctx, id, p)
	if err != nil {
		return response{}, err
	}
	defer s.Close()
	stop := context.AfterFunc(ctx, func() { _ = s.Reset() })
	defer stop()
	deadline, _ := ctx.Deadline()
	_ = s.SetDeadline(deadline)
	if err := writeFrame(s, req); err != nil {
		_ = s.Reset()
		return response{}, err
	}
	var result response
	if err := readFrame(s, &result); err != nil {
		_ = s.Reset()
		return response{}, err
	}
	return result, nil
}

func (n *Node) callLookup(ctx context.Context, id peer.ID, query string) (LookupResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	ctx = network.WithAllowLimitedConn(ctx, "bp agent lookup")
	s, err := n.Host.NewStream(ctx, id, LookupProtocol)
	if err != nil {
		return LookupResponse{}, err
	}
	defer s.Close()
	stop := context.AfterFunc(ctx, func() { _ = s.Reset() })
	defer stop()
	deadline, _ := ctx.Deadline()
	_ = s.SetDeadline(deadline)
	if err := writeFrame(s, LookupRequest{Find: query}); err != nil {
		_ = s.Reset()
		return LookupResponse{}, err
	}
	var result LookupResponse
	if err := readFrame(s, &result); err != nil {
		_ = s.Reset()
		return LookupResponse{}, err
	}
	return result, nil
}
