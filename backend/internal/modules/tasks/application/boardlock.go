package application

import (
	"context"
	"sync"
)

// boardLocks serializes the rank writes of one Board inside this process, before a database transaction (and its
// connection) is taken. Requests of the same Board wait here without holding a connection, so a burst of rank
// writes cannot exhaust the connection pool while the one that holds the Board's advisory lock needs another
// connection. The advisory lock in the store still serializes writers across processes.
type boardLocks struct {
	mu      sync.Mutex
	entries map[string]*boardLock
}

type boardLock struct {
	ch   chan struct{}
	refs int
}

func (l *boardLocks) acquire(ctx context.Context, key string) (release func(), err error) {
	l.mu.Lock()
	if l.entries == nil {
		l.entries = map[string]*boardLock{}
	}
	e := l.entries[key]
	if e == nil {
		e = &boardLock{ch: make(chan struct{}, 1)}
		l.entries[key] = e
	}
	e.refs++
	l.mu.Unlock()
	drop := func() {
		l.mu.Lock()
		e.refs--
		if e.refs == 0 {
			delete(l.entries, key)
		}
		l.mu.Unlock()
	}
	select {
	case e.ch <- struct{}{}:
		return func() { <-e.ch; drop() }, nil
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
}
