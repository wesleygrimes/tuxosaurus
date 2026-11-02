package rexclient

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrNotFound is returned when no session or thread matches a query.
var ErrNotFound = errors.New("not found")

// AmbiguousError lists the IDs that matched a query.
type AmbiguousError struct {
	What    string
	Query   string
	Matches []string
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("%s %q is ambiguous: %s", e.What, e.Query, strings.Join(e.Matches, ", "))
}

type candidate struct{ id, label string }

// match resolves query against candidates in order of precedence: exact ID,
// exact label, then unique ID prefix. IDs look like "block:<uuid>" on
// Tuxosaurus, so prefixes are also tried against the part after the colon.
func match(what, query string, cands []candidate) (int, error) {
	if query == "" {
		return -1, fmt.Errorf("%s: empty name: %w", what, ErrNotFound)
	}
	for i, c := range cands {
		if c.id == query {
			return i, nil
		}
	}
	var hits []int
	for i, c := range cands {
		if c.label == query {
			hits = append(hits, i)
		}
	}
	if len(hits) == 0 {
		for i, c := range cands {
			_, bare, _ := strings.Cut(c.id, ":")
			if strings.HasPrefix(c.id, query) || (bare != "" && strings.HasPrefix(bare, query)) {
				hits = append(hits, i)
			}
		}
	}
	switch len(hits) {
	case 0:
		return -1, fmt.Errorf("%s %q: %w", what, query, ErrNotFound)
	case 1:
		return hits[0], nil
	}
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = cands[h].id
	}
	return -1, &AmbiguousError{What: what, Query: query, Matches: ids}
}

// MatchSession picks one session by ID, label or unique ID prefix.
func MatchSession(sessions []Session, query string) (Session, error) {
	cands := make([]candidate, len(sessions))
	for i, s := range sessions {
		cands[i] = candidate{s.ID, s.Label}
	}
	i, err := match("session", query, cands)
	if err != nil {
		return Session{}, err
	}
	return sessions[i], nil
}

// Thread is a terminal block together with its session.
type Thread struct {
	Session Session `json:"session"`
	Block   Block   `json:"block"`
}

// MatchThread picks one thread by block ID, label or unique ID prefix.
func MatchThread(threads []Thread, query string) (Thread, error) {
	cands := make([]candidate, len(threads))
	for i, t := range threads {
		cands[i] = candidate{t.Block.ID, t.Block.Label}
	}
	i, err := match("thread", query, cands)
	if err != nil {
		return Thread{}, err
	}
	return threads[i], nil
}

// Threads lists terminal blocks in the given sessions, attaching to each.
func (c *Client) Threads(ctx context.Context, sessions []Session) ([]Thread, error) {
	var out []Thread
	for _, s := range sessions {
		if err := c.Attach(ctx, s.ID); err != nil {
			if NotFound(err) {
				continue // destroyed while listing
			}
			return nil, err
		}
		blocks, err := c.ListBlocks(ctx, s.ID)
		if err != nil {
			return nil, err
		}
		for _, b := range blocks {
			if b.Flavor != "" && b.Flavor != ShellFlavor {
				continue
			}
			out = append(out, Thread{Session: s, Block: b})
		}
	}
	return out, nil
}

// FindSession resolves a session query against the live session list.
func (c *Client) FindSession(ctx context.Context, query string) (Session, error) {
	sessions, err := c.ListSessions(ctx)
	if err != nil {
		return Session{}, err
	}
	return MatchSession(sessions, query)
}

// FindThread resolves a thread query, optionally within one session, and
// leaves the client attached to that thread's session.
func (c *Client) FindThread(ctx context.Context, sessionQuery, query string) (Thread, error) {
	sessions, err := c.ListSessions(ctx)
	if err != nil {
		return Thread{}, err
	}
	if sessionQuery != "" {
		s, err := MatchSession(sessions, sessionQuery)
		if err != nil {
			return Thread{}, err
		}
		sessions = []Session{s}
	}
	threads, err := c.Threads(ctx, sessions)
	if err != nil {
		return Thread{}, err
	}
	t, err := MatchThread(threads, query)
	if err != nil {
		return Thread{}, err
	}
	return t, c.Attach(ctx, t.Session.ID)
}
