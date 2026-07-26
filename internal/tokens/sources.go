package tokens

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type checkpoint struct {
	Size   int64 `json:"size"`
	Offset int64 `json:"offset"`
	MTime  int64 `json:"mtime"`
}

type promptRef struct {
	ID      string `json:"id"`
	Preview string `json:"preview,omitempty"`
}

type nodeRef struct {
	UUID string    `json:"uuid"`
	Ref  promptRef `json:"ref"`
	TS   string    `json:"ts,omitempty"`
}

type sourceContext struct {
	Kind       string            `json:"kind"`
	Title      string            `json:"title,omitempty"`
	CWD        string            `json:"cwd,omitempty"`
	Originator string            `json:"originator,omitempty"`
	Model      string            `json:"model,omitempty"`
	Nodes      []nodeRef         `json:"nodes,omitempty"`
	Prompts    map[string]string `json:"prompts,omitempty"`
}

type contextFile struct {
	Sources map[string]sourceContext `json:"sources"`
}

type claudeLine struct {
	Type        string `json:"type"`
	CustomTitle string `json:"customTitle"`
	SessionID   string `json:"sessionId"`
	UUID        string `json:"uuid"`
	ParentUUID  string `json:"parentUuid"`
	PromptID    string `json:"promptId"`
	IsMeta      bool   `json:"isMeta"`
	IsSidechain bool   `json:"isSidechain"`
	RequestID   string `json:"requestId"`
	Timestamp   string `json:"timestamp"`
	Message     struct {
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

type pendingClaude struct {
	rec claudeLine
	ref promptRef
}

func discoverFiles(root, kind string) ([]string, error) {
	if root == "" {
		return nil, nil
	}
	var files []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if entry.IsDir() {
			return nil
		}
		base := entry.Name()
		switch kind {
		case "claude":
			if filepath.Ext(base) == ".jsonl" && filepath.Dir(filepath.Dir(path)) == filepath.Clean(root) {
				files = append(files, path)
			}
		case "codex":
			if strings.HasPrefix(base, "rollout-") && filepath.Ext(base) == ".jsonl" {
				files = append(files, path)
			}
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

func readCompleteLines(path string, offset int64, visit func(int64, []byte) error) (int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return offset, err
	}
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}
	reader := bufio.NewReaderSize(file, 128*1024)
	current := offset
	for {
		start := current
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 && err == nil {
			current += int64(len(line))
			if visitErr := visit(start, bytes.TrimSuffix(line, []byte{'\n'})); visitErr != nil {
				return current, visitErr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return current, nil
			}
			return current, err
		}
	}
}

func scanClaude(path string, cp checkpoint, ctx sourceContext, names resolver) ([]RawRecord, checkpoint, sourceContext, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, cp, ctx, err
	}
	if info.Size() < cp.Offset {
		cp.Offset = 0
		ctx = sourceContext{Kind: "claude"}
	}
	if err := backfillNodeTimes(path, ctx.Nodes); err != nil {
		return nil, cp, ctx, err
	}
	nodeMap := make(map[string]promptRef, len(ctx.Nodes)+1024)
	nodeTimes := make(map[string]string, len(ctx.Nodes)+1024)
	order := make([]string, 0, len(ctx.Nodes)+1024)
	for _, node := range ctx.Nodes {
		ref := node.Ref
		if ref.Preview == "" {
			ref.Preview = ctx.Prompts[ref.ID]
		}
		nodeMap[node.UUID] = ref
		nodeTimes[node.UUID] = node.TS
		order = append(order, node.UUID)
	}
	var lines []claudeLine
	nextOffset, err := readCompleteLines(path, cp.Offset, func(_ int64, line []byte) error {
		if !bytes.Contains(line, []byte{'"'}) {
			return nil
		}
		var rec claudeLine
		if json.Unmarshal(line, &rec) != nil {
			return nil
		}
		lines = append(lines, rec)
		if rec.Type == "custom-title" && rec.CustomTitle != "" {
			ctx.Title = rec.CustomTitle
		}
		return nil
	})
	if err != nil {
		return nil, cp, ctx, err
	}

	parents := make(map[string]string, len(lines))
	real := make(map[string]promptRef)
	for _, rec := range lines {
		if rec.UUID != "" {
			parents[rec.UUID] = rec.ParentUUID
			if ts, ok := parseTimestamp(rec.Timestamp); ok {
				nodeTimes[rec.UUID] = ts.Format(time.RFC3339Nano)
			}
		}
		if isRealPrompt(rec) {
			id := rec.PromptID
			if id == "" {
				id = rec.UUID
			}
			real[rec.UUID] = promptRef{ID: id, Preview: contentPreview(rec.Message.Content, 200)}
		}
	}
	var resolve func(string, int, map[string]bool) promptRef
	resolve = func(uuid string, hops int, visiting map[string]bool) promptRef {
		if uuid == "" || hops >= 400 || visiting[uuid] {
			return promptRef{}
		}
		if ref := real[uuid]; ref.ID != "" {
			return ref
		}
		if ref := nodeMap[uuid]; ref.ID != "" {
			return ref
		}
		visiting[uuid] = true
		ref := resolve(parents[uuid], hops+1, visiting)
		delete(visiting, uuid)
		if ref.ID != "" {
			nodeMap[uuid] = ref
		}
		return ref
	}

	var pending []pendingClaude
	for _, rec := range lines {
		if rec.UUID != "" {
			ref := real[rec.UUID]
			if ref.ID == "" {
				ref = resolve(rec.ParentUUID, 0, map[string]bool{})
			}
			if ref.ID != "" {
				nodeMap[rec.UUID] = ref
			}
			order = append(order, rec.UUID)
		}
		if rec.Type == "assistant" {
			usage := rec.Message.Usage
			if usage.InputTokens != 0 || usage.OutputTokens != 0 || usage.CacheCreationInputTokens != 0 || usage.CacheReadInputTokens != 0 {
				pending = append(pending, pendingClaude{rec: rec, ref: resolve(rec.ParentUUID, 0, map[string]bool{})})
			}
		}
	}

	agent, owner := names.claudeAgent(path, ctx.Title)
	rows := make([]RawRecord, 0, len(pending))
	for _, item := range pending {
		ts, ok := parseTimestamp(item.rec.Timestamp)
		if !ok {
			continue
		}
		key := item.rec.RequestID
		if key == "" {
			key = item.rec.SessionID + ":" + item.rec.UUID
		}
		ref := item.ref
		if ref.ID == "" {
			ref = promptRef{ID: "(unattributed)", Preview: "(unattributed)"}
		}
		rows = append(rows, RawRecord{
			Key:        "claude:" + key,
			TS:         ts.Format(time.RFC3339Nano),
			Day:        ts.Format("2006-01-02"),
			Hour:       ts.Hour(),
			Src:        "claude",
			Agent:      agent,
			Owner:      owner,
			Model:      valueOr(item.rec.Message.Model, "?"),
			Requests:   1,
			In:         item.rec.Message.Usage.InputTokens,
			Out:        item.rec.Message.Usage.OutputTokens,
			CacheWrite: item.rec.Message.Usage.CacheCreationInputTokens,
			CacheRead:  item.rec.Message.Usage.CacheReadInputTokens,
			PromptID:   ref.ID,
			Prompt:     ref.Preview,
			Sidechain:  item.rec.IsSidechain,
		})
	}
	ctx.Kind = "claude"
	ctx.Nodes = compactNodes(order, nodeMap, nodeTimes, 2000)
	normalizeContextPrompts(&ctx)
	cp = checkpoint{Size: info.Size(), Offset: nextOffset, MTime: info.ModTime().UnixNano()}
	return rows, cp, ctx, nil
}

func normalizeContextPrompts(context *sourceContext) {
	prompts := make(map[string]string)
	for index := range context.Nodes {
		ref := &context.Nodes[index].Ref
		preview := ref.Preview
		if preview == "" {
			preview = context.Prompts[ref.ID]
		}
		if ref.ID != "" && preview != "" {
			prompts[ref.ID] = preview
		}
		ref.Preview = ""
	}
	if len(prompts) == 0 {
		context.Prompts = nil
	} else {
		context.Prompts = prompts
	}
}

// backfillNodeTimes upgrades contexts written before node timestamps were stored.
// It only retains timestamps for UUIDs already present in the bounded context.
func backfillNodeTimes(path string, nodes []nodeRef) error {
	missing := map[string]*nodeRef{}
	for index := range nodes {
		if nodes[index].UUID != "" && nodes[index].TS == "" {
			missing[nodes[index].UUID] = &nodes[index]
		}
	}
	if len(missing) == 0 {
		return nil
	}
	_, err := readCompleteLines(path, 0, func(_ int64, line []byte) error {
		if len(missing) == 0 || !bytes.Contains(line, []byte(`"uuid"`)) || !bytes.Contains(line, []byte(`"timestamp"`)) {
			return nil
		}
		var identity struct {
			UUID      string `json:"uuid"`
			Timestamp string `json:"timestamp"`
		}
		if json.Unmarshal(line, &identity) != nil {
			return nil
		}
		node := missing[identity.UUID]
		if node == nil {
			return nil
		}
		if ts, ok := parseTimestamp(identity.Timestamp); ok {
			node.TS = ts.Format(time.RFC3339Nano)
			delete(missing, identity.UUID)
		}
		return nil
	})
	return err
}

func compactNodes(order []string, nodes map[string]promptRef, timestamps map[string]string, limit int) []nodeRef {
	seen := map[string]bool{}
	result := make([]nodeRef, 0, limit)
	for index := len(order) - 1; index >= 0 && len(result) < limit; index-- {
		uuid := order[index]
		if uuid == "" || seen[uuid] {
			continue
		}
		ref := nodes[uuid]
		if ref.ID == "" {
			continue
		}
		seen[uuid] = true
		result = append(result, nodeRef{UUID: uuid, Ref: ref, TS: timestamps[uuid]})
	}
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}

func isRealPrompt(rec claudeLine) bool {
	if rec.Type != "user" || rec.IsMeta {
		return false
	}
	var content any
	if len(rec.Message.Content) == 0 || json.Unmarshal(rec.Message.Content, &content) != nil {
		return false
	}
	if blocks, ok := content.([]any); ok {
		allTools := true
		for _, raw := range blocks {
			block, ok := raw.(map[string]any)
			if !ok || block["type"] != "tool_result" {
				allTools = false
				break
			}
		}
		if allTools {
			return false
		}
	}
	preview := contentPreview(rec.Message.Content, 400)
	return preview != "" && !strings.HasPrefix(preview, "<local-command") && !strings.HasPrefix(preview, "<command-name>")
}

func contentPreview(raw json.RawMessage, limit int) string {
	var content any
	if len(raw) == 0 || json.Unmarshal(raw, &content) != nil {
		return ""
	}
	var text string
	switch value := content.(type) {
	case string:
		text = value
	case []any:
		parts := make([]string, 0, len(value))
		for _, rawBlock := range value {
			block, ok := rawBlock.(map[string]any)
			if !ok {
				continue
			}
			switch block["type"] {
			case "text":
				parts = append(parts, fmt.Sprint(block["text"]))
			case "tool_result":
				parts = append(parts, "[tool_result]")
			}
		}
		text = strings.Join(parts, " ")
	default:
		text = fmt.Sprint(value)
	}
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > limit {
		text = text[:limit]
	}
	return text
}

type codexLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Payload   struct {
		Type       string `json:"type"`
		CWD        string `json:"cwd"`
		Originator string `json:"originator"`
		Model      string `json:"model"`
		Info       struct {
			Last struct {
				InputTokens           int64 `json:"input_tokens"`
				CachedInputTokens     int64 `json:"cached_input_tokens"`
				CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
				OutputTokens          int64 `json:"output_tokens"`
				ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
			} `json:"last_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

func scanCodex(path string, cp checkpoint, ctx sourceContext, names resolver) ([]RawRecord, checkpoint, sourceContext, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, cp, ctx, err
	}
	if info.Size() < cp.Offset {
		cp.Offset = 0
		ctx = sourceContext{Kind: "codex"}
	}
	type event struct {
		offset int64
		rec    codexLine
	}
	var events []event
	nextOffset, err := readCompleteLines(path, cp.Offset, func(offset int64, line []byte) error {
		if !bytes.Contains(line, []byte{'"'}) {
			return nil
		}
		var rec codexLine
		if json.Unmarshal(line, &rec) != nil {
			return nil
		}
		if rec.Type == "session_meta" {
			if rec.Payload.CWD != "" {
				ctx.CWD = rec.Payload.CWD
			}
			if rec.Payload.Originator != "" {
				ctx.Originator = rec.Payload.Originator
			}
			if rec.Payload.Model != "" {
				ctx.Model = rec.Payload.Model
			}
		} else if rec.Type == "event_msg" && rec.Payload.Type == "token_count" {
			events = append(events, event{offset: offset, rec: rec})
		}
		return nil
	})
	if err != nil {
		return nil, cp, ctx, err
	}
	agent, owner := names.codexAgent(ctx.CWD)
	rows := make([]RawRecord, 0, len(events))
	for _, event := range events {
		last := event.rec.Payload.Info.Last
		if last.InputTokens == 0 && last.CachedInputTokens == 0 && last.CacheWriteInputTokens == 0 && last.OutputTokens == 0 && last.ReasoningOutputTokens == 0 {
			continue
		}
		ts, ok := parseTimestamp(event.rec.Timestamp)
		if !ok {
			continue
		}
		hash := sha256.Sum256([]byte(path + "\x00" + strconv.FormatInt(event.offset, 10)))
		originator := valueOr(ctx.Originator, "codex")
		rows = append(rows, RawRecord{
			Key:        "codex:" + hex.EncodeToString(hash[:16]),
			TS:         ts.Format(time.RFC3339Nano),
			Day:        ts.Format("2006-01-02"),
			Hour:       ts.Hour(),
			Src:        "codex",
			Agent:      agent,
			Owner:      owner,
			Model:      valueOr(ctx.Model, "gpt-5.6"),
			Requests:   1,
			In:         last.InputTokens,
			Out:        last.OutputTokens + last.ReasoningOutputTokens,
			CacheWrite: last.CacheWriteInputTokens,
			CacheRead:  last.CachedInputTokens,
			PromptID:   originator,
			Prompt:     "[codex " + originator + "] " + truncate(filepath.Base(path), 40),
			Sidechain:  ctx.Originator == "codex_exec",
		})
	}
	ctx.Kind = "codex"
	cp = checkpoint{Size: info.Size(), Offset: nextOffset, MTime: info.ModTime().UnixNano()}
	return rows, cp, ctx, nil
}

func parseTimestamp(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	ts, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false
	}
	return ts.In(istanbul), true
}

func truncate(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
