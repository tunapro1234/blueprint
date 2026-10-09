package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"blueprint/internal/api"
	"blueprint/internal/identity"
)

// Rooms and boards are the hive-mind surface: a room is a shared thread whose
// every post lands in each member's queue, a board is shared versioned
// key/value state. The API and MCP already reach them; these commands give the
// terminal agents the same reach over plain bp, so an agent on any harness can
// join the group without an MCP client. The core (internal/api) does the work,
// enforces identity and frames any external author; this layer only parses the
// command line and prints the result.

const roomUsage = `usage:
  bp room list
  bp room join <name> [--topic <text>] [--with <agent,agent,...>]
  bp room post <name> <text...>
  bp room read <name> [--after <post-id>] [--limit <n>]
  bp room leave <name> [<agent>]

A room is a shared thread: every member's queue receives each post, so agents
on any harness talk as a group. Run it inside a bp agent's terminal; the pane
proves who you are.`

const boardUsage = `usage:
  bp board list
  bp board get <board> [<key>] [--prefix <key-prefix>]
  bp board put <board> <key> <value...> [--expect <version>] [--del]
  bp board history <board> <key> [--limit <n>]

A board is shared key/value state with versions and history, for agents to
coordinate without posting into each other's queues.`

// callerForWrite builds the API caller for a room/board write (join, post,
// leave, put) or a membership-scoped read. The pane proves the agent's name,
// exactly as bp msg and bp mcp verify it; a command run outside a known agent
// terminal has no proven identity and is refused, so no one can act as another
// agent from a stray shell.
func (a *app) callerForWrite(action string) (api.Caller, error) {
	who := a.senderIdentity()
	if !who.Certain || !identity.ValidName(who.Label) {
		return api.Caller{}, fmt.Errorf("bp %s needs a proven identity: run it inside a bp agent's terminal", action)
	}
	return api.Caller{Transport: "cli", Name: who.Label, Verified: true}, nil
}

// roomCommand is bp room ...
func (a *app) roomCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", roomUsage)
	}
	core := a.apiCore()
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return fmt.Errorf("usage: bp room list")
		}
		rooms, err := core.Rooms()
		if err != nil {
			return err
		}
		if len(rooms) == 0 {
			fmt.Fprintln(a.err, "no rooms yet; bp room join <name> makes one")
			return nil
		}
		sort.Slice(rooms, func(i, j int) bool { return rooms[i].Name < rooms[j].Name })
		for _, r := range rooms {
			line := fmt.Sprintf("%s\t%d member", r.Name, len(r.Members))
			if len(r.Members) != 1 {
				line += "s"
			}
			if r.Topic != "" {
				line += "\t" + r.Topic
			}
			fmt.Fprintln(a.out, line)
		}
		return nil
	case "join":
		if len(args) < 2 {
			return fmt.Errorf("usage: bp room join <name> [--topic <text>] [--with <agent,...>]")
		}
		name := args[1]
		topic, with := "", ""
		rest := args[2:]
		for i := 0; i < len(rest); i++ {
			switch rest[i] {
			case "--topic":
				if i+1 >= len(rest) {
					return fmt.Errorf("--topic needs text")
				}
				i++
				topic = rest[i]
			case "--with":
				if i+1 >= len(rest) {
					return fmt.Errorf("--with needs a comma-separated agent list")
				}
				i++
				with = rest[i]
			default:
				return fmt.Errorf("unknown room join option %s", rest[i])
			}
		}
		caller, err := a.callerForWrite("room join")
		if err != nil {
			return err
		}
		// Join the caller, plus anyone named in --with. The caller leads the
		// list so a new room has them as a member; adding others to a room the
		// caller has not joined is refused by the core.
		members := []string{caller.Name}
		for _, m := range splitList(with) {
			if m != caller.Name {
				members = append(members, m)
			}
		}
		room, err := core.Join(a.ctx, caller, name, topic, members)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "joined %s (%d members: %s)\n", room.Name, len(room.Members), strings.Join(room.Members, ", "))
		return nil
	case "post":
		if len(args) < 3 {
			return fmt.Errorf("usage: bp room post <name> <text...>")
		}
		caller, err := a.callerForWrite("room post")
		if err != nil {
			return err
		}
		text := strings.Join(args[2:], " ")
		result, err := core.Post(a.ctx, caller, args[1], text)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "posted %s to %s (delivered to %d)\n", result.Post.ID, args[1], len(result.Deliveries))
		for _, e := range result.Errors {
			fmt.Fprintln(a.err, "delivery:", e)
		}
		return nil
	case "read":
		if len(args) < 2 {
			return fmt.Errorf("usage: bp room read <name> [--after <post-id>] [--limit <n>]")
		}
		name := args[1]
		after := ""
		limit := 0
		rest := args[2:]
		for i := 0; i < len(rest); i++ {
			switch rest[i] {
			case "--after":
				if i+1 >= len(rest) {
					return fmt.Errorf("--after needs a post id")
				}
				i++
				after = rest[i]
			case "--limit":
				if i+1 >= len(rest) {
					return fmt.Errorf("--limit needs a number")
				}
				i++
				n, err := strconv.Atoi(rest[i])
				if err != nil || n < 0 {
					return fmt.Errorf("--limit needs a non-negative number")
				}
				limit = n
			default:
				return fmt.Errorf("unknown room read option %s", rest[i])
			}
		}
		caller, err := a.callerForWrite("room read")
		if err != nil {
			return err
		}
		room, posts, err := core.RoomRead(caller, name, after, limit)
		if err != nil {
			return err
		}
		if room.Topic != "" {
			fmt.Fprintf(a.err, "# %s — %s\n", room.Name, room.Topic)
		}
		for _, p := range posts {
			fmt.Fprintf(a.out, "%s\t%s: %s\n", p.ID, p.From, p.Text)
		}
		if len(posts) == 0 {
			fmt.Fprintln(a.err, "no posts")
		}
		return nil
	case "leave":
		if len(args) < 2 || len(args) > 3 {
			return fmt.Errorf("usage: bp room leave <name> [<agent>]")
		}
		caller, err := a.callerForWrite("room leave")
		if err != nil {
			return err
		}
		target := ""
		if len(args) == 3 {
			target = args[2]
		}
		room, err := core.Leave(caller, args[1], target)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "left %s (%d members remain)\n", room.Name, len(room.Members))
		return nil
	}
	return fmt.Errorf("%s", roomUsage)
}

