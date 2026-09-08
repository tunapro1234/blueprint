package p2p

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"blueprint/internal/messagetext"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

const MaxMessageBytes = 16 * 1024

type Channel struct {
	ID           string      `json:"id"`
	Peer         string      `json:"peer"`
	PeerID       string      `json:"peer_id"`
	SourcePeerID string      `json:"source_peer_id"`
	Sender       SenderClaim `json:"sender"`
	To           string      `json:"to"`
	From         string      `json:"from"`
	Text         string      `json:"text"`
	Created      time.Time   `json:"created"`
	State        string      `json:"state"` // outgoing, accepted, delivered, unverified, failed
	QueueID      string      `json:"queue_id,omitempty"`
	Reason       string      `json:"reason,omitempty"`
	LastError    string      `json:"last_error,omitempty"`
	Updated      time.Time   `json:"updated,omitempty"`
	NextTry      time.Time   `json:"next_try,omitempty"`
	Attempts     int         `json:"attempts,omitempty"`
}

// SenderClaim records the originating BP's local observation. The receiver
// treats it as the authenticated peer's assertion, never local authority.
type SenderClaim struct {
	Thread  string `json:"thread,omitempty"`
	Source  string `json:"source,omitempty"`
	Certain bool   `json:"certain"`
}

func validID(id string) bool {
	if len(id) != 33 || id[0] != 'p' {
		return false
	}
	_, err := hex.DecodeString(id[1:])
	return err == nil
}
func statePath(root string, parts ...string) string {
	return filepath.Join(append([]string{root, "p2p"}, parts...)...)
}
func channelPath(root, id string) string { return statePath(root, "outbox", id+".json") }

func atomicJSON(path string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return atomicFile(path, append(b, '\n'), false)
}
func atomicFile(path string, b []byte, exclusive bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	if exclusive {
		err = os.Link(f.Name(), path)
	} else {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func Identity(root string) (crypto.PrivKey, error) {
	path := statePath(root, "identity.key")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key, _, e := crypto.GenerateEd25519Key(rand.Reader)
		if e != nil {
			return nil, e
		}
		raw, e := crypto.MarshalPrivateKey(key)
		if e != nil {
			return nil, e
		}
		if e = atomicFile(path, raw, true); e != nil && !errors.Is(e, os.ErrExist) {
			return nil, e
		}
		b, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, err
	}
	return crypto.UnmarshalPrivateKey(b)
}

// Lock prevents two local services from owning the same peer identity/outbox.
func Lock(root string) (*os.File, error) {
	path := statePath(root, "service.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("p2p service already running: %w", err)
	}
	return f, nil
}

func Enqueue(root string, cfg Config, alias, to, from, text string) (Channel, error) {
	return EnqueueSender(root, cfg, alias, to, from, text, SenderClaim{})
}

func EnqueueSender(root string, cfg Config, alias, to, from, text string, sender SenderClaim) (Channel, error) {
	p, ok := cfg.Peers[alias]
	if !ok {
		return Channel{}, fmt.Errorf("unknown p2p peer: %s", alias)
	}
	if !ValidName(to) {
		return Channel{}, fmt.Errorf("invalid target agent")
	}
	if err := messagetext.Label(from); err != nil {
		return Channel{}, err
	}
	if len(from) > 256 || len(text) > MaxMessageBytes || strings.TrimSpace(text) == "" {
		return Channel{}, fmt.Errorf("message must be nonempty and at most %d bytes", MaxMessageBytes)
	}
	if err := messagetext.Validate(text); err != nil {
		return Channel{}, err
	}
	key, err := Identity(root)
	if err != nil {
		return Channel{}, err
	}
	sourceID, err := peer.IDFromPrivateKey(key)
	if err != nil {
		return Channel{}, err
	}
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return Channel{}, err
	}
	c := Channel{ID: "p" + hex.EncodeToString(buf[:]), Peer: alias, PeerID: p.ID, SourcePeerID: sourceID.String(), Sender: sender, To: to, From: from, Text: text, Created: time.Now().UTC(), State: "outgoing"}
	b, err := json.Marshal(c)
	if err != nil {
		return Channel{}, err
	}
	err = atomicFile(channelPath(root, c.ID), b, true)
	return c, err
}
func ReadChannel(root, id string) (Channel, error) {
	if !validID(id) {
		return Channel{}, fmt.Errorf("invalid p2p channel ID")
	}
	var c Channel
	err := readJSON(channelPath(root, id), &c)
	if err == nil && (c.ID != id || (c.State != "outgoing" && c.State != "accepted" && !terminal(c.State))) {
		err = fmt.Errorf("invalid persisted p2p channel %s", id)
	}
	return c, err
}
func Channels(root string) ([]Channel, error) {
	files, err := os.ReadDir(statePath(root, "outbox"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var all []Channel
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		c, e := ReadChannel(root, strings.TrimSuffix(f.Name(), ".json"))
		if e != nil {
			return nil, e
		}
		all = append(all, c)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Created.Equal(all[j].Created) {
			return all[i].ID < all[j].ID
		}
		return all[i].Created.Before(all[j].Created)
	})
	return all, nil
}
func terminal(s string) bool { return s == "delivered" || s == "unverified" || s == "failed" }
