package fsx

import "sync"

// SchedulerActivity is a private aggregate observer for bounded concurrent
// schedulers. It carries no path, content, timing, or request identity.
// Normal runtime passes nil and performs no observation work.
type SchedulerActivity struct {
	mu       sync.Mutex
	total    uint64
	active   uint64
	inflight uint64
}

type SchedulerSnapshot struct {
	Total    uint64
	Active   uint64
	InFlight uint64
}

func (c *SchedulerActivity) BeginScan() func() {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	c.total++
	c.active++
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		c.active--
		c.mu.Unlock()
	}
}

func (c *SchedulerActivity) Reserve() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.inflight++
	c.mu.Unlock()
}

func (c *SchedulerActivity) Release() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.inflight--
	c.mu.Unlock()
}

func (c *SchedulerActivity) Snapshot() SchedulerSnapshot {
	if c == nil {
		return SchedulerSnapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return SchedulerSnapshot{Total: c.total, Active: c.active, InFlight: c.inflight}
}
