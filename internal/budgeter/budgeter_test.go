package budgeter

import (
	"strings"
	"testing"

	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/model"
)

func TestBudgeter_BasicPrompt(t *testing.T) {
	b := New(DefaultBudgetConfig())

	state := model.WorkingMemoryState{
		ActiveGoal: "Diagnose CPU throttling on Node 2",
		SensoryContext: []model.SensoryChunk{
			{ID: "s1", Text: "vcgencmd measure_temp reports 48.3C", Salience: 0.9},
		},
		LongTermContext: []string{
			"Node 2 PMIC can brown out if running 4 threads simultaneously",
		},
		Trajectory: []model.ReasoningStep{
			{
				StepIndex: 1,
				Thought:   "Check thread allocation of llama-server",
				Action:    "inspect systemd service unit",
				Status:    model.StepStatusSuccess,
			},
		},
	}

	prompt := b.BuildPrompt(state, "Service configured with --threads 2")

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
	if !strings.Contains(prompt.UserPrompt, "Step 1") {
		t.Fatalf("trajectory missing")
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

	// Create large state
	var chunks []model.SensoryChunk
	for i := 0; i < 50; i++ {
		chunks = append(chunks, model.SensoryChunk{
			ID:       string(rune(i)),
			Text:     strings.Repeat("very long sensory text log stream line ", 10),
			Salience: 0.8,
		})
	}

	state := model.WorkingMemoryState{
		ActiveGoal:     "Perform load test under constrained memory",
		SensoryContext: chunks,
	}

	prompt := b.BuildPrompt(state, "")
	// Ensure system prompt is intact
	if !strings.Contains(prompt.SystemPrompt, "Working Memory Deliberation Engine") {
		t.Fatalf("system prompt truncated")
	}
	// Verify total is within or very close to budget bounds
	if prompt.EstimatedTotal > 450 {
		t.Fatalf("budgeter failed to constrain large input: %d tokens", prompt.EstimatedTotal)
	}
}
