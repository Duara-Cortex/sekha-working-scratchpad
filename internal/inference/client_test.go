package inference

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseDeliberation(t *testing.T) {
	raw := `Thought: The sensor data indicates an unhandled voltage drop on the bus.
We need to check whether the threshold was breached.
Action: Query Node 1 for historical voltage drop incidents
Complete: false`

	thought, action, complete := ParseDeliberation(raw)

	if complete {
		t.Fatalf("expected complete=false")
	}
	expectedThought := "The sensor data indicates an unhandled voltage drop on the bus. We need to check whether the threshold was breached."
	if thought != expectedThought {
		t.Fatalf("unexpected parsed thought: %s", thought)
	}
	expectedAction := "Query Node 1 for historical voltage drop incidents"
	if action != expectedAction {
		t.Fatalf("unexpected parsed action: %s", action)
	}
}

func TestClient_Infer(t *testing.T) {
	var receivedReq openAIChatRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}

		if err := json.NewDecoder(r.Body).Decode(&receivedReq); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		resp := openAIChatResponse{}
		resp.Choices = append(resp.Choices, struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		}{
			Message: struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			}{
				Role:    "assistant",
				Content: "Thought: Plan validated.\nAction: Execute task\nComplete: true",
			},
			FinishReason: "stop",
		})
		resp.Usage.PromptTokens = 45
		resp.Usage.CompletionTokens = 18
		resp.Usage.TotalTokens = 63
		resp.Timings.PromptPerSecond = 31.5
		resp.Timings.PredictedPerSecond = 12.4

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("failed to encode mock response: %v", err)
		}
	}))
	defer ts.Close()

	client, err := NewClient(ts.URL, "qwen2.5-1.5b-instruct", 5*time.Second)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	out, err := client.Infer(context.Background(), Request{
		SystemPrompt: "System prompt",
		UserPrompt:   "User prompt",
		MaxTokens:    128,
		Temperature:  0.2,
	})
	if err != nil {
		t.Fatalf("Infer failed: %v", err)
	}

	if len(receivedReq.Messages) != 2 {
		t.Fatalf("expected exactly 2 messages in single-turn payload, got %d", len(receivedReq.Messages))
	}
	if receivedReq.Messages[0].Role != "system" || receivedReq.Messages[0].Content != "System prompt" {
		t.Fatalf("unexpected system message: %+v", receivedReq.Messages[0])
	}
	if receivedReq.Messages[1].Role != "user" || receivedReq.Messages[1].Content != "User prompt" {
		t.Fatalf("unexpected user message: %+v", receivedReq.Messages[1])
	}

	if !out.IsComplete {
		t.Fatalf("expected isComplete=true")
	}
	if out.Thought != "Plan validated." {
		t.Fatalf("unexpected thought: %s", out.Thought)
	}
	if out.Action != "Execute task" {
		t.Fatalf("unexpected action: %s", out.Action)
	}
	if out.PromptTokens != 45 || out.CompletionTokens != 18 {
		t.Fatalf("unexpected tokens: %d, %d", out.PromptTokens, out.CompletionTokens)
	}
	if out.PromptTPS != 31.5 || out.PredictedTPS != 12.4 {
		t.Fatalf("unexpected timings: %f, %f", out.PromptTPS, out.PredictedTPS)
	}
}

func TestNewClient_RequiresConfig(t *testing.T) {
	if _, err := NewClient("", "model", 5*time.Second); err == nil {
		t.Errorf("expected error when BaseURL is empty, got nil")
	}
	if _, err := NewClient("http://localhost:8080", "", 5*time.Second); err == nil {
		t.Errorf("expected error when Model is empty, got nil")
	}
	if _, err := NewClient("http://localhost:8080", "model", 0); err == nil {
		t.Errorf("expected error when Timeout is <= 0, got nil")
	}
}
