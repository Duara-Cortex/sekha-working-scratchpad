package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/budgeter"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/inference"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/model"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/scratchpad"
)

type mockEngine struct {
	output *inference.DeliberationOutput
	err    error
}

func (m *mockEngine) Infer(ctx context.Context, systemPrompt, userPrompt string, maxTokens int, temperature float64) (*inference.DeliberationOutput, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.output, nil
}

func (m *mockEngine) Health(ctx context.Context) error {
	return nil
}

func TestServer_DeliberateAndScratchpad(t *testing.T) {
	store := scratchpad.NewStore()
	bud := budgeter.New(budgeter.DefaultBudgetConfig())
	mock := &mockEngine{
		output: &inference.DeliberationOutput{
			RawContent:       "Thought: Examine sensory log\nAction: Read buffer chunk c1\nComplete: false",
			Thought:          "Examine sensory log",
			Action:           "Read buffer chunk c1",
			IsComplete:       false,
			PromptTokens:     50,
			CompletionTokens: 20,
			TotalTokens:      70,
			PromptTPS:        32.0,
			PredictedTPS:     12.5,
		},
	}
	server := NewServer(store, bud, mock)

	// 1. Deliberate
	reqBody := model.DeliberateRequest{
		Objective: "Isolate network jitter",
		SensoryChunks: []model.SensoryChunk{
			{ID: "c1", Text: "packet delay > 5ms", Salience: 0.8},
		},
		LongTermContext: []string{"Switch port 2 links to Node 3"},
	}
	data, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/working/deliberate", bytes.NewReader(data))
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp model.DeliberateResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode deliberate response: %v", err)
	}

	if resp.StepIndex != 1 {
		t.Fatalf("expected step index 1, got %d", resp.StepIndex)
	}
	if resp.Thought != "Examine sensory log" {
		t.Fatalf("unexpected thought: %s", resp.Thought)
	}
	if resp.ProposedAction != "Read buffer chunk c1" {
		t.Fatalf("unexpected action: %s", resp.ProposedAction)
	}

	// 2. Query Scratchpad
	reqGet := httptest.NewRequest(http.MethodGet, "/api/v1/working/scratchpad", nil)
	wGet := httptest.NewRecorder()
	server.ServeHTTP(wGet, reqGet)

	if wGet.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", wGet.Code)
	}

	var state model.WorkingMemoryState
	if err := json.NewDecoder(wGet.Body).Decode(&state); err != nil {
		t.Fatalf("failed to decode state: %v", err)
	}

	if state.ActiveGoal != "Isolate network jitter" {
		t.Fatalf("unexpected active goal: %s", state.ActiveGoal)
	}
	if len(state.Trajectory) != 1 {
		t.Fatalf("expected trajectory length 1, got %d", len(state.Trajectory))
	}
	if len(state.SensoryContext) != 1 {
		t.Fatalf("expected 1 sensory chunk, got %d", len(state.SensoryContext))
	}

	// 3. Clear Scratchpad
	reqClear := httptest.NewRequest(http.MethodPost, "/api/v1/working/clear", nil)
	wClear := httptest.NewRecorder()
	server.ServeHTTP(wClear, reqClear)

	if wClear.Code != http.StatusOK {
		t.Fatalf("expected clear 200, got %d", wClear.Code)
	}

	clearedState := store.GetState()
	if clearedState.ActiveGoal != "" || len(clearedState.Trajectory) != 0 {
		t.Fatalf("scratchpad not cleared properly")
	}
}

func TestServer_RollbackEndpoint(t *testing.T) {
	store := scratchpad.NewStore()
	bud := budgeter.New(budgeter.DefaultBudgetConfig())
	mock := &mockEngine{
		output: &inference.DeliberationOutput{
			Thought: "Step 1 good", Action: "Do step 1", IsComplete: false,
		},
	}
	server := NewServer(store, bud, mock)

	// Step 1
	store.SetGoal("Test rollback")
	store.AddStep("Step 1 good", "Do step 1", model.StepStatusSuccess)
	snapID := store.CreateSnapshot("Baseline")
	if snapID != 1 {
		t.Fatalf("expected snapID 1, got %d", snapID)
	}

	// Step 2 (faulty)
	store.AddStep("Step 2 bad", "crash", model.StepStatusError)

	// Trigger rollback endpoint
	req := httptest.NewRequest(http.MethodPost, "/api/v1/working/rollback?snapshot_id=1", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("rollback returned status %d: %s", w.Code, w.Body.String())
	}

	state := store.GetState()
	if len(state.Trajectory) != 1 {
		t.Fatalf("expected 1 step after rollback, got %d", len(state.Trajectory))
	}
	if state.Trajectory[0].Thought != "Step 1 good" {
		t.Fatalf("expected step 1 thought, got: %s", state.Trajectory[0].Thought)
	}
}
