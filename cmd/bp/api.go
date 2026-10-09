package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"blueprint/internal/api"
	"blueprint/internal/book"
	"blueprint/internal/delivery"
	"blueprint/internal/guard"
	"blueprint/internal/identity"
	"blueprint/internal/msgq"
)

// apiCore builds the API core over this bp's queue and agentbooks.
func (a *app) apiCore() *api.Core {
	core := api.NewCore(a.config.StateDir, a.queue, a.apiDirectory)
	core.Kick = a.apiKick()
	// The same framer key frames the API's own stores at read time and queue
	// records at delivery. Without it the local API still works (local
	// callers are never framed) and the gateway refuses to start.
	if a.config.StateDir != "" {
		if framer, err := guard.LoadFramer(a.config.StateDir); err != nil {
			fmt.Fprintf(a.err, "bp: api: guard framing unavailable: %v; the remote gateway stays off\n", err)
		} else {
			core.Frame = api.GuardFramer{F: framer}
		}
		if render, err := delivery.Renderer(a.config.StateDir); err == nil {
			core.Render = render
		}
	}
	core.Watch = a.guardWatch()
	return core
}

// guardWatch returns this process's single taint tracker, built once and shared
// by every api.Core (server, gateway, room, mcp), so a reach that spans two of
// them in one process (e.g. untrusted input on the gateway, then an action the
// local API sees) is not missed because each built its own Watch. bp p2p serve
// and bp api serve run as separate processes, so the TaintSource
// (MessageLogTaint on the msgq log) carries taint across processes: it is the
// same messages.jsonl the P2P node's default Watch and the tool-call hook read.
func (a *app) guardWatch() *guard.Watch {
	if a.guardWatchInst == nil {
		errw := a.err
		if errw == nil {
			errw = os.Stderr
		}
		a.guardWatchInst = guard.NewWatch(
			guard.WatchConfig{TaintSource: guard.MessageLogTaint(msgq.MessageLogPath(a.config.MsgqRoot))},
			guard.AuditSink{StateDir: a.config.StateDir, Errors: errw})
	}
	return a.guardWatchInst
}

// apiDirectory lists the agentbook agents with their live state.
func (a *app) apiDirectory(context.Context) ([]api.AgentInfo, error) {
	fleet, states, err := a.fleet()
	if err != nil {
		return nil, err
	}
	agents := make([]api.AgentInfo, 0, len(fleet.Agents))
	for _, name := range fleet.SortedNames() {
		agent := fleet.Agents[name]
		state := "closed"
		if live, ok := states[name]; ok {
			switch {
			case live.Dead:
				state = "dead"
			case live.Busy:
				state = "working"
			case live.Alive:
				state = "idle"
			}
		}
		agents = append(agents, api.AgentInfo{Name: name, Kind: api.KindTerminal, State: state,
			Parent: fleet.Parents[name], Role: agent.Role})
	}
	return agents, nil
}

// apiKick runs a delivery pass for one target right after an API send, so a
// message to an idle agent does not wait for the daemon's next tick. Passes
// are serialized: the pane-lock bookkeeping is per process and not safe from
// several goroutines at once. A kick that finds one running is coalesced.
func (a *app) apiKick() func(string) {
	var mu sync.Mutex
	return func(target string) {
		if !mu.TryLock() {
			return
		}
		defer mu.Unlock()
		a.prepareDispatch()
		if a.queue == nil || a.tmux == nil {
			return
		}
		_ = a.queue.DispatchTargets(a.ctx, a.tmux, []string{target}, func(string) {})
	}
}

const apiUsage = `usage:
  bp serve --api [--listen 127.0.0.1:PORT] [--no-socket]
  bp mcp [--as <name>]
  bp api token [--path]
  bp api config <claude|codex|gemini|antigravity|grok|opencode|cursor|hermes> [--http]
  bp api gateway clients|pair|token|revoke`

// serve runs the local API in the foreground.
func (a *app) serve(args []string) error {
	listen, socket, gateway := "", true, false
	sawAPI := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--api":
			sawAPI = true
		case "--gateway":
			gateway = true
		case "--no-socket":
			socket = false
		case "--listen":
			if i+1 >= len(args) {
				return fmt.Errorf("--listen needs 127.0.0.1:PORT")
			}
			i++
			listen = args[i]
		default:
			return fmt.Errorf("unknown serve option %s\n%s", args[i], apiUsage)
		}
	}
	if !sawAPI && !gateway {
		return fmt.Errorf("%s", apiUsage)
	}
	ctx, stop := signal.NotifyContext(a.ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := log.New(a.err, "bp api: ", log.LstdFlags)
	errs := make(chan error, 3)
	running := 0
	if sawAPI {
		if !socket && listen == "" {
			return fmt.Errorf("nothing to serve: --no-socket without --listen")
		}
		n, err := a.startAPI(ctx, logger, socket, listen, errs)
		if err != nil {
			return err
		}
		running += n
	}
	if gateway {
		if err := a.startGateway(ctx, logger, errs); err != nil {
			return err
		}
		running++
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-errs:
		return err
	}
}

