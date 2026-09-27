package budgeter

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/model"
)

func TestBudgeter_BasicPrompt(t *testing.T) {
	b := New(DefaultBudgetConfig())

	req := model.DeliberateRequest{
		Objective: "Diagnose CPU throttling on Node 2",
		SensoryChunks: []model.SensoryChunk{
			{ID: "s1", Text: "vcgencmd measure_temp reports 48.3C", Salience: 0.9},
		},
		LongTermContext: []string{
			"Node 2 PMIC can brown out if running 4 threads simultaneously",
		},
		Observation: "Service configured with --threads 2",
	}

	prompt := b.BuildPrompt(req)

	if !strings.Contains(prompt.SystemPrompt, "Working Memory Deliberation Engine") {
		t.Fatalf("system prompt missing key role identifier")
	}
	if !strings.Contains(prompt.UserPrompt, "ACTIVE TASK GOAL") {
		t.Fatalf("user prompt missing goal header")
	}
	if !strings.Contains(prompt.UserPrompt, "48.3C") {
		t.Fatalf("sensory content missing")
	}
	if !strings.Contains(prompt.UserPrompt, "brown out") {
		t.Fatalf("long term context missing")
	}
	if !strings.Contains(prompt.UserPrompt, "CURRENT OBSERVATION") {
		t.Fatalf("observation header missing")
	}
	if !strings.Contains(prompt.UserPrompt, "Service configured with --threads 2") {
		t.Fatalf("observation content missing")
	}
	if prompt.EstimatedTotal > prompt.BudgetLimit {
		t.Fatalf("prompt exceeded budget limit: %d > %d", prompt.EstimatedTotal, prompt.BudgetLimit)
	}
}

func TestBudgeter_OverflowEnforcement(t *testing.T) {
	// Small budget to force truncation
	cfg := BudgetConfig{
		MaxContextTokens: 500,
		OutputReserve:    100,
		SystemBudget:     100,
		GoalBudget:       50,
		SensoryBudget:    100,
		LongTermBudget:   50,
		TrajectoryBudget: 100,
	}
	b := New(cfg)

	// Create large request
	var chunks []model.SensoryChunk
	for i := 0; i < 50; i++ {
		chunks = append(chunks, model.SensoryChunk{
			ID:       string(rune(i)),
			Text:     strings.Repeat("very long sensory text log stream line ", 10),
			Salience: 0.8,
		})
	}

	req := model.DeliberateRequest{
		Objective:     "Perform load test under constrained memory",
		SensoryChunks: chunks,
		Observation:   strings.Repeat("very long observation details ", 10),
	}

	prompt := b.BuildPrompt(req)
	// Ensure system prompt is intact
	if !strings.Contains(prompt.SystemPrompt, "Working Memory Deliberation Engine") {
		t.Fatalf("system prompt truncated")
	}
	// Verify total is within or very close to budget bounds
	if prompt.EstimatedTotal > 450 {
		t.Fatalf("budgeter failed to constrain large input: %d tokens", prompt.EstimatedTotal)
	}
}

// window4096 mirrors the live Node 2 envelope: CONTEXT_LIMIT=4096, OUTPUT_RESERVE=512.
func window4096() Config {
	cfg := DefaultConfig()
	cfg.MaxContextTokens = 4096
	cfg.OutputReserve = 512
	return cfg
}

// makeChunks builds n chunks of roughly tokensEach estimated tokens, with salience from sal(i).
func makeChunks(n, tokensEach int, sal func(i int) float64) []model.SensoryChunk {
	chunks := make([]model.SensoryChunk, n)
	for i := range chunks {
		text := fmt.Sprintf("chunk-%03d ", i) + strings.Repeat("abcd ", int(float64(tokensEach)*CharsPerTokenHeuristic/5))
		chunks[i] = model.SensoryChunk{ID: fmt.Sprintf("c%d", i), Text: text, Salience: sal(i)}
	}
	return chunks
}

// keptChunkIDs returns the chunk markers in prompt order, and whether each was truncated.
func keptChunkIDs(t *testing.T, userPrompt string) (ids []int, truncated []bool) {
	t.Helper()
	for _, line := range strings.Split(userPrompt, "\n") {
		if !strings.HasPrefix(line, "- [salience=") {
			continue
		}
		idx := strings.Index(line, "chunk-")
		if idx < 0 {
			t.Fatalf("sensory line without chunk marker: %q", line)
		}
		var id int
		if _, err := fmt.Sscanf(line[idx:], "chunk-%03d", &id); err != nil {
			t.Fatalf("bad chunk marker in %q: %v", line, err)
		}
		ids = append(ids, id)
		truncated = append(truncated, strings.HasSuffix(line, truncationMarker))
	}
	return ids, truncated
}

