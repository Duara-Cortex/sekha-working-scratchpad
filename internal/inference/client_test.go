package inference

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseDeliberation(t *testing.T) {
	raw := `Thought: The sensor data indicates an unhandled voltage drop on the bus.
We need to check whether the threshold was breached.
Action: Query Node 1 for historical voltage drop incidents
Complete: false`

	thought, action, complete := ParseDeliberation(raw)

	if !complete == false {
		t.Fatalf("expected complete=false")
	}
	if thought != "The sensor data indicates an unhandled voltage drop on the bus. We need to check whether the threshold was breached." {
		t.Fatalf("unexpected parsed thought: %s", thought)
	}
	if action != "Query Node 1 for historical voltage drop incidents" {
		t.Fatalf("unexpected parsed action: %s", action)
	}
}

func TestLlamaClient_Infer(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
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
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	client := NewClient(ts.URL, 0)
	out, err := client.Infer(context.Background(), "System prompt", "User prompt", 128, 0.2)
	if err != nil {
		t.Fatalf("Infer failed: %v", err)
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