// boardCommand is bp board ...
func (a *app) boardCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", boardUsage)
	}
	core := a.apiCore()
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return fmt.Errorf("usage: bp board list")
		}
		boards, err := core.Boards()
		if err != nil {
			return err
		}
		if len(boards) == 0 {
			fmt.Fprintln(a.err, "no boards yet; bp board put <board> <key> <value> makes one")
			return nil
		}
		sort.Strings(boards)
		for _, b := range boards {
			fmt.Fprintln(a.out, b)
		}
		return nil
	case "get":
		if len(args) < 2 {
			return fmt.Errorf("usage: bp board get <board> [<key>] [--prefix <key-prefix>]")
		}
		board := args[1]
		key, prefix := "", ""
		rest := args[2:]
		for i := 0; i < len(rest); i++ {
			switch {
			case rest[i] == "--prefix":
				if i+1 >= len(rest) {
					return fmt.Errorf("--prefix needs a key prefix")
				}
				i++
				prefix = rest[i]
			case strings.HasPrefix(rest[i], "--"):
				return fmt.Errorf("unknown board get option %s", rest[i])
			case key == "":
				key = rest[i]
			default:
				return fmt.Errorf("bp board get takes one key")
			}
		}
		entries, err := core.BoardGet(board, key, prefix)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			fmt.Fprintln(a.err, "no entries")
			return nil
		}
		for _, e := range entries {
			fmt.Fprintf(a.out, "%s = %s\t(v%d, %s)\n", e.Key, e.Value, e.Version, e.Author)
		}
		return nil
	case "put":
		if len(args) < 4 {
			return fmt.Errorf("usage: bp board put <board> <key> <value...> [--expect <version>] [--del]")
		}
		board, key := args[1], args[2]
		expect, del := -1, false
		valueParts := []string{}
		rest := args[3:]
		for i := 0; i < len(rest); i++ {
			switch rest[i] {
			case "--expect":
				if i+1 >= len(rest) {
					return fmt.Errorf("--expect needs a version number")
				}
				i++
				n, err := strconv.Atoi(rest[i])
				if err != nil || n < 0 {
					return fmt.Errorf("--expect needs a non-negative version")
				}
				expect = n
			case "--del":
				del = true
			default:
				valueParts = append(valueParts, rest[i])
			}
		}
		value := strings.Join(valueParts, " ")
		if del {
			value = ""
		} else if value == "" {
			return fmt.Errorf("bp board put needs a value (or --del to remove the key)")
		}
		caller, err := a.callerForWrite("board put")
		if err != nil {
			return err
		}
		entry, err := core.BoardPut(caller, board, key, value, expect, del)
		if err != nil {
			return err
		}
		if del {
			fmt.Fprintf(a.out, "deleted %s/%s\n", board, key)
		} else {
			fmt.Fprintf(a.out, "%s/%s = %s (v%d)\n", board, entry.Key, entry.Value, entry.Version)
		}
		return nil
	case "history":
		if len(args) < 3 {
			return fmt.Errorf("usage: bp board history <board> <key> [--limit <n>]")
		}
		board, key := args[1], args[2]
		limit := 0
		rest := args[3:]
		for i := 0; i < len(rest); i++ {
			switch rest[i] {
			case "--limit":
				if i+1 >= len(rest) {
					return fmt.Errorf("--limit needs a number")
				}
				i++
				n, err := strconv.Atoi(rest[i])
				if err != nil || n < 0 {
					return fmt.Errorf("--limit needs a non-negative number")
				}
				limit = n
			default:
				return fmt.Errorf("unknown board history option %s", rest[i])
			}
		}
		changes, err := core.BoardHistory(board, key, limit)
		if err != nil {
			return err
		}
		if len(changes) == 0 {
			fmt.Fprintln(a.err, "no history")
			return nil
		}
		for _, ch := range changes {
			fmt.Fprintf(a.out, "%s\tv%d\t%s\t%s = %s\n", ch.Op, ch.Version, ch.Author, ch.Key, ch.Value)
		}
		return nil
	}
	return fmt.Errorf("%s", boardUsage)
}

// splitList parses a comma-separated option value into trimmed, non-empty
// entries.
func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
