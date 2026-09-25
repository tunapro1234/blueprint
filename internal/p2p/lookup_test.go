package p2p

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestLookupProtocolReturnsOnlyAgentSummary(t *testing.T) {
	a, b := pair(t)
	b.ResolveLookup = func(query string) LookupResponse {
		switch query {
		case "live":
			return LookupResponse{Found: true, Name: "worker", State: "live"}
		case "closed":
			return LookupResponse{Found: true, Name: "worker", State: "closed"}
		case "archived":
			return LookupResponse{Found: true, Name: "worker", State: "archived"}
		case "title":
			return LookupResponse{Found: true, Name: "canonical-worker", State: "closed"}
		default:
			return LookupResponse{}
		}
	}

	for _, test := range []struct {
		query string
		found bool
		name  string
		state string
	}{
		{query: "live", found: true, name: "worker", state: "live"},
		{query: "closed", found: true, name: "worker", state: "closed"},
		{query: "archived", found: true, name: "worker", state: "archived"},
		{query: "title", found: true, name: "canonical-worker", state: "closed"},
		{query: "missing"},
		{query: "ambiguous"},
	} {
		t.Run(test.query, func(t *testing.T) {
			got, err := a.callLookup(context.Background(), b.Host.ID(), test.query)
			if err != nil {
				t.Fatal(err)
			}
			if got.Found != test.found || got.Name != test.name || got.State != test.state {
				t.Fatalf("lookup = %+v, want found=%v name=%q state=%q", got, test.found, test.name, test.state)
			}
		})
	}

	wire, err := json.Marshal(LookupResponse{Found: true, Name: "worker", State: "live"})
	if err != nil {
		t.Fatal(err)
	}
	if string(wire) != `{"found":true,"name":"worker","state":"live"}` {
		t.Fatalf("wire response leaked or changed fields: %s", wire)
	}
	wire, err = json.Marshal(LookupResponse{})
	if err != nil || string(wire) != `{"found":false}` {
		t.Fatalf("not-found response = %s, %v", wire, err)
	}
}

func TestLookupProtocolRefusesUnknownPeersAndInvalidQueries(t *testing.T) {
	a, b := pair(t)
	delete(b.Config.Peers, "a")
	if _, err := a.callLookup(context.Background(), b.Host.ID(), "worker"); err == nil {
		t.Fatal("unauthorized peer received a lookup response")
	}
	b.Config.Peers["a"] = Peer{ID: a.Host.ID().String()}
	for _, query := range []string{"", "  ", "bad\nquery", "bad\x00query", strings.Repeat("x", MaxLookupQueryBytes+1)} {
		if _, err := a.callLookup(context.Background(), b.Host.ID(), query); err == nil {
			t.Errorf("invalid query %q was accepted", query)
		}
	}
}

func TestLookupPeersMarkUnsupportedOldProtocolAsError(t *testing.T) {
	a, b := pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := a.Host.Connect(ctx, peer.AddrInfo{ID: b.Host.ID(), Addrs: b.Host.Addrs()}); err != nil {
		t.Fatal(err)
	}
	b.Host.RemoveStreamHandler(LookupProtocol)
	report := a.lookupPeers(ctx, "worker", time.Second)
	if len(report.Peers) != 1 || report.Peers[0].Peer != "b" || report.Peers[0].Found || report.Peers[0].Error != "error" {
		t.Fatalf("unsupported protocol report = %+v", report)
	}
}

func TestValidLookupQuery(t *testing.T) {
	if !ValidLookupQuery("native title with spaces") {
		t.Fatal("valid title with spaces was rejected")
	}
	for _, query := range []string{"", "  ", "line\nbreak", "nul\x00byte", strings.Repeat("x", MaxLookupQueryBytes+1)} {
		if ValidLookupQuery(query) {
			t.Errorf("invalid lookup query accepted: %q", query)
		}
	}
}
