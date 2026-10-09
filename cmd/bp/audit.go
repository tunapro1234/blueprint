package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"blueprint/internal/audit"
)

func (a *app) auditCommand(args []string) error {
	const usage = "usage: bp audit [--since <dur>] [--kind <prefix>] [--severity info|warn|alert] [--peer <alias>] [-n N] [--json]"
	filter := audit.Filter{Limit: 50}
	jsonOut := false
	for i := 0; i < len(args); i++ {
		value := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s", usage)
			}
			i++
			return args[i], nil
		}
		switch args[i] {
		case "--json":
			jsonOut = true
		case "--since":
			v, err := value()
			if err != nil {
				return err
			}
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				return fmt.Errorf("invalid --since: %s", v)
			}
			filter.Since = time.Now().Add(-d)
		case "--kind":
			v, err := value()
			if err != nil {
				return err
			}
			filter.Kind = v
		case "--severity":
			v, err := value()
			if err != nil {
				return err
			}
			if v != audit.Info && v != audit.Warn && v != audit.Alert {
				return fmt.Errorf("invalid --severity: %s", v)
			}
			filter.Severity = v
		case "--peer":
			v, err := value()
			if err != nil {
				return err
			}
			filter.Peer = v
		case "-n":
			v, err := value()
			if err != nil {
				return err
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return fmt.Errorf("invalid -n: %s", v)
			}
			filter.Limit = n
		default:
			return fmt.Errorf("%s", usage)
		}
	}
	events, err := audit.Read(a.config.StateDir, filter)
	if err != nil {
		return err
	}
	if jsonOut {
		if events == nil {
			events = []audit.Event{}
		}
		return json.NewEncoder(a.out).Encode(events)
	}
	if len(events) == 0 {
		fmt.Fprintln(a.out, "no audit events")
		return nil
	}
	for _, ev := range events {
		parts := []string{ev.Time.Local().Format(time.DateTime), ev.Severity, ev.Kind}
		if ev.Peer != "" {
			parts = append(parts, "peer="+printable(ev.Peer))
		}
		if ev.Actor != "" {
			parts = append(parts, "from="+printable(ev.Actor))
		}
		if ev.Target != "" {
			parts = append(parts, "to="+printable(ev.Target))
		}
		if ev.Reason != "" {
			parts = append(parts, "reason="+strconv.QuoteToASCII(ev.Reason))
		}
		fmt.Fprintln(a.out, strings.Join(parts, " "))
	}
	return nil
}

// printable quotes values that came from peers so the terminal never
// interprets their bytes.
func printable(s string) string {
	for _, r := range s {
		if r <= ' ' || r == '"' || r == 0x7f || r > 0x7e {
			return strconv.QuoteToASCII(s)
		}
	}
	return s
}

// messagesCommand prints the message log or rebuilds it from done/.
func (a *app) messagesCommand(args []string) error {
	const usage = "usage: bp messages reindex"
	if len(args) != 1 || args[0] != "reindex" {
		return fmt.Errorf("%s", usage)
	}
	if a.queue == nil {
		return fmt.Errorf("no message queue configured")
	}
	n, err := a.queue.Reindex()
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "messages.jsonl: %d finished message(s) added\n", n)
	return nil
}
