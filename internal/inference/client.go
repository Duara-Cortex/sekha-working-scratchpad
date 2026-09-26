package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Request defines the input parameters for a model inference call.
type Request struct {
	Model        string
	SystemPrompt string
	UserPrompt   string
	MaxTokens    int
	Temperature  float64
}

// Engine defines the interface for language model inference.
type Engine interface {
	Infer(ctx context.Context, req Request) (*DeliberationOutput, error)
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

// Config defines the explicit settings required to connect to an OpenAI-compatible inference server.
type Config struct {
	BaseURL    string
	Model      string
	Timeout    time.Duration
	HTTPClient *http.Client
}

// Client connects to any OpenAI-compatible inference server (llama-server, vLLM, Ollama, etc.).
type Client struct {
	baseURL    string
	modelName  string
	httpClient *http.Client
}

// LlamaClient provides a backwards-compatible type alias for Client.
type LlamaClient = Client

// NewClient initializes a client with explicit base URL, model name, and timeout.
func NewClient(baseURL, modelName string, timeout time.Duration) (*Client, error) {
	return NewClientWithConfig(Config{
		BaseURL: baseURL,
		Model:   modelName,
		Timeout: timeout,
	})
}

// NewClientWithConfig initializes a client using a Config struct. Returns an error if required settings are missing.
func NewClientWithConfig(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, errors.New("inference BaseURL is required and cannot be empty")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("inference Model is required and cannot be empty")
	}
	if cfg.Timeout <= 0 {
		return nil, errors.New("inference Timeout must be greater than zero")
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: cfg.Timeout,
		}
	}

	return &Client{
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		modelName:  strings.TrimSpace(cfg.Model),
		httpClient: httpClient,
	}, nil
}

// Model returns the configured default model name for the client.
func (c *Client) Model() string {
	return c.modelName
}

// BaseURL returns the configured base URL for the client.
func (c *Client) BaseURL() string {
	return c.baseURL
}

type openAIChatRequest struct {
	Model       string              `json:"model"`
	Messages    []openAIChatMessage `json:"messages"`
	Temperature float64             `json:"temperature"`
	MaxTokens   int                 `json:"max_tokens"`
	Stream      bool                `json:"stream"`
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

// Infer sends the prompt payload to the inference engine and extracts deliberation fields.
func (c *Client) Infer(ctx context.Context, req Request) (*DeliberationOutput, error) {
	modelName := strings.TrimSpace(req.Model)
	if modelName == "" {
		modelName = c.modelName
	}
	if modelName == "" {
		return nil, errors.New("model name is required for inference")
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 256
	}

	reqBody := openAIChatRequest{
		Model: modelName,
		Messages: []openAIChatMessage{
			{Role: "system", Content: req.SystemPrompt},
			{Role: "user", Content: req.UserPrompt},
		},
		Temperature: req.Temperature,
		MaxTokens:   maxTokens,
		Stream:      false,
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal inference request: %w", err)
	}

	endpointURL := fmt.Sprintf("%s/v1/chat/completions", c.baseURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create inference HTTP request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("inference call failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, fmt.Errorf("inference endpoint returned %d (failed to read response: %w)", resp.StatusCode, readErr)
		}
		return nil, fmt.Errorf("inference endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
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

// Health checks reachability of the inference engine endpoint.
func (c *Client) Health(ctx context.Context) error {
	endpointURL := fmt.Sprintf("%s/health", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpointURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create health check request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("health check request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("inference server health check returned status %d", resp.StatusCode)
	}
	return nil
}

// ParseDeliberation parses structured thought, action, and completion status from model output.
func ParseDeliberation(raw string) (thought, action string, isComplete bool) {
	lines := strings.Split(raw, "\n")
	var thoughtLines []string
	var actionLines []string
	mode := "thought"

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)

		switch {
		case strings.HasPrefix(lower, "thought:"):
			mode = "thought"
			thoughtLines = append(thoughtLines, strings.TrimSpace(trimmed[len("thought:"):]))
		case strings.HasPrefix(lower, "action:"):
			mode = "action"
			actionLines = append(actionLines, strings.TrimSpace(trimmed[len("action:"):]))
		case strings.HasPrefix(lower, "complete:"):
			val := strings.TrimSpace(lower[len("complete:"):])
			if strings.HasPrefix(val, "true") || strings.HasPrefix(val, "yes") {
				isComplete = true
			}
		default:
			if mode == "thought" {
				thoughtLines = append(thoughtLines, trimmed)
			} else if mode == "action" {
				actionLines = append(actionLines, trimmed)
			}
		}
	}

	thought = strings.TrimSpace(strings.Join(thoughtLines, " "))
	action = strings.TrimSpace(strings.Join(actionLines, " "))

	if thought == "" && action == "" {
		return raw, "Continue deliberation", isComplete
	}
	if action == "" {
		return thought, "Next reasoning step", isComplete
	}
	return thought, action, isComplete
}
