package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/budgeter"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/inference"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/model"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/scratchpad"
)

type mockEngine struct {
	output      *inference.DeliberationOutput
	err         error
	calls       int
	userPrompts []string
}

func (m *mockEngine) Infer(_ context.Context, req inference.Request) (*inference.DeliberationOutput, error) {
	m.calls++
	m.userPrompts = append(m.userPrompts, req.UserPrompt)
	if m.err != nil {
		return nil, m.err
	}
	return m.output, nil
}

func (m *mockEngine) Health(_ context.Context) error {
	return nil
}

func TestServer_StatelessDeliberationConsecutiveCalls(t *testing.T) {
	store := scratchpad.NewStore()
	bud := budgeter.New(budgeter.DefaultConfig())
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

	// Call 1: First deliberation with Task 1
	reqBody1 := model.DeliberateRequest{
		Objective: "Task 1: Isolate network jitter",
		SensoryChunks: []model.SensoryChunk{
			{ID: "c1", Text: "packet delay > 5ms", Salience: 0.8},
		},
		LongTermContext: []string{"Switch port 2 links to Node 3"},
		Observation:     "Ping 192.168.8.183 succeeded with high jitter",
	}
	data1, err := json.Marshal(reqBody1)
	if err != nil {
		t.Fatalf("failed to marshal request 1: %v", err)
	}
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/working/deliberate", bytes.NewReader(data1))
	w1 := httptest.NewRecorder()
	server.ServeHTTP(w1, req1)

	if w1.Code != http.StatusOK {
		t.Fatalf("call 1: expected status 200, got %d: %s", w1.Code, w1.Body.String())
	}

	var resp1 model.DeliberateResponse
	if err := json.NewDecoder(w1.Body).Decode(&resp1); err != nil {
		t.Fatalf("call 1: failed to decode response: %v", err)
	}

	if resp1.Status != "ok" {
		t.Fatalf("call 1: expected status 'ok', got %s", resp1.Status)
	}
	if resp1.StepIndex != 1 {
		t.Fatalf("call 1: expected step index 1, got %d", resp1.StepIndex)
	}
	if resp1.TrajectoryLength != 1 {
		t.Fatalf("call 1: expected trajectory length 1, got %d", resp1.TrajectoryLength)
	}
	if resp1.ActiveGoal != "Task 1: Isolate network jitter" {
		t.Fatalf("call 1: unexpected active goal: %s", resp1.ActiveGoal)
	}
	if resp1.Thought != "Examine sensory log" {
		t.Fatalf("call 1: unexpected thought: %s", resp1.Thought)
	}
	if resp1.ProposedAction != "Read buffer chunk c1" {
		t.Fatalf("call 1: unexpected proposed action: %s", resp1.ProposedAction)
	}
	if resp1.PromptTokens != 50 || resp1.CompletionTokens != 20 || resp1.TotalTokens != 70 {
		t.Fatalf("call 1: unexpected token counts: %d, %d, %d", resp1.PromptTokens, resp1.CompletionTokens, resp1.TotalTokens)
	}

	// Verify store was not mutated
	storeState := store.GetState()
	if len(storeState.Trajectory) != 0 {
		t.Fatalf("call 1: store trajectory should be empty, got %d steps", len(storeState.Trajectory))
	}
	if storeState.ActiveGoal != "" {
		t.Fatalf("call 1: store active goal should be empty, got %s", storeState.ActiveGoal)
	}

	// Call 2: Second deliberation with completely different Task 2
	reqBody2 := model.DeliberateRequest{
		Objective: "Task 2: Diagnose CPU thermal throttling",
		SensoryChunks: []model.SensoryChunk{
			{ID: "c2", Text: "core temp reached 85C", Salience: 0.95},
		},
		LongTermContext: []string{"Thermal throttling threshold is 80C"},
		Observation:     "CPU clock scaled down to 600MHz",
	}
	data2, err := json.Marshal(reqBody2)
	if err != nil {
		t.Fatalf("failed to marshal request 2: %v", err)
	}
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/working/deliberate", bytes.NewReader(data2))
	w2 := httptest.NewRecorder()
	server.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("call 2: expected status 200, got %d: %s", w2.Code, w2.Body.String())
	}

	var resp2 model.DeliberateResponse
	if err := json.NewDecoder(w2.Body).Decode(&resp2); err != nil {
		t.Fatalf("call 2: failed to decode response: %v", err)
	}

	if resp2.Status != "ok" {
		t.Fatalf("call 2: expected status 'ok', got %s", resp2.Status)
	}
	if resp2.StepIndex != 1 {
		t.Fatalf("call 2: expected step index 1, got %d", resp2.StepIndex)
	}
	if resp2.TrajectoryLength != 1 {
		t.Fatalf("call 2: expected trajectory length 1, got %d", resp2.TrajectoryLength)
	}
	if resp2.ActiveGoal != "Task 2: Diagnose CPU thermal throttling" {
		t.Fatalf("call 2: unexpected active goal: %s", resp2.ActiveGoal)
	}

	// Verify prompt isolation between consecutive calls
	if len(mock.userPrompts) != 2 {
		t.Fatalf("expected 2 engine calls, got %d", len(mock.userPrompts))
	}
	prompt1 := mock.userPrompts[0]
	prompt2 := mock.userPrompts[1]

	// Prompt 1 must contain Call 1 details
	if !strings.Contains(prompt1, "Task 1: Isolate network jitter") ||
		!strings.Contains(prompt1, "packet delay > 5ms") ||
		!strings.Contains(prompt1, "Switch port 2 links to Node 3") ||
		!strings.Contains(prompt1, "high jitter") {
		t.Fatalf("prompt 1 missing call 1 details: %s", prompt1)
	}

	// Prompt 2 must contain Call 2 details
	if !strings.Contains(prompt2, "Task 2: Diagnose CPU thermal throttling") ||
		!strings.Contains(prompt2, "core temp reached 85C") ||
		!strings.Contains(prompt2, "Thermal throttling threshold is 80C") ||
		!strings.Contains(prompt2, "scaled down to 600MHz") {
		t.Fatalf("prompt 2 missing call 2 details: %s", prompt2)
	}

	// Prompt 2 MUST NOT contain any Call 1 details (strict isolation)
	if strings.Contains(prompt2, "Task 1") ||
		strings.Contains(prompt2, "packet delay") ||
		strings.Contains(prompt2, "Switch port 2") ||
		strings.Contains(prompt2, "high jitter") {
		t.Fatalf("prompt 2 leaked call 1 state: %s", prompt2)
	}

	// Store must still be completely unmutated
	finalStoreState := store.GetState()
	if len(finalStoreState.Trajectory) != 0 {
		t.Fatalf("store trajectory leaked state: %d steps", len(finalStoreState.Trajectory))
	}
	if finalStoreState.ActiveGoal != "" {
		t.Fatalf("store active goal leaked state: %s", finalStoreState.ActiveGoal)
	}
}

