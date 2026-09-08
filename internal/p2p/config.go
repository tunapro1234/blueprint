// Package p2p provides authenticated libp2p transport and durable BP channels.
package p2p

import (
	"fmt"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

type Config struct {
	Enabled    bool            `json:"enabled" yaml:"enabled"`
	Listen     []string        `json:"listen,omitempty" yaml:"listen,omitempty"`
	Advertise  []string        `json:"advertise,omitempty" yaml:"advertise,omitempty"`
	Rendezvous []string        `json:"rendezvous,omitempty" yaml:"rendezvous,omitempty"`
	Relay      bool            `json:"relay,omitempty" yaml:"relay,omitempty"`
	MDNS       bool            `json:"mdns,omitempty" yaml:"mdns,omitempty"`
	Peers      map[string]Peer `json:"peers,omitempty" yaml:"peers,omitempty"`
}

type Peer struct {
	ID        string   `json:"id" yaml:"id"`
	Addresses []string `json:"addresses,omitempty" yaml:"addresses,omitempty"`
	// Empty exposes nothing. Discovery and connectivity never grant agent access.
	Expose []string `json:"expose,omitempty" yaml:"expose,omitempty"`
}

func ValidName(s string) bool {
	if s == "" || s == "." || s == ".." || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-", c) {
			return false
		}
	}
	return true
}

func (c Config) Validate() error {
	ids := map[peer.ID]bool{}
	for name, p := range c.Peers {
		if !ValidName(name) {
			return fmt.Errorf("invalid p2p peer alias %q", name)
		}
		id, err := peer.Decode(p.ID)
		if err != nil {
			return fmt.Errorf("peer %s: invalid Peer ID", name)
		}
		if ids[id] {
			return fmt.Errorf("duplicate p2p peer identity: %s", name)
		}
		ids[id] = true
		for _, a := range p.Expose {
			if !ValidName(a) {
				return fmt.Errorf("peer %s: invalid exposed agent", name)
			}
		}
		for _, a := range p.Addresses {
			if _, err := ma.NewMultiaddr(a); err != nil {
				return fmt.Errorf("peer %s: invalid multiaddress: %w", name, err)
			}
		}
	}
	for _, a := range append(append([]string{}, c.Listen...), c.Advertise...) {
		if _, err := ma.NewMultiaddr(a); err != nil {
			return fmt.Errorf("invalid p2p address: %w", err)
		}
	}
	for _, a := range c.Rendezvous {
		if _, err := peer.AddrInfoFromString(a); err != nil {
			return fmt.Errorf("rendezvous needs a full multiaddress including /p2p/PeerID: %w", err)
		}
	}
	return nil
}
