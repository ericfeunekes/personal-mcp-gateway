package fsx

import "sync"

// ActivityCounter is an instance-local aggregate observation seam used only by
// the private candidate resource probe. It retains no path, operation, timing,
// or content values. A nil counter keeps the normal runtime free of observers.
type ActivityCounter struct {
	mu     sync.Mutex
	total  uint64
	active uint64
}

// ActivitySnapshot contains only aggregate lifecycle counts. Total is the
// number of operations begun and Active is the number not yet returned.
type ActivitySnapshot struct {
	Total  uint64
	Active uint64
}

// Begin records one complete vault operation. A nil counter returns nil so the
// normal runtime can conditionally skip the defer and all observation work.
func (c *ActivityCounter) Begin() func() {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	c.active++
	c.total++
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		c.active--
		c.mu.Unlock()
	}
}

// Snapshot returns aggregate operation totals and in-flight work for this
// counter's one Vault instance.
func (c *ActivityCounter) Snapshot() ActivitySnapshot {
	if c == nil {
		return ActivitySnapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return ActivitySnapshot{Total: c.total, Active: c.active}
}
