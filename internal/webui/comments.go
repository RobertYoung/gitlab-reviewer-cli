package webui

import (
	"fmt"
	"sync"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

// commentStore holds manual comments composed in the diff view, per MR,
// until they are published or folded into a review's stored record — the
// browser equivalent of the TUI diff screen's pending comment list.
type commentStore struct {
	mu    sync.Mutex
	seq   int
	byKey map[string][]review.Finding
}

func newCommentStore() *commentStore {
	return &commentStore{byKey: map[string][]review.Finding{}}
}

// add stores one manual comment and returns it with its session-unique ID.
// Comments arrive accepted, like the TUI composer's.
func (c *commentStore) add(key string, f review.Finding) review.Finding {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	f.ID = fmt.Sprintf("m%03d", c.seq)
	f.State = review.StateAccepted
	f.Manual = true
	c.byKey[key] = append(c.byKey[key], f)
	return f
}

// list returns a copy of the MR's pending comments.
func (c *commentStore) list(key string) []review.Finding {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]review.Finding(nil), c.byKey[key]...)
}

// accepted returns the MR's comments still awaiting publication.
func (c *commentStore) accepted(key string) []review.Finding {
	var out []review.Finding
	for _, f := range c.list(key) {
		if f.State == review.StateAccepted {
			out = append(out, f)
		}
	}
	return out
}

// setState records a publish outcome on one pending comment.
func (c *commentStore) setState(key, id string, state review.FindingState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	items := c.byKey[key]
	for i := range items {
		if items[i].ID == id {
			items[i].State = state
			return
		}
	}
}

// remove deletes one pending comment.
func (c *commentStore) remove(key, id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	items := c.byKey[key]
	for i := range items {
		if items[i].ID == id {
			c.byKey[key] = append(items[:i], items[i+1:]...)
			return
		}
	}
}

// take removes and returns the MR's accepted comments; used when a review
// run adopts them into its stored record.
func (c *commentStore) take(key string) []review.Finding {
	c.mu.Lock()
	defer c.mu.Unlock()
	items := c.byKey[key]
	var taken, kept []review.Finding
	for _, f := range items {
		if f.State == review.StateAccepted {
			taken = append(taken, f)
		} else {
			kept = append(kept, f)
		}
	}
	c.byKey[key] = kept
	return taken
}
