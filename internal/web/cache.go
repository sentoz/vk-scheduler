package web

import (
	"context"
	"sync"
	"time"

	"github.com/sentoz/vk-scheduler/internal/config"
	"github.com/sentoz/vk-scheduler/internal/resource"
)

type status struct {
	state        string
	transitional bool
	failure      string
}
type cacheEntry struct {
	result  status
	expires time.Time
}
type flight struct {
	done   chan struct{}
	result status
}
type statusCache struct {
	mu         sync.Mutex
	generation uint64
	entries    map[string]cacheEntry
	flights    map[cacheKey]*flight
	ctx        context.Context
	timeout    time.Duration
	checker    resource.StateChecker
	workers    sync.WaitGroup
}
type cacheKey struct {
	generation uint64
	key        string
}

func (c *statusCache) get(ctx context.Context, generation uint64, target config.Resource) status {
	if ctx.Err() != nil {
		return status{state: "unknown", failure: "Status request cancelled"}
	}
	c.mu.Lock()
	if ctx.Err() != nil || c.ctx.Err() != nil {
		c.mu.Unlock()
		return status{state: "unknown", failure: "Status request cancelled"}
	}
	if generation > c.generation {
		c.generation = generation
		c.entries = map[string]cacheEntry{}
	}
	if item, ok := c.entries[target.Key()]; generation == c.generation && ok && time.Now().Before(item.expires) {
		c.mu.Unlock()
		return item.result
	}
	key := cacheKey{generation, target.Key()}
	f := c.flights[key]
	if f == nil {
		f = &flight{done: make(chan struct{})}
		c.flights[key] = f
		c.workers.Add(1)
		go func() {
			defer c.workers.Done()
			read, cancel := context.WithTimeout(c.ctx, c.timeout)
			defer cancel()
			state, transitional, err := c.checker.GetState(read, target)
			result := status{state: state, transitional: transitional}
			if err != nil {
				result = status{state: "unknown", failure: "Cloud status unavailable"}
			} else if transitional {
				result.state = "transitional"
			} else if state != "running" && state != "stopped" {
				result = status{state: "unknown", failure: "Cloud status unavailable"}
			}
			c.mu.Lock()
			f.result = result
			if generation == c.generation {
				c.entries[target.Key()] = cacheEntry{result, time.Now().Add(30 * time.Second)}
			}
			delete(c.flights, key)
			close(f.done)
			c.mu.Unlock()
		}()
	}
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		return status{state: "unknown", failure: "Status request cancelled"}
	case <-f.done:
		return f.result
	}
}
