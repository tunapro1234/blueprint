package wa

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"blueprint/internal/identity"
)

type Outgoing struct {
	Agent      string   `json:"agent"`
	To         *string  `json:"to"`
	ReplyTo    *string  `json:"replyTo"`
	Text       string   `json:"text"`
	Mentions   []string `json:"mentions,omitempty"`
	MentionAll bool     `json:"mentionAll,omitempty"`
}

type SendOptions struct {
	To         string
	Reply      string
	Mentions   []string
	MentionAll bool
}

// Agent resolves the label a WhatsApp message will be signed with. It is the
// shared resolver, not a local chain: this file used to consult
// tmux display-message with no TMUX in the environment, which answered for
// whichever client was attached and signed cron's messages with three
// bystanders' names.
//
// Nothing here falls back to "server-main". A WhatsApp message reaches a phone
// where the label is the only attribution there is, so an unattributable one
// must say "unknown" (or confess a guess) rather than borrow the
// orchestrator's authority. Inference is allowed for the same reason: the label
// admits it with a "?".
func Agent(ctx context.Context, client identity.Sessioner, opts identity.Options) identity.Identity {
	opts.Infer = true
	opts.Fallback = ""
	return identity.Resolve(ctx, client, opts)
}

func Send(outbox, agent, text string, opts SendOptions) error {
	text = Format(agent, text)
	if err := os.MkdirAll(outbox, 0755); err != nil {
		return err
	}
	var toPtr, replyPtr *string
	if opts.To != "" {
		toCopy := opts.To
		toPtr = &toCopy
	}
	if opts.Reply != "" {
		replyCopy := opts.Reply
		replyPtr = &replyCopy
	}
	record := Outgoing{
		Agent: agent, To: toPtr, ReplyTo: replyPtr, Text: text,
		Mentions: opts.Mentions, MentionAll: opts.MentionAll,
	}
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

func NormalizeMention(value string) (string, error) {
	input := value
	value = strings.TrimSpace(value)
	if value == "" {
		return "", invalidMention(input)
	}
	if strings.HasPrefix(value, "@") && strings.Count(value, "@") == 1 {
		value = value[1:]
	}
	if strings.Contains(value, "@") {
		if strings.Count(value, "@") != 1 {
			return "", invalidMention(input)
		}
		user, domain, _ := strings.Cut(value, "@")
		if deviceMention(user) {
			return "", fmt.Errorf("invalid mention %q: device suffixes are not supported", input)
		}
		if !validMentionDigits(user) {
			return "", invalidMention(input)
		}
		domain = strings.ToLower(domain)
		switch domain {
		case "s.whatsapp.net", "lid":
			return user + "@" + domain, nil
		case "c.us":
			return user + "@s.whatsapp.net", nil
		default:
			return "", invalidMention(input)
		}
	}

	var digits strings.Builder
	plusSeen := false
	for _, char := range value {
		switch {
		case char >= '0' && char <= '9':
			digits.WriteRune(char)
		case char == '+' && !plusSeen && digits.Len() == 0:
			plusSeen = true
		case unicode.IsSpace(char) || char == '-' || char == '(' || char == ')':
			continue
		default:
			return "", invalidMention(input)
		}
	}
	number := digits.String()
	if !validMentionDigits(number) {
		return "", invalidMention(input)
	}
	return number + "@s.whatsapp.net", nil
}

func validMentionDigits(value string) bool {
	if len(value) < 7 || len(value) > 20 {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func deviceMention(user string) bool {
	if strings.Count(user, ":") != 1 {
		return false
	}
	number, suffix, _ := strings.Cut(user, ":")
	if number == "" || suffix == "" {
		return false
	}
	for _, char := range number + suffix {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func invalidMention(value string) error {
	return fmt.Errorf("invalid mention %q: expected a phone number or supported WhatsApp JID", value)
}

func Format(agent, text string) string {
	if !strings.HasPrefix(text, "["+agent+"]") {
		text = "[" + agent + "] " + text
	}
	return text
}

type Incoming struct {
	ChatJid    string `json:"chatJid"`
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
	// A chat is its JID: a group renamed later still shows its older rows
	// when asked for by either name.
	chats := map[string]bool{}
	for _, row := range all {
		if row.ChatJid != "" && strings.EqualFold(row.ChatName, who) {
			chats[row.ChatJid] = true
		}
	}
	var matched []Incoming
	for _, row := range all {
		if chats[row.ChatJid] || strings.EqualFold(row.ChatName, who) || strings.EqualFold(row.SenderName, who) {
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
	// Chats are keyed by JID and shown under their latest name, most
	// recently active last.
	type chat struct {
		name, at string
		last     int
	}
	seen := map[string]*chat{}
	for i, row := range all {
		key := row.ChatJid
		if key == "" {
			key = "name:" + fallback(row.ChatName, "?")
		}
		c := seen[key]
		if c == nil {
			c = &chat{}
			seen[key] = c
		}
		if row.ChatName != "" || c.name == "" {
			c.name = fallback(row.ChatName, "?")
		}
		c.at, c.last = clock(row.TS), i
	}
	list := make([]*chat, 0, len(seen))
	for _, c := range seen {
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].last < list[j].last })
	if len(list) > 20 {
		list = list[len(list)-20:]
	}
	result := make([]string, 0, len(list))
	for _, c := range list {
		result = append(result, fmt.Sprintf("%s %s", c.at, c.name))
	}
	return result, nil
}

func fallback(value, other string) string {
	if value == "" {
		return other
	}
	return value
}
