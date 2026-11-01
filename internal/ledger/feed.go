package ledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The feed is a pull-based, cursor-addressed view of a project's event
// log. Its guarantees:
//
//   - Order: events are totally ordered by seq within a project; a feed
//     returns them in that order.
//   - Cursor: <epoch>:<seq> names the last event a reader has consumed.
//     A cursor from another incarnation of the project (different epoch)
//     or beyond the head is ErrCursorInvalid; one older than the retained
//     floor is ErrCursorExpired. Either way the reader resyncs from the
//     brief, which carries the current cursor.
//   - Replay: reading again from the same cursor returns the same events
//     (until retention drops them). Delivery to the reader is therefore
//     at-least-once; readers deduplicate by seq.
//   - Filtering: the returned next cursor advances past events the filter
//     skipped, so a narrow filter never rescans the same history.
//   - Bounds: at most Limit events (default 50, max 500) and about
//     maxFeedBytes of JSON per call; More reports that the reader should
//     call again.
//   - Retention: the log keeps at most Store.MaxEvents events; older ones
//     survive only in the snapshot.
//
// Nothing is pushed: no member is interrupted by another's state write.

const (
	defaultFeedLimit = 50
	maxFeedLimit     = 500
	maxFeedBytes     = 256 << 10
)

// FeedFilter narrows a feed. Empty fields match everything.
type FeedFilter struct {
	Kinds []string `json:"kinds,omitempty"` // project, task, finding
	IDs   []string `json:"ids,omitempty"`
	// Involving matches events whose actor, owner, task creator, named
	// verifier, finding author or recovered-from owner is this label.
	Involving string `json:"involving,omitempty"`
}

// FeedQuery is one feed read.
type FeedQuery struct {
	After  string     `json:"after,omitempty"`
	Limit  int        `json:"limit,omitempty"`
	Filter FeedFilter `json:"filter,omitempty"`
	// Summary drops entity bodies from events, keeping seq, op, id,
	// owner, state and note.
	Summary bool `json:"summary,omitempty"`
}

// FeedPage is one feed read's result.
type FeedPage struct {
	Events []Event `json:"events"`
	// Next is the cursor to pass as After next time. Store it only after
	// the events are processed.
	Next string `json:"next"`
	// Head is the project's latest cursor.
	Head string `json:"head"`
	More bool   `json:"more"`
	// Floor is the oldest cursor still served.
	Floor string `json:"floor"`
}

func (f FeedFilter) match(ev Event) bool {
	if len(f.Kinds) > 0 && !contains(f.Kinds, ev.Kind) {
		return false
	}
	if len(f.IDs) > 0 && !contains(f.IDs, ev.ID) {
		return false
	}
	if f.Involving != "" {
		who := f.Involving
		hit := ev.Actor == who || ev.Owner == who || strings.HasPrefix(ev.Note, "from "+who+":")
		if ev.Task != nil {
			hit = hit || ev.Task.CreatedBy == who || ev.Task.Verifier == who
		}
		if ev.Finding != nil {
			hit = hit || ev.Finding.Author == who
		}
		if !hit {
			return false
		}
	}
	return true
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func validFilter(f FeedFilter) error {
	for _, k := range f.Kinds {
		if k != "project" && k != "task" && k != "finding" {
			return invalid("feed kind must be project, task or finding, not %q", k)
		}
	}
	if len(f.IDs) > 100 {
		return invalid("at most 100 ids in a filter")
	}
	return nil
}

// Feed reads events after a cursor. An empty After starts at the oldest
// retained event.
func (s *Store) Feed(project string, q FeedQuery) (FeedPage, error) {
	if err := validFilter(q.Filter); err != nil {
		return FeedPage{}, err
	}
	var out FeedPage
	err := s.read(project, func(st *State) error {
		var err error
		out, err = s.feed(st, project, q)
		return err
	})
	return out, err
}

func (s *Store) feed(st *State, project string, q FeedQuery) (FeedPage, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultFeedLimit
	}
	if limit > maxFeedLimit {
		limit = maxFeedLimit
	}
	epoch := st.Project.Epoch
	floor := st.FirstSeq - 1
	if floor < 0 {
		floor = 0
	}
	after := floor
	if q.After != "" {
		e, seq, err := parseCursor(q.After)
		if err != nil {
			return FeedPage{}, err
		}
		if e != epoch {
			return FeedPage{}, fmt.Errorf("%w: cursor is from another incarnation of project %s; resync from bp work brief", ErrCursorInvalid, project)
		}
		if seq > st.Seq {
			return FeedPage{}, fmt.Errorf("%w: cursor %d is beyond the head %d", ErrCursorInvalid, seq, st.Seq)
		}
		if seq < floor {
			return FeedPage{}, fmt.Errorf("%w: events after %d were dropped by retention (oldest kept is %d); resync from bp work brief", ErrCursorExpired, seq, st.FirstSeq)
		}
		after = seq
	}
	page := FeedPage{Events: []Event{}, Head: formatCursor(epoch, st.Seq), Floor: formatCursor(epoch, floor)}
	next := after
	bytes := 0
	full := false
	sc, err := scanLog(s.logPath(project))
	if err != nil {
		return FeedPage{}, err
	}
	for _, ev := range sc.events {
		if ev.Seq <= after || ev.Seq > st.Seq {
			continue
		}
		if full {
			page.More = true
			break
		}
		if q.Filter.match(ev) {
			if q.Summary {
				ev.Project, ev.Task, ev.Finding = nil, nil, nil
			}
			size := eventSize(ev)
			if len(page.Events) > 0 && bytes+size > maxFeedBytes {
				page.More = true
				break
			}
			page.Events = append(page.Events, ev)
			bytes += size
			if len(page.Events) >= limit {
				full = true
			}
		}
		next = ev.Seq
	}
	page.Next = formatCursor(epoch, next)
	return page, nil
}

