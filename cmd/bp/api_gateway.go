package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sort"
	"time"

	"blueprint/internal/api"
)

// gatewayOrigins are the browser origins of the web apps the gateway serves.
var gatewayOrigins = []string{"https://claude.ai", "https://chatgpt.com"}

// gateway builds the remote MCP gateway from api.gateway in the config.
func (a *app) gateway() (*api.Gateway, error) {
	if a.config.API == nil || a.config.API.Gateway == nil {
		return nil, fmt.Errorf("api.gateway is not configured in %s", a.config.Path)
	}
	cfg := a.config.API.Gateway
	profiles := map[string]api.GatewayProfile{}
	for name, client := range cfg.Clients {
		profiles[name] = api.GatewayProfile{Name: name, Policy: api.Policy{
			Agents: client.Agents, Rooms: client.Rooms, Boards: client.Boards, ReadOnlyBoards: client.ReadOnlyBoards,
			RatePerHour: client.RatePerHour, Burst: client.Burst, MaxBytes: client.MaxBytes, Redact: client.Redact}}
	}
	return &api.Gateway{Core: a.apiCore(), PublicURL: cfg.PublicURL, Profiles: profiles, AllowedOrigins: gatewayOrigins}, nil
}

// startGateway serves the gateway on its loopback address until ctx ends.
func (a *app) startGateway(ctx context.Context, logger *log.Logger, errs chan<- error) error {
	gateway, err := a.gateway()
	if err != nil {
		return err
	}
	cfg := a.config.API.Gateway
	if cfg.PublicURL == "" {
		return fmt.Errorf("api.gateway.publicUrl is required: OAuth clients must know the public address")
	}
	if len(cfg.Clients) == 0 {
		return fmt.Errorf("api.gateway.clients is empty: nothing would be exposed")
	}
	if err := loopbackOnly(cfg.Listen); err != nil {
		return err
	}
	// Fail closed: remote text must be framed on every path first.
	if err := gateway.Ready(); err != nil {
		return fmt.Errorf("the gateway stays off: %w", err)
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	server := gateway.HTTPServer()
	logger.Printf("gateway serving on http://%s for %s", listener.Addr(), cfg.PublicURL)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()
	go func() {
		<-ctx.Done()
		server.Close()
	}()
	return nil
}

const gatewayUsage = `usage:
  bp api gateway clients
  bp api gateway pending           registered web apps waiting to be paired
  bp api gateway pair <client> <client-id>
                                   one-time code for that web app's sign-in page
  bp api gateway token <client>    static bearer token (shown once)
  bp api gateway revoke <client|--all>`

// gatewayCommand is bp api gateway ...
func (a *app) gatewayCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", gatewayUsage)
	}
	gateway, err := a.gateway()
	if err != nil {
		return err
	}
	switch {
	case args[0] == "clients" && len(args) == 1:
		names := make([]string, 0, len(gateway.Profiles))
		for name := range gateway.Profiles {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			p := gateway.Profiles[name].Policy
			fmt.Fprintf(a.out, "%s\tagents=%v rooms=%v boards=%v read-only=%v\n", name, p.Agents, p.Rooms, p.Boards, p.ReadOnlyBoards)
		}
		return nil
	case args[0] == "pending" && len(args) == 1:
		clients, err := gateway.Clients()
		if err != nil {
			return err
		}
		for _, c := range clients {
			state := "waiting"
			if c.Paired {
				state = "paired"
			}
			fmt.Fprintf(a.out, "%s\t%s\t%q\t%v\t%s\n", c.ID, state, c.Name, c.Redirect, c.Created.Format(time.RFC3339))
		}
		return nil
	case args[0] == "pair" && len(args) == 3:
		code, err := gateway.Pair(args[1], args[2])
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "%s\nEnter this code on the sign-in page that shows client id %s, within 10 minutes. It works once.\n", code, args[2])
		return nil
	case args[0] == "token" && len(args) == 2:
		token, err := gateway.IssueStaticToken(args[1])
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "%s\nSend it as Authorization: Bearer <token>. It is not stored and is shown only now.\n", token)
		return nil
	case args[0] == "revoke" && len(args) == 2:
		profile := args[1]
		if profile == "--all" {
			profile = ""
		}
		n, err := gateway.Revoke(profile)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "revoked %d tokens\n", n)
		return nil
	}
	return fmt.Errorf("%s", gatewayUsage)
}
