// Package responsecache stores opt-in exact responses in memory.
package responsecache

import (
	"sync"
	"time"
)

const (
	DefaultMaxBytes = 64 << 20
	DefaultMaxEntry = 1 << 20
	DefaultTTL      = 5 * time.Minute
)

// Cache is a memory-only exact response store.
type Cache struct {
	mu       sync.Mutex
	maxBytes int
	maxEntry int
	ttl      time.Duration
	used     int
	items    map[string]item
	order    []string
}

type item struct {
	body    []byte
	expires time.Time
}

// New builds a cache. Non-positive limits use the defaults.
func New(maxBytes, maxEntry int, ttl time.Duration) *Cache {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if maxEntry <= 0 {
		maxEntry = DefaultMaxEntry
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Cache{maxBytes: maxBytes, maxEntry: maxEntry, ttl: ttl, items: map[string]item{}}
}

// Get returns a copy of a live entry.
func (c *Cache) Get(key string, now time.Time) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[key]
	if !ok || !now.Before(it.expires) {
		if ok {
			c.deleteLocked(key)
		}
		return nil, false
	}
	return append([]byte(nil), it.body...), true
}

// Put stores an immutable copy when it fits. Oversized entries are refused.
func (c *Cache) Put(key string, body []byte, now time.Time) bool {
	if c == nil || key == "" || len(body) == 0 || len(body) > c.maxEntry {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if prev, ok := c.items[key]; ok {
		c.used -= len(prev.body)
		delete(c.items, key)
	}
	for c.used+len(body) > c.maxBytes && len(c.order) > 0 {
		c.deleteLocked(c.order[0])
	}
	if c.used+len(body) > c.maxBytes {
		return false
	}
	copied := append([]byte(nil), body...)
	c.items[key] = item{body: copied, expires: now.Add(c.ttl)}
	c.order = append(c.order, key)
	c.used += len(copied)
	return true
}

// InvalidatePrefix drops every key with the prefix.
func (c *Cache) InvalidatePrefix(prefix string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.items {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			c.deleteLocked(key)
		}
	}
}

func (c *Cache) deleteLocked(key string) {
	it, ok := c.items[key]
	if !ok {
		return
	}
	c.used -= len(it.body)
	delete(c.items, key)
	for i, id := range c.order {
		if id == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}
