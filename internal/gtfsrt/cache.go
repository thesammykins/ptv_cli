package gtfsrt

import (
	"context"
	"sync"
)

// InvocationCache deduplicates same-feed snapshots for one command process.
// It never persists a realtime snapshot across invocations.
type InvocationCache struct {
	mu        sync.Mutex
	snapshots map[string]*Snapshot
	inFlight  map[string]*cacheFetch
}

type cacheFetch struct {
	done chan struct{}
	snap *Snapshot
	err  error
}

func NewInvocationCache() *InvocationCache {
	return &InvocationCache{snapshots: make(map[string]*Snapshot), inFlight: make(map[string]*cacheFetch)}
}

func (c *InvocationCache) GetOrFetch(ctx context.Context, client *Client, feed Feed) (*Snapshot, error) {
	if c == nil {
		c = NewInvocationCache()
	}
	c.mu.Lock()
	if snapshot, ok := c.snapshots[feed.ID]; ok {
		c.mu.Unlock()
		return snapshot, nil
	}
	if fetch, ok := c.inFlight[feed.ID]; ok {
		c.mu.Unlock()
		select {
		case <-fetch.done:
			return fetch.snap, fetch.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	fetch := &cacheFetch{done: make(chan struct{})}
	c.inFlight[feed.ID] = fetch
	c.mu.Unlock()
	snapshot, err := client.FetchSnapshot(ctx, feed)
	c.mu.Lock()
	fetch.snap, fetch.err = snapshot, err
	if err == nil {
		c.snapshots[feed.ID] = snapshot
	}
	delete(c.inFlight, feed.ID)
	close(fetch.done)
	c.mu.Unlock()
	return snapshot, err
}