// startAPI serves the local API on the unix socket and/or a loopback TCP
// address until ctx ends. It returns how many listeners started.
func (a *app) startAPI(ctx context.Context, logger *log.Logger, socket bool, listen string, errs chan<- error) (int, error) {
	token, err := api.LoadOrCreateToken(a.config.StateDir)
	if err != nil {
		return 0, err
	}
	server := &api.Server{Core: a.apiCore(), Token: token}
	var listeners []net.Listener
	if socket {
		listener, err := api.ListenUnix(api.SocketPath(a.config.StateDir))
		if err != nil {
			return 0, err
		}
		listeners = append(listeners, listener)
		logger.Printf("serving on unix socket %s", api.SocketPath(a.config.StateDir))
	}
	if listen != "" {
		if err := loopbackOnly(listen); err != nil {
			closeAll(listeners)
			return 0, err
		}
		listener, err := net.Listen("tcp", listen)
		if err != nil {
			closeAll(listeners)
			return 0, err
		}
		listeners = append(listeners, listener)
		logger.Printf("serving on http://%s (token: %s)", listener.Addr(), api.TokenPath(a.config.StateDir))
	}
	for _, listener := range listeners {
		httpServer := server.HTTPServer()
		go func(l net.Listener) {
			if err := httpServer.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- err
			}
		}(listener)
		go func() {
			<-ctx.Done()
			httpServer.Close()
		}()
	}
	return len(listeners), nil
}

func closeAll(listeners []net.Listener) {
	for _, l := range listeners {
		l.Close()
	}
}

func loopbackOnly(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("--listen %s: %w", address, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("--listen %s: the local API binds loopback only (127.0.0.1 or [::1])", address)
	}
	return nil
}

// startDaemonAPI is called by bp daemon: it serves the API only when the
// config enables it, and logs instead of failing the daemon.
func (a *app) startDaemonAPI(ctx context.Context, logger *log.Logger) {
	if a.config.API == nil {
		return
	}
	errs := make(chan error, 3)
	if a.config.API.Enabled {
		if _, err := a.startAPI(ctx, logger, true, a.config.API.Listen, errs); err != nil {
			logger.Printf("api: not started: %v", err)
		}
	}
	if a.config.API.Gateway != nil && a.config.API.Gateway.Enabled {
		if err := a.startGateway(ctx, logger, errs); err != nil {
			logger.Printf("api gateway: not started: %v", err)
		}
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case err := <-errs:
				logger.Printf("api: %v", err)
			}
		}
	}()
}

// mcp runs an MCP server on stdin/stdout for the calling agent.
func (a *app) mcp(args []string) error {
	as := os.Getenv("BP_AGENT")
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--as":
			if i+1 >= len(args) {
				return fmt.Errorf("--as needs an agent name")
			}
			i++
			as = args[i]
		default:
			return fmt.Errorf("unknown mcp option %s\n%s", args[i], apiUsage)
		}
	}
	if as != "" && !identity.ValidName(as) {
		return fmt.Errorf("--as %q is not a valid agent name", as)
	}
	caller := api.Caller{Transport: "mcp"}
	who := a.senderIdentity()
	if who.Certain && identity.ValidName(who.Label) {
		// Inside a bp terminal the pane proves who this is; --as cannot
		// override it, or any agent could speak as any other.
		if as != "" && as != who.Label {
			fmt.Fprintf(a.err, "bp mcp: ignoring --as %s: this terminal belongs to %s\n", as, who.Label)
		}
		caller.Name, caller.Verified = who.Label, true
	} else {
		caller.Name = as
	}
	if caller.Name != "" && !caller.Verified {
		if fleet, err := book.LoadFleet(book.Paths(a.config.Agentbooks)); err == nil {
			if _, taken := fleet.Agents[caller.Name]; taken {
				fmt.Fprintf(a.err, "bp mcp: %s is a terminal agent; this session is not running in its terminal, so its messages are marked unverified\n", caller.Name)
			}
		}
	}
	session := api.NewMCPSession(a.apiCore(), caller, nil)
	ctx, stop := signal.NotifyContext(a.ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := api.ServeStdio(ctx, session, os.Stdin, a.out)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// apiCommand is bp api token|config.
func (a *app) apiCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", apiUsage)
	}
	switch args[0] {
	case "token":
		if len(args) == 2 && args[1] == "--path" {
			fmt.Fprintln(a.out, api.TokenPath(a.config.StateDir))
			return nil
		}
		if len(args) != 1 {
			return fmt.Errorf("usage: bp api token [--path]")
		}
		token, err := api.LoadOrCreateToken(a.config.StateDir)
		if err != nil {
			return err
		}
		fmt.Fprintln(a.out, token)
		return nil
	case "config":
		if len(args) < 2 || len(args) > 3 || (len(args) == 3 && args[2] != "--http") {
			return fmt.Errorf("usage: bp api config <client> [--http]")
		}
		url := ""
		if len(args) == 3 {
			if a.config.API == nil || a.config.API.Listen == "" {
				return fmt.Errorf("--http needs api.listen in %s; stdio (without --http) needs nothing", a.config.Path)
			}
			url = "http://" + a.config.API.Listen + "/mcp"
		}
		snippet, err := api.ClientSnippet(args[1], url)
		if err != nil {
			return err
		}
		fmt.Fprint(a.out, snippet)
		return nil
	case "gateway":
		return a.gatewayCommand(args[1:])
	}
	return fmt.Errorf("%s", apiUsage)
}
