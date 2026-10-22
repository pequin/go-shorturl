package links

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestMemoryCreateAndLookup(t *testing.T) {
	m := NewMemory()
	ctx := t.Context()

	if err := m.Create(ctx, Link{Code: "abc", URL: "https://example.com"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	err := m.Create(ctx, Link{Code: "abc", URL: "https://other.example"})
	if !errors.Is(err, ErrCodeTaken) {
		t.Fatalf("second Create(abc) error = %v, want ErrCodeTaken", err)
	}

	got, err := m.Link(ctx, "abc")
	if err != nil {
		t.Fatalf("Link(abc): %v", err)
	}
	if got.URL != "https://example.com" {
		t.Errorf("Link(abc).URL = %q, want the first URL to survive", got.URL)
	}
}

func TestMemoryUnknownCode(t *testing.T) {
	m := NewMemory()
	if _, err := m.Link(t.Context(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Link(nope) error = %v, want ErrNotFound", err)
	}
	if _, err := m.Resolve(t.Context(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve(nope) error = %v, want ErrNotFound", err)
	}
}

func TestMemoryResolveCountsVisits(t *testing.T) {
	m := NewMemory()
	ctx := t.Context()
	if err := m.Create(ctx, Link{Code: "x", URL: "https://example.com"}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := m.Resolve(ctx, "x"); err != nil {
			t.Fatal(err)
		}
	}
	l, err := m.Link(ctx, "x")
	if err != nil {
		t.Fatal(err)
	}
	if l.Visits != 3 {
		t.Errorf("Visits = %d after 3 resolves, want 3", l.Visits)
	}
}

// Run with -race: these tests are mostly about the detector seeing no
// unsynchronized access, the counts just prove no update was lost.
func TestMemoryConcurrentResolve(t *testing.T) {
	m := NewMemory()
	ctx := t.Context()
	if err := m.Create(ctx, Link{Code: "hot", URL: "https://example.com"}); err != nil {
		t.Fatal(err)
	}

	const workers, perWorker = 16, 200
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for range perWorker {
				if _, err := m.Resolve(ctx, "hot"); err != nil {
					t.Error(err)
					return
				}
				if _, err := m.Link(ctx, "hot"); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()

	l, err := m.Link(ctx, "hot")
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(workers * perWorker); l.Visits != want {
		t.Errorf("Visits = %d, want %d", l.Visits, want)
	}
}

func TestMemoryConcurrentCreateSameCode(t *testing.T) {
	m := NewMemory()
	ctx := t.Context()

	const workers = 32
	var created, taken atomic.Int32
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() {
			err := m.Create(ctx, Link{Code: "race", URL: fmt.Sprintf("https://example.com/%d", i)})
			switch {
			case err == nil:
				created.Add(1)
			case errors.Is(err, ErrCodeTaken):
				taken.Add(1)
			default:
				t.Errorf("Create: unexpected error %v", err)
			}
		})
	}
	wg.Wait()

	if created.Load() != 1 || taken.Load() != workers-1 {
		t.Errorf("created=%d taken=%d, want exactly one winner out of %d", created.Load(), taken.Load(), workers)
	}
}
