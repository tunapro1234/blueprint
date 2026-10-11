package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"blueprint/internal/tasks"
)

const taskUsage = `usage: bp task <command>

  list [--owner <agent>] [--state <state>] [--parent <id>] [--mine] [--json]
        list tasks, optionally filtered; --mine lists tasks you own (your todo)
  add <title...> [--owner <agent>] [--parent <id>] [--state <state>]
      [--body <text>] [--acceptance <text>]
        create a task and print its id
  done <id>
        mark a task done
  assign <id> <agent>
        set a task's owner

States: todo | in_progress | blocked | done | cancelled`

// cmdTask is the "bp task" command: a thin CLI over the durable task store so
// the task model (docs/fleet-console.md, WS2) is usable without the web console.
// "Todo" is not a separate store: `bp task list --mine` is just the tasks owned
// by the current identity.
func (a *app) cmdTask(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", taskUsage)
	}
	if a.config.StateDir == "" {
		return fmt.Errorf("no state directory configured")
	}
	store := tasks.NewStore(a.config.StateDir)
	switch args[0] {
	case "list":
		return a.taskList(store, args[1:])
	case "add":
		return a.taskAdd(store, args[1:])
	case "done":
		return a.taskDone(store, args[1:])
	case "assign":
		return a.taskAssign(store, args[1:])
	default:
		return fmt.Errorf("unknown task command %q\n%s", args[0], taskUsage)
	}
}

func (a *app) taskList(store *tasks.Store, args []string) error {
	var filter tasks.Filter
	mine, asJSON := false, false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--owner":
			if i+1 >= len(args) {
				return fmt.Errorf("--owner needs an agent name")
			}
			i++
			filter.Owner = args[i]
		case "--state":
			if i+1 >= len(args) {
				return fmt.Errorf("--state needs a state")
			}
			i++
			filter.State = args[i]
		case "--parent":
			if i+1 >= len(args) {
				return fmt.Errorf("--parent needs a task id")
			}
			i++
			filter.Parent = args[i]
		case "--mine":
			mine = true
		case "--json":
			asJSON = true
		default:
			return fmt.Errorf("unknown task list option %s", args[i])
		}
	}
	if mine {
		if filter.Owner != "" {
			return fmt.Errorf("--mine and --owner cannot be combined")
		}
		name := a.currentIdentityName()
		if name == "" {
			return fmt.Errorf("cannot determine your identity; pass --owner <agent> instead")
		}
		filter.Owner = name
	}
	if filter.State != "" && !tasks.ValidState(filter.State) {
		return fmt.Errorf("invalid state %q (todo|in_progress|blocked|done|cancelled)", filter.State)
	}
	list, err := store.List(filter)
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(a.out).Encode(list)
	}
	if len(list) == 0 {
		fmt.Fprintln(a.err, "no tasks; bp task add <title> makes one")
		return nil
	}
	for _, task := range list {
		line := fmt.Sprintf("%s\t%s\t%s", task.ID, task.State, task.Title)
		if task.Owner != "" {
			line += "\t@" + task.Owner
		}
		fmt.Fprintln(a.out, line)
	}
	return nil
}

func (a *app) taskAdd(store *tasks.Store, args []string) error {
	var owner, parent, body, acceptance string
	state := tasks.StateTodo
	var titleParts []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--owner":
			if i+1 >= len(args) {
				return fmt.Errorf("--owner needs an agent name")
			}
			i++
			owner = args[i]
		case "--parent":
			if i+1 >= len(args) {
				return fmt.Errorf("--parent needs a task id")
			}
			i++
			parent = args[i]
		case "--state":
			if i+1 >= len(args) {
				return fmt.Errorf("--state needs a state")
			}
			i++
			state = args[i]
		case "--body":
			if i+1 >= len(args) {
				return fmt.Errorf("--body needs text")
			}
			i++
			body = args[i]
		case "--acceptance":
			if i+1 >= len(args) {
				return fmt.Errorf("--acceptance needs text")
			}
			i++
			acceptance = args[i]
		default:
			if strings.HasPrefix(args[i], "--") {
				return fmt.Errorf("unknown task add option %s", args[i])
			}
			titleParts = append(titleParts, args[i])
		}
	}
	title := strings.TrimSpace(strings.Join(titleParts, " "))
	if title == "" {
		return fmt.Errorf("usage: bp task add <title...> [options]")
	}
	if !tasks.ValidState(state) {
		return fmt.Errorf("invalid state %q (todo|in_progress|blocked|done|cancelled)", state)
	}
	now := time.Now().UTC()
	created, err := store.Create(tasks.Task{
		ID: tasks.NewID(), Title: title, Owner: owner, Parent: parent,
		State: state, Body: body, Acceptance: acceptance, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(a.out, created.ID)
	return nil
}

func (a *app) taskDone(store *tasks.Store, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: bp task done <id>")
	}
	updated, err := store.Update(args[0], time.Now().UTC(), func(task *tasks.Task) error {
		task.State = tasks.StateDone
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%s: %s\n", updated.ID, updated.State)
	return nil
}

func (a *app) taskAssign(store *tasks.Store, args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: bp task assign <id> <agent>")
	}
	id, owner := args[0], args[1]
	updated, err := store.Update(id, time.Now().UTC(), func(task *tasks.Task) error {
		task.Owner = owner
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%s: owner -> %s\n", updated.ID, updated.Owner)
	return nil
}

// currentIdentityName is the bare agent name of the current identity, or "" when
// it cannot be named with confidence. An uncertain label (one carrying the infer
// mark) is not trustworthy enough to filter a personal todo on, so it is
// rejected rather than guessed.
func (a *app) currentIdentityName() string {
	who := a.senderIdentity()
	if who.Inferred() || !who.Certain {
		return ""
	}
	return who.Label
}
