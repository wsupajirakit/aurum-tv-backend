package hmacsig

import (
	"container/list"
	"sync"
	"time"
)

// NonceCache remembers nonces until their timestamps can no longer be accepted, so a captured
// request cannot be replayed. Entries are kept in insertion order; the oldest are evicted first,
// and the total size is capped.
type NonceCache struct {
	mu      sync.Mutex
	max     int
	order   *list.List // of *nonceEntry, oldest first
	entries map[string]*list.Element
}

type nonceEntry struct {
	nonce     string
	expiresAt time.Time
}

func NewNonceCache(max int) *NonceCache {
	return &NonceCache{max: max, order: list.New(), entries: make(map[string]*list.Element)}
}

// Remember stores nonce until now+ttl. It returns false when the nonce is still remembered (a replay).
func (c *NonceCache) Remember(nonce string, now time.Time, ttl time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for e := c.order.Front(); e != nil; e = c.order.Front() {
		entry := e.Value.(*nonceEntry)
		if entry.expiresAt.After(now) {
			break
		}
		c.order.Remove(e)
		delete(c.entries, entry.nonce)
	}
	if _, seen := c.entries[nonce]; seen {
		return false
	}
	c.entries[nonce] = c.order.PushBack(&nonceEntry{nonce: nonce, expiresAt: now.Add(ttl)})
	if c.order.Len() > c.max {
		oldest := c.order.Front()
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*nonceEntry).nonce)
	}
	return true
}

func (c *NonceCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
