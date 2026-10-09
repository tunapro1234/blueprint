package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"blueprint/internal/audit"
	"blueprint/internal/identity"
)

// Rooms are named pub/sub channels. A post is stored in the room's history
// and then sent to every other member through Send, so it travels the same
// queue (or inbox) as a direct message and never interrupts a busy agent.

// Room is a room's membership record.
type Room struct {
	Name      string   `json:"name"`
	Topic     string   `json:"topic,omitempty"`
	CreatedBy string   `json:"createdBy,omitempty"`
	CreatedAt string   `json:"createdAt"`
	Members   []string `json:"members"`
}

// RoomPost is one entry of a room's history.
type RoomPost struct {
	ID        string  `json:"id"`
	Room      string  `json:"room"`
	From      string  `json:"from"`
	Author    string  `json:"author"` // caller name, for membership checks
	Text      string  `json:"text"`
	TS        float64 `json:"ts"`
	Untrusted bool    `json:"untrusted,omitempty"`
}

// PostResult is a stored post and its per-member deliveries.
type PostResult struct {
	Post       RoomPost     `json:"post"`
	Deliveries []SendResult `json:"deliveries"`
	Errors     []string     `json:"errors,omitempty"`
}

// maxRoomMembers keeps one post from fanning out without bound.
const maxRoomMembers = 64

func (c *Core) roomPath(room string) string {
	return filepath.Join(c.dir(), "rooms", room+".json")
}
func (c *Core) roomHistory(room string) string {
	return filepath.Join(c.dir(), "rooms", room+".jsonl")
}

func validRoom(name string) error {
	if !identity.ValidName(name) || len(name) > 64 {
		return invalid("%q is not a valid room name", name)
	}
	return nil
}

func (c *Core) loadRoom(name string) (Room, bool, error) {
	var room Room
	if err := readJSON(c.roomPath(name), &room); err != nil {
		return Room{}, false, err
	}
	return room, room.Name != "", nil
}

// Rooms lists every room, sorted by name.
func (c *Core) Rooms() ([]Room, error) {
	entries, err := os.ReadDir(filepath.Join(c.dir(), "rooms"))
	if os.IsNotExist(err) {
		return []Room{}, nil
	}
	if err != nil {
		return nil, err
	}
	rooms := []Room{}
	for _, entry := range entries {
		name, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || validRoom(name) != nil {
			continue
		}
		if room, found, err := c.loadRoom(name); err == nil && found {
			rooms = append(rooms, room)
		}
	}
	sort.Slice(rooms, func(i, j int) bool { return rooms[i].Name < rooms[j].Name })
	return rooms, nil
}

// Room returns one room.
func (c *Core) Room(name string) (Room, error) {
	if err := validRoom(name); err != nil {
		return Room{}, err
	}
	room, found, err := c.loadRoom(name)
	if err != nil {
		return Room{}, err
	}
	if !found {
		return Room{}, fmt.Errorf("%w: room %s", ErrNotFound, name)
	}
	return room, nil
}

// Join adds agents to a room, creating it on first join. With no agents
// listed the caller joins. Every member must be an addressable agent.
func (c *Core) Join(ctx context.Context, caller Caller, name, topic string, agents []string) (Room, error) {
	room, err := c.join(ctx, caller, name, topic, agents)
	c.auditResult(caller, "room.join", name, strings.Join(agents, ","), err)
	return room, err
}

func (c *Core) join(ctx context.Context, caller Caller, name, topic string, agents []string) (Room, error) {
	if err := ValidateCaller(caller); err != nil {
		return Room{}, err
	}
	if err := validRoom(name); err != nil {
		return Room{}, err
	}
	if len(topic) > 500 || validateTextAllowEmpty(topic) != nil {
		return Room{}, invalid("topic must be at most 500 printable characters")
	}
	if len(agents) == 0 {
		agents = []string{caller.Name}
	}
	for _, agent := range agents {
		if _, err := c.Lookup(ctx, agent); err != nil {
			return Room{}, err
		}
	}
	var out Room
	err := withLock(c.roomPath(name), func() error {
		room, found, err := c.loadRoom(name)
		if err != nil {
			return err
		}
		if !found {
			room = Room{Name: name, CreatedBy: caller.Label(), CreatedAt: c.now().UTC().Format(time.RFC3339), Members: []string{}}
		} else if !contains(room.Members, caller.Name) && !(len(agents) == 1 && agents[0] == caller.Name) {
			// Anyone may join an existing room; only members may add others.
			return fmt.Errorf("%w: join %s yourself before adding others", ErrForbidden, name)
		}
		if topic != "" {
			room.Topic = topic
		}
		for _, agent := range agents {
			if !contains(room.Members, agent) {
				room.Members = append(room.Members, agent)
			}
		}
		if len(room.Members) > maxRoomMembers {
			return invalid("room %s would have %d members; the limit is %d", name, len(room.Members), maxRoomMembers)
		}
		sort.Strings(room.Members)
		out = room
		return writeJSON(c.roomPath(name), room)
	})
	return out, err
}