func sensoryTokens(userPrompt string) int {
	total := 0
	for _, line := range strings.Split(userPrompt, "\n") {
		if strings.HasPrefix(line, "- [salience=") {
			total += EstimateTokens(line + "\n")
		}
	}
	return total
}

func TestBudgeter_SensoryUsesRestOfWindow(t *testing.T) {
	b := New(window4096())
	req := model.DeliberateRequest{
		Objective:     "Summarize the transcript",
		SensoryChunks: makeChunks(60, 60, func(int) float64 { return 0.5 }),
	}

	prompt := b.BuildPrompt(req)

	if got := sensoryTokens(prompt.UserPrompt); got <= 2000 {
		t.Fatalf("expected sensory to use > 2000 tokens of a 4096 window, got %d", got)
	}
	limit := prompt.BudgetLimit - DefaultSafetyMargin
	if prompt.EstimatedTotal > limit {
		t.Fatalf("prompt over window: %d > %d", prompt.EstimatedTotal, limit)
	}
	if prompt.Usage.PromptWindowTokens != 3584 {
		t.Fatalf("expected prompt window 3584, got %d", prompt.Usage.PromptWindowTokens)
	}
}

func TestBudgeter_PrepackedThatFitsIsUntouched(t *testing.T) {
	b := New(window4096())
	chunks := makeChunks(35, 60, func(i int) float64 { return float64(i%7) / 10 })
	// ~410 tokens of facts: more than LONG_TERM_BUDGET, which prepacked requests ignore.
	facts := make([]string, 6)
	for i := range facts {
		facts[i] = fmt.Sprintf("fact-%d ", i) + strings.Repeat("wxyz ", 50)
	}
	req := model.DeliberateRequest{
		Objective:       "Summarize the transcript",
		SensoryChunks:   chunks,
		LongTermContext: facts,
		Prepacked:       true,
	}

	prompt := b.BuildPrompt(req)

	for _, c := range chunks {
		if !strings.Contains(prompt.UserPrompt, strings.TrimSpace(c.Text)+"\n") {
			t.Fatalf("prepacked chunk %s was altered or dropped", c.ID)
		}
	}
	for _, f := range facts {
		if !strings.Contains(prompt.UserPrompt, "- "+strings.TrimSpace(f)+"\n") {
			t.Fatalf("prepacked fact was dropped: %.20q", f)
		}
	}
	ids, _ := keptChunkIDs(t, prompt.UserPrompt)
	for i, id := range ids {
		if id != i {
			t.Fatalf("prepacked chunks reordered: position %d holds chunk %d", i, id)
		}
	}
	u := prompt.Usage
	if u.SensoryKept != 35 || u.SensoryDropped != 0 || u.SensoryTruncated != 0 || u.FactsKept != 6 {
		t.Fatalf("unexpected usage for fitting prepacked request: %+v", u)
	}

	// The same request without prepacked is held to LONG_TERM_BUDGET.
	req.Prepacked = false
	if got := b.BuildPrompt(req).Usage.FactsKept; got >= 6 {
		t.Fatalf("expected non-prepacked facts to be capped, kept %d", got)
	}
}

func TestBudgeter_DropsLowestSalienceFirstAndPreservesOrder(t *testing.T) {
	b := New(window4096())
	// 80 chunks x ~60 tokens is far over the window. Saliences are distinct and scrambled.
	sal := func(i int) float64 { return float64((i*37)%80) / 80 }
	chunks := makeChunks(80, 60, sal)

	for _, prepacked := range []bool{false, true} {
		prompt := b.BuildPrompt(model.DeliberateRequest{
			Objective:     "Summarize the transcript",
			SensoryChunks: chunks,
			Prepacked:     prepacked,
		})

		ids, truncated := keptChunkIDs(t, prompt.UserPrompt)
		if len(ids) == 0 || len(ids) == len(chunks) {
			t.Fatalf("prepacked=%t: expected some chunks dropped, kept %d of %d", prepacked, len(ids), len(chunks))
		}

		// Kept chunks appear in their original order.
		for i := 1; i < len(ids); i++ {
			if ids[i] <= ids[i-1] {
				t.Fatalf("prepacked=%t: order not preserved: %v", prepacked, ids)
			}
		}

		// Every dropped chunk has lower salience than every kept chunk; at most one is truncated
		// and it has the lowest salience of those kept.
		kept := map[int]bool{}
		minKept, minWhole, truncCount := 2.0, 2.0, 0
		for i, id := range ids {
			kept[id] = true
			minKept = min(minKept, sal(id))
			if truncated[i] {
				truncCount++
			} else {
				minWhole = min(minWhole, sal(id))
			}
		}
		for i, id := range ids {
			if truncated[i] && sal(id) > minWhole {
				t.Fatalf("prepacked=%t: truncated chunk %d outranks a whole chunk", prepacked, id)
			}
		}
		if truncCount > 1 {
			t.Fatalf("prepacked=%t: expected at most one truncated chunk, got %d", prepacked, truncCount)
		}
		for i := range chunks {
			if !kept[i] && sal(i) > minKept {
				t.Fatalf("prepacked=%t: dropped chunk %d (salience %.3f) outranks a kept chunk (%.3f)", prepacked, i, sal(i), minKept)
			}
		}

		if prompt.EstimatedTotal > prompt.BudgetLimit-DefaultSafetyMargin {
			t.Fatalf("prepacked=%t: prompt over window: %d", prepacked, prompt.EstimatedTotal)
		}
	}
}

