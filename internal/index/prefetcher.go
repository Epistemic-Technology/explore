package index

import (
	"context"
	"sync"

	"github.com/mikethicke/explore/internal/debug"
	"github.com/mikethicke/explore/internal/model"
)

// Prefetcher generates explanations in the background for nodes the user is
// likely to visit next (visible siblings, parent files for symbols, etc.).
// Cache hits return immediately, so prefetched items pre-warm the on-disk
// cache *and* the TUI's in-memory map without ever blocking the user-driven
// path. Workers are concurrency-capped so a frenzy of navigation doesn't blow
// past Anthropic's rate limits.
//
// Lifecycle: NewPrefetcher → callers Enqueue and consume Updates → Close once
// the TUI shuts down. After Close the Updates channel is drained and closed.
type Prefetcher struct {
	explain func(context.Context, model.NodeID) (*model.Explanation, error)

	updates  chan Update
	queueCap int

	mu       sync.Mutex
	queue    []model.NodeID
	queued   map[model.NodeID]bool
	inflight map[model.NodeID]bool
	closed   bool

	wake   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Update is the result of one prefetch task. On error Exp is nil; on success
// (including cache hit) Exp carries the explanation.
type Update struct {
	ID  model.NodeID
	Exp *model.Explanation
	Err error
}

const (
	defaultPrefetchConcurrency = 3
	defaultPrefetchQueueCap    = 50
	defaultUpdateBuffer        = 16
)

// NewPrefetcher starts a worker pool. concurrency clamps the number of
// in-flight LLM calls (≤0 falls back to the default of 3).
func NewPrefetcher(gen *Generator, concurrency int) *Prefetcher {
	return newPrefetcher(func(ctx context.Context, id model.NodeID) (*model.Explanation, error) {
		switch id.Kind {
		case model.KindFile:
			return gen.ExplainFile(ctx, id.Path)
		case model.KindSymbol:
			// parentSummary="" — see prefetcher.execute for rationale.
			return gen.ExplainSymbol(ctx, id.Path, id.Symbol, "")
		case model.KindDir:
			return gen.ExplainDir(ctx, id.Path)
		case model.KindRepo:
			return gen.ExplainRepo(ctx)
		}
		return nil, nil
	}, concurrency)
}

// newPrefetcher is the internal constructor. Production callers go through
// NewPrefetcher; tests use this to inject a fake explain function and avoid
// the LLM round-trip.
func newPrefetcher(explain func(context.Context, model.NodeID) (*model.Explanation, error), concurrency int) *Prefetcher {
	if concurrency <= 0 {
		concurrency = defaultPrefetchConcurrency
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &Prefetcher{
		explain:  explain,
		updates:  make(chan Update, defaultUpdateBuffer),
		queueCap: defaultPrefetchQueueCap,
		queued:   make(map[model.NodeID]bool),
		inflight: make(map[model.NodeID]bool),
		wake:     make(chan struct{}, 1),
		ctx:      ctx,
		cancel:   cancel,
	}
	for i := 0; i < concurrency; i++ {
		p.wg.Add(1)
		go p.worker()
	}
	return p
}

// Updates is the channel of completed prefetch results. The TUI listens on it
// via a Cmd. Closed after the last worker exits, post-Close.
func (p *Prefetcher) Updates() <-chan Update { return p.updates }

// Enqueue adds NodeIDs to the queue, deduplicating against items that are
// either already queued or in flight. Queue is capped (oldest entries drop
// when the cap is reached) so a fast-navigating user doesn't pile up stale
// work indefinitely.
func (p *Prefetcher) Enqueue(ids ...model.NodeID) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	added := 0
	for _, id := range ids {
		if !loadableKind(id.Kind) {
			continue
		}
		if p.queued[id] || p.inflight[id] {
			continue
		}
		if len(p.queue) >= p.queueCap {
			// Drop oldest. Keeps the queue focused on the user's *current*
			// neighborhood rather than wherever they were ten keypresses ago.
			old := p.queue[0]
			p.queue = p.queue[1:]
			delete(p.queued, old)
		}
		p.queue = append(p.queue, id)
		p.queued[id] = true
		added++
	}
	p.mu.Unlock()
	if added > 0 {
		p.signal()
	}
}

// Close cancels worker contexts, waits for them, then closes Updates. Safe to
// call multiple times. Pending queued items are dropped; in-flight LLM calls
// can outlive Close because Explain* writes to the cache on success even when
// the surrounding context cancels mid-stream (work isn't wasted).
func (p *Prefetcher) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.queue = nil
	p.queued = nil
	p.mu.Unlock()

	p.cancel()
	// Wake any worker blocked on the wake channel so they observe ctx.Done().
	for i := 0; i < cap(p.wake)+1; i++ {
		select {
		case p.wake <- struct{}{}:
		default:
		}
	}
	p.wg.Wait()
	close(p.updates)
}

func (p *Prefetcher) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *Prefetcher) next() (model.NodeID, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queue) == 0 {
		return model.NodeID{}, false
	}
	id := p.queue[0]
	p.queue = p.queue[1:]
	delete(p.queued, id)
	p.inflight[id] = true
	return id, true
}

func (p *Prefetcher) doneInflight(id model.NodeID) {
	p.mu.Lock()
	delete(p.inflight, id)
	p.mu.Unlock()
}

func (p *Prefetcher) worker() {
	defer p.wg.Done()
	for {
		id, ok := p.next()
		if !ok {
			select {
			case <-p.ctx.Done():
				return
			case <-p.wake:
				continue
			}
		}
		p.execute(id)
		p.doneInflight(id)
	}
}

func (p *Prefetcher) execute(id model.NodeID) {
	debug.Logf("prefetch: execute kind=%v path=%q sym=%q", id.Kind, id.Path, id.Symbol)
	// Symbol prefetches pass parentSummary="" so we don't have to look up the
	// parent file's explanation here. The cache key for symbols is keyed on
	// source hash (not parentSummary), so a later user-driven request hits
	// the same cached entry — slight quality trade-off in the saved prose,
	// accepted for v0.2 to keep the prefetcher self-contained.
	exp, err := p.explain(p.ctx, id)
	select {
	case p.updates <- Update{ID: id, Exp: exp, Err: err}:
	case <-p.ctx.Done():
	}
}

func loadableKind(k model.Kind) bool {
	switch k {
	case model.KindFile, model.KindSymbol, model.KindDir, model.KindRepo:
		return true
	}
	return false
}
