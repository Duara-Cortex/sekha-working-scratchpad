package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Engine defines the interface for language model inference.
type Engine interface {
	Infer(ctx context.Context, systemPrompt, userPrompt string, maxTokens int, temperature float64) (*DeliberationOutput, error)
	Health(ctx context.Context) error
}

// DeliberationOutput contains structured parsing and execution telemetry from the SLM.
type DeliberationOutput struct {
	RawContent       string
	Thought          string
	Action           string
	IsComplete       bool
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	PromptTPS        float64
	PredictedTPS     float64
}

// LlamaClient connects to local llama-server running on Node 2 (port 8082).
type LlamaClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient initializes a client pointing to llama-server.
func NewClient(baseURL string, timeout time.Duration) *LlamaClient {
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8082"
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &LlamaClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

type openAIChatRequest struct {
	Model       string               `json:"model,omitempty"`
	Messages    []openAIChatMessage  `json:"messages"`
	Temperature float64              `json:"temperature"`
	MaxTokens   int                  `json:"max_tokens"`
	Stream      bool                 `json:"stream"`
}

type openAIChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatResponse struct {
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Timings struct {
		PromptPerSecond    float64 `json:"prompt_per_second"`
		PredictedPerSecond float64 `json:"predicted_per_second"`
	} `json:"timings"`
}

// Infer sends the prompt payload to llama-server and extracts deliberation fields.
func (c *LlamaClient) Infer(ctx context.Context, systemPrompt, userPrompt string, maxTokens int, temperature float64) (*DeliberationOutput, error) {
	if maxTokens <= 0 {
		maxTokens = 256
	}
	if temperature <= 0 {
		temperature = 0.2
	}

	reqBody := openAIChatRequest{
		Model: "qwen2.5-1.5b-instruct",
		Messages: []openAIChatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Temperature: temperature,
		MaxTokens:   maxTokens,
		Stream:      false,
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/v1/chat/completions", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("inference call failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("inference endpoint returned %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var chatResp openAIChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return nil, fmt.Errorf("failed to decode inference response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("empty choices returned from inference engine")
	}

	content := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	thought, action, isComplete := ParseDeliberation(content)

	return &DeliberationOutput{
		RawContent:       content,
		Thought:          thought,
		Action:           action,
		IsComplete:       isComplete,
		PromptTokens:     chatResp.Usage.PromptTokens,
		CompletionTokens: chatResp.Usage.CompletionTokens,
		TotalTokens:      chatResp.Usage.TotalTokens,
		PromptTPS:        chatResp.Timings.PromptPerSecond,
		PredictedTPS:     chatResp.Timings.PredictedPerSecond,
	}, nil
}

// Health checks reachability of the llama-server endpoint.
func (c *LlamaClient) Health(ctx context.Context) error {
	url := fmt.Sprintf("%s/health", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("llama-server health check returned status %d", resp.StatusCode)
	}
	return nil
}

// ParseDeliberation parses structured thought, action, and completion status from model output.
func ParseDeliberation(raw string) (thought string, action string, isComplete bool) {
	lines := strings.Split(raw, "\n")
	var thoughtLines []string
	var actionLines []string
	mode := "thought"

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)

		if strings.HasPrefix(lower, "thought:") {
			mode = "thought"
			thoughtLines = append(thoughtLines, strings.TrimSpace(trimmed[8:]))
			continue
		} else if strings.HasPrefix(lower, "action:") {
			mode = "action"
			actionLines = append(actionLines, strings.TrimSpace(trimmed[7:]))
			continue
		} else if strings.HasPrefix(lower, "complete:") {
			val := strings.TrimSpace(lower[9:])
			if strings.HasPrefix(val, "true") || strings.HasPrefix(val, "yes") {
				isComplete = true
			}
			continue
		}

		if mode == "thought" && trimmed != "" {
			thoughtLines = append(thoughtLines, trimmed)
		} else if mode == "action" && trimmed != "" {
			actionLines = append(actionLines, trimmed)
		}
	}

	thought = strings.TrimSpace(strings.Join(thoughtLines, " "))
	action = strings.TrimSpace(strings.Join(actionLines, " "))

	// Fallback if model didn't format explicitly
	if thought == "" && action == "" {
		thought = raw
		action = "Continue deliberation"
	} else if action == "" {
		action = "Next reasoning step"
	}

	return thought, action, isComplete
}
