package index

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mikethicke/explore/internal/model"
)

// fakeExplain returns a constant explanation, recording every ID passed in.
type fakeExplain struct {
	mu      sync.Mutex
	calls   []model.NodeID
	block   chan struct{} // if non-nil, blocks until closed
	failFor map[model.NodeID]bool
}

func (f *fakeExplain) fn(ctx context.Context, id model.NodeID) (*model.Explanation, error) {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	f.calls = append(f.calls, id)
	shouldFail := f.failFor[id]
	f.mu.Unlock()
	if shouldFail {
		return nil, errors.New("synthetic failure")
	}
	return &model.Explanation{NodeID: id, Prose: id.String()}, nil
}

func (f *fakeExplain) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func file(path string) model.NodeID {
	return model.NodeID{Kind: model.KindFile, Path: path}
}

// drainUpdates pulls n updates with a per-update timeout, then returns.
func drainUpdates(t *testing.T, ch <-chan Update, n int, timeout time.Duration) []Update {
	t.Helper()
	out := make([]Update, 0, n)
	for i := 0; i < n; i++ {
		select {
		case u, ok := <-ch:
			if !ok {
				t.Fatalf("updates channel closed after %d items, expected %d", i, n)
			}
			out = append(out, u)
		case <-time.After(timeout):
			t.Fatalf("timed out waiting for update %d/%d", i+1, n)
		}
	}
	return out
}

func TestPrefetcher_ProcessesEnqueued(t *testing.T) {
	fe := &fakeExplain{}
	p := newPrefetcher(fe.fn, 2)
	defer p.Close()

	p.Enqueue(file("a.go"), file("b.go"), file("c.go"))

	got := drainUpdates(t, p.Updates(), 3, time.Second)
	if len(got) != 3 {
		t.Fatalf("expected 3 updates, got %d", len(got))
	}
	seen := map[string]bool{}
	for _, u := range got {
		if u.Err != nil {
			t.Errorf("unexpected error: %v", u.Err)
		}
		seen[u.ID.Path] = true
	}
	for _, want := range []string{"a.go", "b.go", "c.go"} {
		if !seen[want] {
			t.Errorf("missing update for %s", want)
		}
	}
}

func TestPrefetcher_DedupsInFlightAndQueued(t *testing.T) {
	gate := make(chan struct{})
	fe := &fakeExplain{block: gate}
	p := newPrefetcher(fe.fn, 1)
	defer p.Close()

	// Enqueue the same ID multiple times. With 1 worker, the first one goes
	// in-flight immediately and blocks on the gate; subsequent enqueues
	// should be deduped against either the in-flight set or the queue.
	id := file("a.go")
	p.Enqueue(id, id, id, id, id)
	// Give the worker a moment to pick up the first item.
	time.Sleep(20 * time.Millisecond)
	close(gate)

	// Expect exactly one update.
	select {
	case <-p.Updates():
	case <-time.After(time.Second):
		t.Fatal("expected one update")
	}
	// Confirm no second update arrives.
	select {
	case u := <-p.Updates():
		t.Fatalf("unexpected second update: %+v", u)
	case <-time.After(50 * time.Millisecond):
	}
	if c := fe.callCount(); c != 1 {
		t.Fatalf("expected 1 explain call, got %d", c)
	}
}

