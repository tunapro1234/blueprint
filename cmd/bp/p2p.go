package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"blueprint/internal/p2p"
	"github.com/libp2p/go-libp2p/core/peer"
)

func (a *app) p2pCommand(args []string) error {
	if len(args) == 0 {
		args = []string{"status"}
	}
	switch args[0] {
	case "id":
		if len(args) != 1 {
			return fmt.Errorf("usage: bp p2p id")
		}
		key, e := p2p.Identity(a.config.StateDir)
		if e != nil {
			return e
		}
		id, e := peer.IDFromPrivateKey(key)
		if e != nil {
			return e
		}
		fmt.Fprintln(a.out, id)
		return nil
	case "start":
		if len(args) != 1 {
			return fmt.Errorf("usage: bp p2p start")
		}
		if e := a.ensureP2P(); e != nil {
			return e
		}
		fmt.Fprintln(a.out, "p2p service ready")
		return nil
	case "stop":
		if len(args) != 1 {
			return fmt.Errorf("usage: bp p2p stop")
		}
		return p2p.Control(a.ctx, a.config.StateDir, http.MethodPost, "/stop", nil)
	case "status":
		if len(args) > 2 || (len(args) == 2 && args[1] != "--json") {
			return fmt.Errorf("usage: bp p2p status [--json]")
		}
		var info p2p.Info
		if e := p2p.Control(a.ctx, a.config.StateDir, http.MethodGet, "/status", &info); e != nil {
			return fmt.Errorf("p2p service unavailable: %w; start with bp p2p start", e)
		}
		if len(args) == 2 {
			return json.NewEncoder(a.out).Encode(info)
		}
		fmt.Fprintf(a.out, "%s (pid %d)\n", info.ID, info.PID)
		for _, address := range info.Addresses {
			fmt.Fprintf(a.out, "  %s/p2p/%s\n", address, info.ID)
		}
		for _, p := range info.Peers {
			state := "disconnected"
			if p.Connected {
				state = p.Path
			}
			fmt.Fprintf(a.out, "  %s: %s\n", p.Name, state)
		}
		return nil
	case "channels":
		if len(args) > 2 || (len(args) == 2 && args[1] != "--json") {
			return fmt.Errorf("usage: bp p2p channels [--json]")
		}
		channels, e := p2p.Channels(a.config.StateDir)
		if e != nil {
			return e
		}
		if len(args) == 2 {
			return json.NewEncoder(a.out).Encode(channels)
		}
		for _, c := range channels {
			fmt.Fprintf(a.out, "%s %s %s@%s %s\n", c.ID, c.State, c.To, c.Peer, c.LastError)
		}
		return nil
	case "ping":
		if len(args) != 2 {
			return fmt.Errorf("usage: bp p2p ping <peer>")
		}
		if e := a.ensureP2P(); e != nil {
			return e
		}
		start := time.Now()
		if e := p2p.Ping(a.ctx, a.config.StateDir, args[1]); e != nil {
			return e
		}
		fmt.Fprintf(a.out, "%s: connected (%s)\n", args[1], time.Since(start).Round(time.Millisecond))
		return nil
	case "serve":
		if len(args) != 1 {
			return fmt.Errorf("usage: bp p2p serve")
		}
		if a.config.InvalidConfig != "" || a.config.P2P == nil || !a.config.P2P.Enabled {
			return fmt.Errorf("p2p.enabled must be true in a valid configuration")
		}
		ctx, cancel := signal.NotifyContext(a.ctx, syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		a.ctx = ctx
		n, e := p2p.New(ctx, a.config.StateDir, *a.config.P2P, a.queue)
		if e != nil {
			return e
		}
		defer n.Close()
		n.Log = a.err
		return n.Serve(ctx, a.dispatchNow)
	default:
		return fmt.Errorf("usage: bp p2p id|start|stop|status|channels|ping|serve")
	}
}

// ensureP2P starts one detached, machine-local network worker. It carries no
// tmux or Codex thread identity inherited from whichever CLI started it.
func (a *app) ensureP2P() error {
	if a.config.InvalidConfig != "" || a.config.P2P == nil || !a.config.P2P.Enabled {
		return fmt.Errorf("enable p2p in %s first", a.config.Path)
	}
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
	defer cancel()
	var info p2p.Info
	if p2p.Control(ctx, a.config.StateDir, http.MethodGet, "/status", &info) == nil {
		return nil
	}
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	logPath := p2p.LogPath(a.config.StateDir)
	if e = os.MkdirAll(filepath.Dir(logPath), 0700); e != nil {
		return e
	}
	log, e := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if e != nil {
		return e
	}
	defer log.Close()
	cmd := exec.Command(exe, "p2p", "serve")
	cmd.Stdin = nil
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		switch key {
		case "TMUX", "TMUX_PANE", "AGENT", "CODEX_THREAD_ID", "BP_HOME":
			continue
		}
		cmd.Env = append(cmd.Env, v)
	}
	cmd.Env = append(cmd.Env, "BP_HOME="+a.config.Home)
	if e = cmd.Start(); e != nil {
		return e
	}
	_ = cmd.Process.Release()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("p2p service did not start; inspect %s", logPath)
		case <-ticker.C:
			if p2p.Control(ctx, a.config.StateDir, http.MethodGet, "/status", &info) == nil {
				return nil
			}
		}
	}
}

func (a *app) p2pMessage(target, alias, text string) error {
	who := a.senderIdentity()
	c, e := p2p.EnqueueSender(a.config.StateDir, *a.config.P2P, alias, target, who.Label, text,
		p2p.SenderClaim{Thread: who.ThreadID, Source: who.Source, Certain: who.Certain})
	if e != nil {
		return e
	}
	// The durable channel exists even if the local worker cannot start. Report its
	// ID rather than encouraging the caller to create a second copy of the message.
	if e = a.ensureP2P(); e != nil {
		fmt.Fprintf(a.err, "WARNING: %v; channel retained on disk\n", e)
	}
	fmt.Fprintf(a.out, "queued for %s@%s (channel: %s)\n", target, alias, c.ID)
	a.resultLine("queued", c.ID)
	return nil
}
func (a *app) p2pChannelStatus(args []string) error {
	if len(args) < 1 || len(args) > 2 || (len(args) == 2 && args[1] != "--json") {
		return fmt.Errorf("usage: bp qstat <channel-id> [--json]")
	}
	c, e := p2p.ReadChannel(a.config.StateDir, args[0])
	if e != nil {
		return e
	}
	if len(args) == 2 {
		return json.NewEncoder(a.out).Encode(c)
	}
	fmt.Fprintf(a.out, "%s: %s@%s (channel: %s)\n", strings.ToUpper(c.State), c.To, c.Peer, c.ID)
	if c.Reason != "" {
		fmt.Fprintln(a.out, c.Reason)
	}
	if c.LastError != "" {
		fmt.Fprintln(a.out, "last connection attempt:", c.LastError)
	}
	if !c.Updated.IsZero() {
		fmt.Fprintln(a.out, "last checked:", c.Updated.Local().Format(time.RFC3339))
	}
	return nil
}
