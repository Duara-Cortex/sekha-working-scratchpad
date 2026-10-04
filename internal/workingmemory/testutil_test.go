package workingmemory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/inference"
)

// clock is a manual clock for deterministic timeouts.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock { return &clock{now: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// fakeCommitter records every batch.
type fakeCommitter struct {
	mu      sync.Mutex
	batches []CommitBatch
	err     error
}

func (f *fakeCommitter) Commit(_ context.Context, b CommitBatch) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.batches = append(f.batches, b)
	return nil
}

func (f *fakeCommitter) only(t *testing.T) CommitBatch {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.batches) != 1 {
		t.Fatalf("expected 1 commit batch, got %d", len(f.batches))
	}
	return f.batches[0]
}

// fakeEngine answers every call with a thought about the chunk and records the prompts.
type fakeEngine struct {
	mu      sync.Mutex
	calls   int
	systems []string
	users   []string
	used    string        // the Used: line; default "c1"
	onInfer func()        // runs inside the call, before it returns
	block   chan struct{} // if set, each call waits for a value
	started chan struct{} // if set, receives once per call when it starts
	err     error
}

func (e *fakeEngine) Infer(ctx context.Context, req inference.Request) (*inference.DeliberationOutput, error) {
	e.mu.Lock()
	e.calls++
	e.systems = append(e.systems, req.SystemPrompt)
	e.users = append(e.users, req.UserPrompt)
	used, onInfer, block, started, err := e.used, e.onInfer, e.block, e.started, e.err
	e.mu.Unlock()

	if started != nil {
		started <- struct{}{}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if onInfer != nil {
		onInfer()
	}
	if err != nil {
		return nil, err
	}
	if used == "" {
		used = "c1"
	}
	chunk := itemUnderConsideration(req.UserPrompt)
	raw := fmt.Sprintf("Thought: thought about %q\nUsed: %s", chunk, used)
	return &inference.DeliberationOutput{RawContent: raw, PromptTokens: len(req.UserPrompt)}, nil
}

func (e *fakeEngine) Health(context.Context) error { return nil }

func (e *fakeEngine) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func itemUnderConsideration(user string) string {
	_, after, ok := strings.Cut(user, "### ITEM UNDER CONSIDERATION\n[c1] ")
	if !ok {
		return ""
	}
	line, _, _ := strings.Cut(after, "\n")
	return line
}

// fakeScorer scores by a fixed table, or 0.5 for anything else.
type fakeScorer struct{ scores map[string]float64 }

func (f fakeScorer) Score(_ context.Context, _, text string) (*float64, string) {
	v, ok := f.scores[text]
	if !ok {
		v = 0.5
	}
	return &v, "scored"
}

type harness struct {
	clock     *clock
	committer *fakeCommitter
	engine    *fakeEngine
	store     *Store
	proc      *Processor
}

func newHarness(t *testing.T, opts Options) *harness {
	t.Helper()
	h := &harness{clock: newClock(), committer: &fakeCommitter{}, engine: &fakeEngine{}}
	opts.Now = h.clock.Now
	if opts.IdleTimeout == 0 {
		opts.IdleTimeout = 30 * time.Second
	}
	if opts.ReinforceStep == 0 {
		opts.ReinforceStep = 0.1
	}
	h.store = NewStore(opts, h.committer, fakeScorer{})
	h.proc = &Processor{
		Store:  h.store,
		Engine: h.engine,
		Prompt: PromptConfig{Budget: 1024, TaskBudget: 150},
	}
	return h
}

// processN runs n model calls; each must find a queued chunk.
func (h *harness) processN(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := h.proc.ProcessNext(ctx)
		cancel()
		if err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
}

func (h *harness) status(t *testing.T, memoryID string) MemoryStatus {
	t.Helper()
	st, err := h.store.Status(memoryID)
	if err != nil {
		t.Fatalf("status %s: %v", memoryID, err)
	}
	return st
}

func score(v float64) *float64 { return &v }

// dialogue builds a memory of n turns; the turns listed in strong are strong.
func dialogue(memoryID string, firstSeq int64, n int, strong ...int) []Chunk {
	isStrong := map[int]bool{}
	for _, i := range strong {
		isStrong[i] = true
	}
	out := make([]Chunk, n)
	speakers := []string{"user", "assistant"}
	for i := 0; i < n; i++ {
		s := 0.1
		if isStrong[i] {
			s = 0.8
		}
		out[i] = Chunk{
			ID:          fmt.Sprintf("%s-h%d", memoryID, i),
			MemoryID:    memoryID,
			Text:        fmt.Sprintf("%s turn %d", memoryID, i),
			Type:        "dialogue",
			Source:      "test",
			Session:     "s-" + memoryID,
			Speaker:     speakers[i%2],
			TurnID:      fmt.Sprintf("t%d", i),
			Seq:         firstSeq + int64(i),
			Task:        "task of " + memoryID,
			TaskScore:   score(s),
			Strong:      isStrong[i],
			ScoreStatus: "scored",
		}
	}
	return out
}

func itemByText(t *testing.T, items []Item, text string) Item {
	t.Helper()
	for _, it := range items {
		if it.Text == text {
			return it
		}
	}
	t.Fatalf("no item with text %q", text)
	return Item{}
}

func commitItemByText(t *testing.T, b CommitBatch, text string) CommitItem {
	t.Helper()
	for _, it := range b.Items {
		if it.Text == text {
			return it
		}
	}
	t.Fatalf("commit batch has no item with text %q", text)
	return CommitItem{}
}

func hasLink(links []Link, rel, item string) bool {
	for _, l := range links {
		if l.Rel == rel && l.Item == item {
			return true
		}
	}
	return false
}

var errBoom = errors.New("boom")
