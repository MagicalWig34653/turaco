package storage

import (
	"bytes"
	"context"
	"io"
	"sync"
)

// memBackend is an in-memory Backend for the Vault tests.
type memBackend struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newMem() *memBackend { return &memBackend{m: map[string][]byte{}} }

func (b *memBackend) Put(_ context.Context, id string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.m[id] = data
	return nil
}

func (b *memBackend) Open(_ context.Context, id string) (io.ReadCloser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	d, ok := b.m[id]
	if !ok {
		return nil, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), d...))), nil
}

func (b *memBackend) Delete(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.m, id)
	return nil
}

func (b *memBackend) Check(context.Context) error { return nil }
func (b *memBackend) Driver() string              { return "memory" }
