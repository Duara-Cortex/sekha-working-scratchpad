package workingmemory

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/inference"
)

// Processor runs the model workers and the sweep that times out chunks and commits idle memories.
type Processor struct {
	Store  *Store
	Engine inference.Engine
	Prompt PromptConfig

	Workers       int
	CallTimeout   time.Duration
	CommitTimeout time.Duration
	SweepInterval time.Duration
	MaxTokens     int
	Temperature   float64

	// OnPrompt, if set, sees every prompt sent to the model (tests use it).
	OnPrompt func(Prompt)
}

// Run starts the workers and the sweep and blocks until ctx is cancelled.
func (p *Processor) Run(ctx context.Context) {
	workers := p.Workers
	if workers < 1 {
		workers = 1
	}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if err := p.ProcessNext(ctx); err != nil {
					if ctx.Err() != nil || errors.Is(err, ErrClosed) {
						return
					}
					log.Printf("working memory: worker: %v", err)
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.sweepLoop(ctx)
	}()
	wg.Wait()
}

func (p *Processor) sweepLoop(ctx context.Context) {
	interval := p.SweepInterval
	if interval <= 0 {
		interval = time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.SweepOnce(ctx)
		}
	}
}

// SweepOnce times out overdue chunks and commits every memory that has been idle long enough.
func (p *Processor) SweepOnce(ctx context.Context) {
	for _, id := range p.Store.Sweep() {
		cctx, cancel := p.commitContext(ctx)
		sum, err := p.Store.Commit(cctx, id, "idle_timeout")
		cancel()
		if err != nil {
			log.Printf("working memory: idle commit of %s failed (will retry): %v", id, err)
			continue
		}
		log.Printf("working memory: committed %s after idle timeout: %d items, %d reinforcements", id, sum.Items, sum.Reinforcements)
	}
}

func (p *Processor) commitContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if p.CommitTimeout > 0 {
		return context.WithTimeout(ctx, p.CommitTimeout)
	}
	return context.WithCancel(ctx)
}

// ProcessNext waits for the next queued strong chunk and runs one stateless model call on it.
func (p *Processor) ProcessNext(ctx context.Context) error {
	job, err := p.Store.NextJob(ctx)
	if err != nil {
		return err
	}
	res := p.process(ctx, job)
	p.Store.FinishJob(job, res)
	return res.Err
}

func (p *Processor) process(ctx context.Context, job *Job) Result {
	prompt := BuildPrompt(p.Prompt, job.Task, job.Chunk, job.Context, job.Related)
	if p.OnPrompt != nil {
		p.OnPrompt(prompt)
	}

	callCtx := ctx
	if p.CallTimeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, p.CallTimeout)
		defer cancel()
	}
	out, err := p.Engine.Infer(callCtx, inference.Request{
		SystemPrompt: prompt.System,
		UserPrompt:   prompt.User,
		MaxTokens:    p.MaxTokens,
		Temperature:  p.Temperature,
	})
	if err != nil {
		return Result{Err: err}
	}

	raw := out.RawContent
	if raw == "" {
		raw = out.Thought
	}
	thought, labels := ParseThought(raw)
	var used []string
	for _, l := range labels {
		if id, ok := prompt.Labels[l]; ok {
			used = append(used, id) // labels the prompt did not contain are ignored
		}
	}
	score, status := p.Store.Score(ctx, job.Task, thought)
	return Result{Thought: thought, Used: used, Score: score, ScoreStatus: status}
}