func eventSize(ev Event) int {
	data, _ := json.Marshal(ev)
	return len(data)
}

// Subscription is a caller's stored cursor and filter, so a restarted
// agent resumes where it acknowledged without keeping state itself.
type Subscription struct {
	Cursor    string     `json:"cursor"`
	Filter    FeedFilter `json:"filter"`
	UpdatedAt string     `json:"updatedAt"`
}

func (s *Store) subsPath(project string) string {
	return filepath.Join(s.projectDir(project), "subs.json")
}

func (s *Store) loadSubs(project string) (map[string]Subscription, error) {
	subs := map[string]Subscription{}
	data, err := os.ReadFile(s.subsPath(project))
	if errors.Is(err, os.ErrNotExist) {
		return subs, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &subs); err != nil {
		return nil, fmt.Errorf("%s: %w", s.subsPath(project), err)
	}
	return subs, nil
}

// Subscribe stores a filter and a starting cursor for actor. An empty
// from starts at the current head (only new changes); "start" starts at
// the oldest retained event.
func (s *Store) Subscribe(actor Actor, project string, filter FeedFilter, from string) (Subscription, error) {
	if err := validActor(actor); err != nil {
		return Subscription{}, err
	}
	if err := validFilter(filter); err != nil {
		return Subscription{}, err
	}
	var out Subscription
	err := s.lock(project, func() error {
		st, err := s.load(project)
		if err != nil {
			return err
		}
		subs, err := s.loadSubs(project)
		if err != nil {
			return err
		}
		cursor := formatCursor(st.Project.Epoch, st.Seq)
		switch from {
		case "":
		case "start":
			floor := st.FirstSeq - 1
			if floor < 0 {
				floor = 0
			}
			cursor = formatCursor(st.Project.Epoch, floor)
		default:
			if _, err := s.feed(st, project, FeedQuery{After: from, Limit: 1}); err != nil {
				return err
			}
			cursor = from
		}
		if len(subs) >= 1000 {
			if _, ok := subs[actor.Label]; !ok {
				return invalid("project %s has 1000 subscriptions, the limit", project)
			}
		}
		out = Subscription{Cursor: cursor, Filter: filter, UpdatedAt: s.stamp()}
		subs[actor.Label] = out
		return writeJSON(s.subsPath(project), subs)
	})
	return out, err
}

// FeedSubscribed reads from actor's stored cursor with its stored filter.
// It does not move the cursor: call Ack with the page's Next once the
// events are handled, so a crash in between replays them.
func (s *Store) FeedSubscribed(actor Actor, project string, limit int, summary bool) (FeedPage, error) {
	var out FeedPage
	err := s.read(project, func(st *State) error {
		subs, err := s.loadSubs(project)
		if err != nil {
			return err
		}
		sub, ok := subs[actor.Label]
		if !ok {
			return fmt.Errorf("%w: %s has no subscription to %s (bp work feed %s --subscribe)", ErrNotFound, actor.Label, project, project)
		}
		out, err = s.feed(st, project, FeedQuery{After: sub.Cursor, Limit: limit, Filter: sub.Filter, Summary: summary})
		return err
	})
	return out, err
}

// Ack moves actor's stored cursor forward. A cursor behind the stored one
// is refused (use Subscribe with from to rewind on purpose).
func (s *Store) Ack(actor Actor, project, cursor string) (Subscription, error) {
	var out Subscription
	err := s.lock(project, func() error {
		st, err := s.load(project)
		if err != nil {
			return err
		}
		subs, err := s.loadSubs(project)
		if err != nil {
			return err
		}
		sub, ok := subs[actor.Label]
		if !ok {
			return fmt.Errorf("%w: %s has no subscription to %s", ErrNotFound, actor.Label, project)
		}
		epoch, seq, err := parseCursor(cursor)
		if err != nil {
			return err
		}
		if epoch != st.Project.Epoch || seq > st.Seq {
			return fmt.Errorf("%w: cursor %s does not belong to the current project history", ErrCursorInvalid, cursor)
		}
		if _, cur, err := parseCursor(sub.Cursor); err == nil && seq < cur {
			return fmt.Errorf("%w: cursor %d is behind the acknowledged %d", ErrConflict, seq, cur)
		}
		sub.Cursor, sub.UpdatedAt = cursor, s.now().Format(time.RFC3339Nano)
		subs[actor.Label] = sub
		out = sub
		return writeJSON(s.subsPath(project), subs)
	})
	return out, err
}