func TestBudgeter_ContextUsageCounts(t *testing.T) {
	cfg := window4096()
	cfg.LongTermBudget = 60
	b := New(cfg)
	chunks := makeChunks(80, 60, func(i int) float64 { return float64(i) / 80 })
	facts := []string{
		"fact-a " + strings.Repeat("wxyz ", 20),
		"fact-b " + strings.Repeat("wxyz ", 20),
		"fact-c " + strings.Repeat("wxyz ", 20),
	}

	prompt := b.BuildPrompt(model.DeliberateRequest{
		Objective:       "Summarize the transcript",
		SensoryChunks:   chunks,
		LongTermContext: facts,
	})
	u := prompt.Usage

	ids, truncated := keptChunkIDs(t, prompt.UserPrompt)
	truncCount := 0
	for _, tr := range truncated {
		if tr {
			truncCount++
		}
	}
	if u.SensoryReceived != 80 || u.SensoryKept != len(ids) || u.SensoryKept+u.SensoryDropped != u.SensoryReceived {
		t.Fatalf("sensory counts wrong: %+v (lines in prompt %d)", u, len(ids))
	}
	if u.SensoryTruncated != truncCount {
		t.Fatalf("sensory_truncated %d, but %d truncated lines in prompt", u.SensoryTruncated, truncCount)
	}
	// Salience rises with index, so the kept set is the tail.
	if ids[0] != 80-len(ids) {
		t.Fatalf("expected the highest-salience tail to be kept, first kept is %d of %d kept", ids[0], len(ids))
	}
	// Each fact is ~27 tokens; a 60-token cap (with header) keeps the top-ranked one only.
	if u.FactsReceived != 3 || u.FactsKept != 1 {
		t.Fatalf("facts counts wrong: %+v", u)
	}
	if !strings.Contains(prompt.UserPrompt, "fact-a") || strings.Contains(prompt.UserPrompt, "fact-b") {
		t.Fatalf("facts not dropped from the lowest-ranked end")
	}
	if u.EstimatedPromptTokens != prompt.EstimatedTotal || u.PromptWindowTokens != 3584 || u.ActualPromptTokens != 0 {
		t.Fatalf("token fields wrong: %+v", u)
	}
}

func TestBudgeter_PrepackedOverflowDropsChunksBeforeFacts(t *testing.T) {
	b := New(window4096())
	facts := make([]string, 10)
	for i := range facts {
		facts[i] = fmt.Sprintf("fact-%d ", i) + strings.Repeat("wxyz ", 50)
	}
	prompt := b.BuildPrompt(model.DeliberateRequest{
		Objective:       "Summarize the transcript",
		SensoryChunks:   makeChunks(80, 60, func(i int) float64 { return 0.5 }),
		LongTermContext: facts,
		Prepacked:       true,
	})
	if prompt.Usage.FactsKept != 10 {
		t.Fatalf("expected all facts kept on prepacked overflow, got %d", prompt.Usage.FactsKept)
	}
	if prompt.Usage.SensoryDropped == 0 {
		t.Fatalf("expected chunks dropped on prepacked overflow")
	}
}

func TestBudgeter_SensoryCap(t *testing.T) {
	cfg := window4096()
	cfg.SensoryBudget = 400
	b := New(cfg)
	prompt := b.BuildPrompt(model.DeliberateRequest{
		Objective:     "Summarize the transcript",
		SensoryChunks: makeChunks(60, 60, func(int) float64 { return 0.5 }),
	})
	if got := sensoryTokens(prompt.UserPrompt); got > 400 {
		t.Fatalf("SENSORY_BUDGET cap ignored: %d tokens", got)
	}
}