func TestPrefetcher_QueueCapDropsOldest(t *testing.T) {
	gate := make(chan struct{})
	fe := &fakeExplain{block: gate}
	p := newPrefetcher(fe.fn, 1)
	p.queueCap = 3 // small cap for easier reasoning
	defer p.Close()

	// First enqueued ID goes in-flight (blocked on gate). The next 3 fill
	// the queue. Two more should each drop the oldest queued item.
	p.Enqueue(file("inflight"))
	time.Sleep(20 * time.Millisecond) // let the worker pick up "inflight"

	p.Enqueue(file("q1"), file("q2"), file("q3"))
	p.Enqueue(file("q4"), file("q5"))
	// Queue should now be [q3, q4, q5] (q1, q2 dropped).

	close(gate)

	// Pull all 4 successful explains (inflight, q3, q4, q5).
	got := drainUpdates(t, p.Updates(), 4, time.Second)
	seen := map[string]bool{}
	for _, u := range got {
		seen[u.ID.Path] = true
	}
	for _, want := range []string{"inflight", "q3", "q4", "q5"} {
		if !seen[want] {
			t.Errorf("missing %s; got %v", want, seen)
		}
	}
	for _, gone := range []string{"q1", "q2"} {
		if seen[gone] {
			t.Errorf("expected %s to be dropped, but it ran", gone)
		}
	}
}

func TestPrefetcher_PropagatesError(t *testing.T) {
	fe := &fakeExplain{failFor: map[model.NodeID]bool{file("bad.go"): true}}
	p := newPrefetcher(fe.fn, 1)
	defer p.Close()

	p.Enqueue(file("bad.go"))
	got := drainUpdates(t, p.Updates(), 1, time.Second)
	if got[0].Err == nil {
		t.Fatal("expected error to propagate")
	}
}

func TestPrefetcher_CloseShutsDownCleanly(t *testing.T) {
	fe := &fakeExplain{}
	p := newPrefetcher(fe.fn, 3)

	p.Enqueue(file("a.go"), file("b.go"))
	drainUpdates(t, p.Updates(), 2, time.Second)

	done := make(chan struct{})
	go func() {
		p.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close did not return")
	}
	// Updates channel should be closed.
	if _, ok := <-p.Updates(); ok {
		t.Fatal("expected updates channel to be closed after Close")
	}

	// Idempotent close.
	p.Close()
}

func TestPrefetcher_EnqueueAfterCloseIsNoop(t *testing.T) {
	fe := &fakeExplain{}
	p := newPrefetcher(fe.fn, 1)
	p.Close()
	p.Enqueue(file("ignored.go"))
	if c := fe.callCount(); c != 0 {
		t.Fatalf("expected 0 calls after close, got %d", c)
	}
}

func TestPrefetcher_SkipsUnloadableKinds(t *testing.T) {
	fe := &fakeExplain{}
	p := newPrefetcher(fe.fn, 1)
	defer p.Close()

	// model.Kind values are KindRepo=0, KindDir=1, KindFile=2, KindSymbol=3 —
	// so a Kind value past those is invalid. Use that to assert filtering.
	bogus := model.NodeID{Kind: model.Kind(99), Path: "x"}
	p.Enqueue(bogus)
	select {
	case u := <-p.Updates():
		t.Fatalf("unexpected update for unloadable kind: %+v", u)
	case <-time.After(50 * time.Millisecond):
	}
}

// Concurrent enqueues of the *same* id should dedup to 1 invocation as long
// as the prior call hasn't already completed. We block the worker via a gate
// so all enqueues land while the first item is in-flight or queued; that's
// the window dedup actually protects.
func TestPrefetcher_ConcurrentEnqueueDedups(t *testing.T) {
	gate := make(chan struct{})
	fe := &fakeExplain{block: gate}
	p := newPrefetcher(fe.fn, 4)
	defer p.Close()

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			p.Enqueue(file("f.go"))
		}()
	}
	wg.Wait()
	// Now the first enqueue is in flight (blocked on gate) or queued; later
	// duplicates were rejected by dedup. Release the gate.
	close(gate)

	count := int32(0)
	done := make(chan struct{})
	go func() {
		for range p.Updates() {
			atomic.AddInt32(&count, 1)
		}
		close(done)
	}()
	time.Sleep(100 * time.Millisecond)
	if c := atomic.LoadInt32(&count); c != 1 {
		t.Fatalf("expected exactly 1 dedup-survived update, got %d", c)
	}
}