// Leave removes an agent (default: the caller) from a room. A member may
// remove itself; removing another member needs membership.
func (c *Core) Leave(caller Caller, name, agent string) (Room, error) {
	if agent == "" {
		agent = caller.Name
	}
	var out Room
	err := func() error {
		if err := ValidateCaller(caller); err != nil {
			return err
		}
		if err := validRoom(name); err != nil {
			return err
		}
		return withLock(c.roomPath(name), func() error {
			room, found, err := c.loadRoom(name)
			if err != nil {
				return err
			}
			if !found || !contains(room.Members, agent) {
				return fmt.Errorf("%w: %s is not in room %s", ErrNotFound, agent, name)
			}
			if !contains(room.Members, caller.Name) {
				return fmt.Errorf("%w: only members change room %s", ErrForbidden, name)
			}
			members := room.Members[:0]
			for _, member := range room.Members {
				if member != agent {
					members = append(members, member)
				}
			}
			room.Members = members
			out = room
			return writeJSON(c.roomPath(name), room)
		})
	}()
	c.auditResult(caller, "room.leave", name, agent, err)
	return out, err
}

// Post stores a message in the room and sends it to every other member.
func (c *Core) Post(ctx context.Context, caller Caller, name, text string) (PostResult, error) {
	if err := ValidateCaller(caller); err != nil {
		return PostResult{}, err
	}
	if err := validRoom(name); err != nil {
		return PostResult{}, err
	}
	if err := validateText(text); err != nil {
		return PostResult{}, err
	}
	room, err := c.Room(name)
	if err != nil {
		return PostResult{}, err
	}
	if !contains(room.Members, caller.Name) {
		c.auditResult(caller, "room.post", name, "", fmt.Errorf("%w: not a member", ErrForbidden))
		return PostResult{}, fmt.Errorf("%w: join room %s before posting", ErrForbidden, name)
	}
	post := RoomPost{ID: randomID("rp"), Room: name, From: caller.Label(), Author: caller.Name,
		Text: text, TS: float64(c.now().UnixNano()) / 1e9, Untrusted: caller.Remote}
	history := c.roomHistory(name)
	if err := withLock(history, func() error { return appendJSONL(history, post) }); err != nil {
		return PostResult{}, err
	}
	c.audit(audit.Event{Kind: "api.room.post.accepted", Actor: caller.Label(), Target: name, ID: post.ID, Fields: map[string]string{"transport": caller.Transport}})
	result := PostResult{Post: post, Deliveries: []SendResult{}}
	// Each member gets the raw post from the original caller, so a remote
	// author's post stays untrusted on every delivery route.
	for _, member := range room.Members {
		if member == caller.Name {
			continue
		}
		sent, err := c.Send(ctx, caller, SendRequest{To: member, Text: post.Text, MessageID: post.ID, Room: name})
		if err != nil {
			result.Errors = append(result.Errors, member+": "+err.Error())
			continue
		}
		result.Deliveries = append(result.Deliveries, sent)
	}
	return result, nil
}

// RoomRead returns posts after the given post id (or the latest ones), oldest
// first. Only members read a room.
func (c *Core) RoomRead(caller Caller, name, after string, limit int) (Room, []RoomPost, error) {
	if err := ValidateCaller(caller); err != nil {
		return Room{}, nil, err
	}
	room, err := c.Room(name)
	if err != nil {
		return Room{}, nil, err
	}
	if !contains(room.Members, caller.Name) {
		return Room{}, nil, fmt.Errorf("%w: join room %s before reading it", ErrForbidden, name)
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	posts := []RoomPost{}
	seen := after == ""
	err = readJSONL(c.roomHistory(name), func(post RoomPost) bool {
		if !seen {
			seen = post.ID == after
			return true
		}
		posts = append(posts, post)
		return true
	})
	if err != nil {
		return Room{}, nil, err
	}
	if !seen {
		return Room{}, nil, fmt.Errorf("%w: post %s in room %s", ErrNotFound, after, name)
	}
	if after == "" && len(posts) > limit {
		posts = posts[len(posts)-limit:]
	} else if len(posts) > limit {
		posts = posts[:limit]
	}
	for i := range posts {
		posts[i].Text = c.render(posts[i].Untrusted, posts[i].From, posts[i].Text)
	}
	return room, posts, nil
}

func (c *Core) auditResult(caller Caller, kind, target, detail string, err error) {
	decision := "accepted"
	if err != nil {
		decision = "rejected"
		detail = strings.TrimSpace(detail + " " + err.Error())
	}
	c.audit(audit.Event{Kind: "api." + kind + "." + decision, Severity: severity(decision), Actor: caller.Label(), Target: target, Reason: detail, Fields: map[string]string{"transport": caller.Transport}})
}

func validateTextAllowEmpty(text string) error {
	if text == "" {
		return nil
	}
	return validateText(text)
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
