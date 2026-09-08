package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bpconfig "blueprint/internal/config"
	"blueprint/internal/identity"
	"blueprint/internal/msgq"
	"blueprint/internal/p2p"
)

func TestP2PCLIChannelContract(t *testing.T) {
	root := t.TempDir()
	cfg := p2p.Config{Enabled: true, Listen: []string{"/ip4/127.0.0.1/tcp/0"}}
	q := msgq.New(filepath.Join(root, "q"))
	n, e := p2p.New(context.Background(), root, cfg, q)
	if e != nil {
		t.Fatal(e)
	}
	defer n.Close()
	n.Log = io.Discard
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- n.Serve(ctx, nil) }()
	defer func() { cancel(); <-done }()
	// The target is deliberately offline; enqueue still has a durable channel.
	cfg.Peers = map[string]p2p.Peer{"laptop": {ID: n.Host.ID().String()}}
	var info p2p.Info
	deadline := time.Now().Add(3 * time.Second)
	for p2p.Control(ctx, root, http.MethodGet, "/status", &info) != nil {
		if time.Now().After(deadline) {
			t.Fatal("worker unavailable")
		}
		time.Sleep(10 * time.Millisecond)
	}
	output := testOutput(t)
	a := &app{ctx: ctx, config: bpconfig.Config{StateDir: root, P2P: &cfg}, queue: q, out: output, err: testOutput(t), originProbe: func(context.Context) identity.Origin { return identity.Origin{} }}
	if e := a.message([]string{"agent@laptop", "hello"}); e != nil {
		t.Fatal(e)
	}
	channels, e := p2p.Channels(root)
	if e != nil || len(channels) != 1 {
		t.Fatalf("channels=%v err=%v", channels, e)
	}
	if out := readTestOutput(t, output); !strings.Contains(out, "RESULT=queued CHANNEL="+channels[0].ID) {
		t.Fatal(out)
	}
	jsonOut := testOutput(t)
	a.out = jsonOut
	if e := a.queueStatus([]string{channels[0].ID, "--json"}); e != nil {
		t.Fatal(e)
	}
	var channel p2p.Channel
	if e := json.Unmarshal([]byte(readTestOutput(t, jsonOut)), &channel); e != nil {
		t.Fatal(e)
	}
	if channel.State != "outgoing" {
		t.Fatalf("queue receipt invented delivery: %+v", channel)
	}
	if e := a.message([]string{"--force-busy", "agent@laptop", "hello"}); e == nil {
		t.Fatal("remote force allowed")
	}
}
