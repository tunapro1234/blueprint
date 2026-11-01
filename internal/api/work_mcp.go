package api

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
)

// workTools are the MCP tools over the work ledger. Each takes an op from
// its own group and the shared WorkRequest fields, so the MCP surface and
// the CLI/REST surface stay one-to-one.
func workTools() []mcpTool {
	groups := []struct {
		name, title, prefix, desc string
		readOnly                  bool
	}{
		{"bp_work_project", "Work projects", "project.", "Create, list, read, pause, resume or stop a work project, or add a root directory whose files may be cited. Only the lead changes a project.", false},
		{"bp_work_task", "Work tasks", "task.", "Tasks with one owner at a time. claim returns a fence; pass it on renew/checkpoint/block/unblock/release/submit. A replaced owner's fence is refused. recover hands back a task whose lease expired. submit records artifacts with sha256 digests; verify (pass/fail with checks) must come from someone other than the submitter. A message receipt never verifies a task.", false},
		{"bp_work_finding", "Work findings", "finding.", "Shared findings with evidence: add (kind finding or negative; a negative result needs conditions), verify/refute with evidenceText (verify needs someone other than the author), check re-hashes sources and marks a finding stale when they changed, revise (author only) re-cites current sources.", false},
		{"bp_work_feed", "Work change feed", "feed", "Changes after a cursor, bounded and in order: op feed (after, limit, filter {kinds, ids, involving}, summary). Or keep a stored cursor: feed.subscribe, feed.next (does not move it), feed.ack with after = the page's next cursor once handled.", false},
		{"bp_work_brief", "Work brief", "brief", "Deterministic handoff for a project: goal, owners, verified and stale findings, failed experiments, blockers, next actions and the feed cursor to continue from. op brief or doctor (health issues only).", true},
	}
	props := workProps()
	tools := []mcpTool{}
	for _, g := range groups {
		ops := []string{}
		for op := range WorkOps {
			if strings.HasPrefix(op, g.prefix) || (g.prefix == "brief" && op == "doctor") {
				ops = append(ops, op)
			}
		}
		sort.Strings(ops)
		schemaProps := map[string]any{"op": map[string]any{"type": "string", "enum": ops, "description": "Operation."}}
		for k, v := range props {
			schemaProps[k] = v
		}
		tool := mcpTool{Name: g.name, Title: g.title, Description: g.desc, InputSchema: schema([]string{"op"}, schemaProps)}
		if g.readOnly {
			tool.Annotations = readOnly()
		}
		allowed := map[string]bool{}
		for _, op := range ops {
			allowed[op] = true
		}
		tool.run = func(ctx context.Context, s *MCPSession, raw json.RawMessage) (any, error) {
			req, err := decode[WorkRequest](raw)
			if err != nil {
				return nil, err
			}
			if !allowed[req.Op] {
				return nil, invalid("%s does not take op %q (use one of %s)", g.name, req.Op, strings.Join(ops, ", "))
			}
			caller := s.Caller()
			if WorkOps[req.Op] {
				if caller, err = s.named(); err != nil {
					return nil, err
				}
			}
			return s.Core.Work(caller, req)
		}
		tools = append(tools, tool)
	}
	return tools
}

func workProps() map[string]any {
	refs := func(d string) map[string]any {
		return map[string]any{"type": "array", "description": d, "items": map[string]any{"type": "object", "additionalProperties": false,
			"required": []string{"path"},
			"properties": map[string]any{
				"path":     str("Absolute path inside a project root."),
				"revision": str("Optional revision you name (e.g. a git commit)."),
				"digest":   str("Optional expected sha256; refused if the file differs."),
			}}}
	}
	strs := func(d string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": d}
	}
	return map[string]any{
		"project":      str("Project name."),
		"id":           str("Task id (t1) or finding id (f1)."),
		"goal":         str("project.create: the goal."),
		"roots":        strs("project.create: absolute directories whose files may be cited."),
		"root":         str("project.root: directory to add."),
		"reason":       str("Reason (pause/stop/recover/cancel/reopen/block)."),
		"title":        str("task.create: one-line title."),
		"spec":         str("task.create: what done means."),
		"deps":         strs("task.create: task ids this depends on."),
		"dep":          str("task.dep: task id to depend on."),
		"verifier":     str("task.create: the only agent allowed to verify."),
		"maxAttempts":  integer("task.create: failed verifications allowed before the task fails (default 3)."),
		"leaseSeconds": integer("task.claim/renew: lease length (default 900, max 86400)."),
		"fence":        integer("Owner writes: the fence your claim returned."),
		"version":      integer("Optional expected version of the task or finding."),
		"note":         str("Checkpoint, submission or release note."),
		"progress":     integer("task.checkpoint: 0-100."),
		"state":        str("task.list: only this state."),
		"owner":        str("task.list: only this owner."),
		"artifacts":    refs("task.submit: the result files."),
		"result":       str("task.verify: pass or fail."),
		"checks":       str("task.verify: what you checked and how."),
		"evidence":     refs("task.verify: files backing the verification."),
		"validator": map[string]any{"type": "object", "description": "task.verify: a validator you ran (argv, exit, output excerpt).", "additionalProperties": false,
			"properties": map[string]any{"command": strs("argv"), "exit": integer("exit code"), "output": str("output excerpt"), "outputDigest": str("sha256 of the full output")}},
		"submission":   integer("task.verify: submission number checked (default latest)."),
		"kind":         str("finding.add: finding or negative."),
		"assertion":    str("finding.add/revise: the claim."),
		"conditions":   str("finding.add: when a negative result applies."),
		"task":         str("finding.add: related task id."),
		"sources":      refs("finding.add/revise: files the claim rests on."),
		"evidenceText": str("finding.verify/refute: what you did to confirm or refute it."),
		"status":       str("finding.list: only this stored status."),
		"after":        str("feed: cursor to read after; feed.ack: cursor to store."),
		"limit":        integer("feed: maximum events (default 50, max 500)."),
		"filter": map[string]any{"type": "object", "additionalProperties": false, "description": "feed/feed.subscribe filter.",
			"properties": map[string]any{"kinds": strs("project, task, finding"), "ids": strs("entity ids"), "involving": str("agent label")}},
		"summary": map[string]any{"type": "boolean", "description": "feed: omit entity bodies."},
		"from":    str("feed.subscribe: start cursor, 'start', or empty for now."),
		"items":   integer("brief: items per list (default 20, max 100)."),
	}
}
