package wa

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"blueprint/internal/identity"
)

type Outgoing struct {
	Agent   string  `json:"agent"`
	To      *string `json:"to"`
	ReplyTo *string `json:"replyTo"`
	Text    string  `json:"text"`
}

// Agent resolves the label a WhatsApp message will be signed with. It is the
// shared resolver, not a local chain: this file used to consult
// tmux display-message with no TMUX in the environment, which answered for
// whichever client was attached and signed cron's messages with three
// bystanders' names.
//
// Nothing here falls back to "server-main". A WhatsApp message reaches a phone
// where the label is the only attribution there is, so an unattributable one
// must say "bilinmiyor" (or confess a guess) rather than borrow the
// orchestrator's authority. Inference is allowed for the same reason: the label
// admits it with a "?".
func Agent(ctx context.Context, client identity.Sessioner, opts identity.Options) identity.Identity {
	opts.Infer = true
	opts.Fallback = ""
	return identity.Resolve(ctx, client, opts)
}

func Send(outbox, agent, to, reply, text string) error {
	text = Format(agent, text)
	if err := os.MkdirAll(outbox, 0755); err != nil {
		return err
	}
	var toPtr, replyPtr *string
	if to != "" {
		toCopy := to
		toPtr = &toCopy
	}
	if reply != "" {
		replyCopy := reply
		replyPtr = &replyCopy
	}
	record := Outgoing{Agent: agent, To: toPtr, ReplyTo: replyPtr, Text: text}
	// The bridge consumes visible *.json files. Keep the producer's temporary
	// file outside that namespace so fs.watch can never claim it mid-rename.
	tmp, err := os.CreateTemp(outbox, ".outbox-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	err = json.NewEncoder(tmp).Encode(record)
	if syncErr := tmp.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	target := filepath.Join(outbox, fmt.Sprintf("%d.json", time.Now().UnixNano()))
	return os.Rename(name, target)
}

func Format(agent, text string) string {
	if !strings.HasPrefix(text, "["+agent+"]") {
		text = "[" + agent + "] " + text
	}
	return text
}

type Incoming struct {
	ChatName   string `json:"chatName"`
	SenderName string `json:"senderName"`
	Text       string `json:"text"`
	TS         string `json:"ts"`
}

func rows(store string) ([]Incoming, error) {
	file, err := os.Open(store)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var result []Incoming
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var row Incoming
		if json.Unmarshal(scanner.Bytes(), &row) == nil {
			result = append(result, row)
		}
	}
	return result, scanner.Err()
}

func clock(ts string) string {
	if len(ts) >= 16 {
		return ts[11:16]
	}
	return ""
}

func Read(store, who string, count int) ([]string, error) {
	all, err := rows(store)
	if err != nil {
		return nil, err
	}
	var matched []Incoming
	for _, row := range all {
		if strings.EqualFold(row.ChatName, who) || strings.EqualFold(row.SenderName, who) {
			matched = append(matched, row)
		}
	}
	if count < len(matched) {
		matched = matched[len(matched)-count:]
	}
	result := make([]string, 0, len(matched))
	for _, row := range matched {
		result = append(result, fmt.Sprintf("%s %s: %s", clock(row.TS), fallback(row.SenderName, "?"), row.Text))
	}
	return result, nil
}

func Chats(store string) ([]string, error) {
	all, err := rows(store)
	if err != nil {
		return nil, err
	}
	order := make([]string, 0)
	seen := map[string]string{}
	for _, row := range all {
		name := fallback(row.ChatName, "?")
		if _, ok := seen[name]; !ok {
			order = append(order, name)
		}
		seen[name] = clock(row.TS)
	}
	if len(order) > 20 {
		order = order[len(order)-20:]
	}
	result := make([]string, 0, len(order))
	for _, name := range order {
		result = append(result, fmt.Sprintf("%s %s", seen[name], name))
	}
	return result, nil
}

func fallback(value, other string) string {
	if value == "" {
		return other
	}
	return value
}
