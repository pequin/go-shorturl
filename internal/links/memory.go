// Package links holds the short link model and an in-memory store for it.
package links

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	// ErrNotFound means no link is stored under the requested code.
	ErrNotFound = errors.New("link not found")
	// ErrCodeTaken means another link already uses the code.
	ErrCodeTaken = errors.New("code already in use")
)

// Link maps a short code to its target URL.
type Link struct {
	Code      string
	URL       string
	CreatedAt time.Time
	Visits    int64
}

// Memory keeps links in a map guarded by a mutex. Everything is lost on
// restart, which is acceptable for a demo and for tests.
type Memory struct {
	mu    sync.RWMutex
	links map[string]Link
}

// NewMemory returns an empty store.
func NewMemory() *Memory {
	return &Memory{links: make(map[string]Link)}
}

// Create stores l and fails with ErrCodeTaken if l.Code is already used.
// The context is accepted to satisfy the store contract; map access does not block.
func (m *Memory) Create(_ context.Context, l Link) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.links[l.Code]; ok {
		return fmt.Errorf("create %q: %w", l.Code, ErrCodeTaken)
	}
	m.links[l.Code] = l
	return nil
}

// Resolve returns the link for code and counts the visit.
func (m *Memory) Resolve(_ context.Context, code string) (Link, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.links[code]
	if !ok {
		return Link{}, fmt.Errorf("resolve %q: %w", code, ErrNotFound)
	}
	l.Visits++
	m.links[code] = l
	return l, nil
}

// Link returns the link for code without counting a visit.
func (m *Memory) Link(_ context.Context, code string) (Link, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	l, ok := m.links[code]
	if !ok {
		return Link{}, fmt.Errorf("lookup %q: %w", code, ErrNotFound)
	}
	return l, nil
}