func TestServer_DeliberateInferenceError(t *testing.T) {
	store := scratchpad.NewStore()
	bud := budgeter.New(budgeter.DefaultConfig())
	mock := &mockEngine{
		err: context.DeadlineExceeded,
	}
	server := NewServer(store, bud, mock)

	reqBody := model.DeliberateRequest{
		Objective: "Failing task",
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/working/deliberate", bytes.NewReader(data))
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", w.Code)
	}

	var errResp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if errResp["status"] != "error" {
		t.Fatalf("expected status 'error', got %s", errResp["status"])
	}

	// Verify store was not mutated on inference failure
	state := store.GetState()
	if len(state.Trajectory) != 0 {
		t.Fatalf("expected 0 trajectory steps on error, got %d", len(state.Trajectory))
	}
}

func TestServer_RollbackEndpoint(t *testing.T) {
	store := scratchpad.NewStore()
	bud := budgeter.New(budgeter.DefaultConfig())
	mock := &mockEngine{
		output: &inference.DeliberationOutput{
			Thought: "Step 1 good", Action: "Do step 1", IsComplete: false,
		},
	}
	server := NewServer(store, bud, mock)

	store.SetGoal("Test rollback")
	store.AddStep("Step 1 good", "Do step 1", model.StepStatusSuccess)
	snapID := store.CreateSnapshot("Baseline")
	if snapID != 1 {
		t.Fatalf("expected snapID 1, got %d", snapID)
	}

	store.AddStep("Step 2 bad", "crash", model.StepStatusError)

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
