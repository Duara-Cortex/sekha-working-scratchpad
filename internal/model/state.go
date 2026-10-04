package model

import (
	"time"
)

// SensoryChunk represents a filtered, high-salience text piece from Node 3.
type SensoryChunk struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	Salience  float64   `json:"salience"`
	Source    string    `json:"source,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// CandidateAction represents an uncommitted proposed action or tool call.
type CandidateAction struct {
	ID        string                 `json:"id"`
	Type      string                 `json:"type"`
	Payload   map[string]interface{} `json:"payload,omitempty"`
	Committed bool                   `json:"committed"`
	CreatedAt time.Time              `json:"created_at"`
}

// DeliberateRequest defines the input payload for POST /api/v1/working/deliberate.
type DeliberateRequest struct {
	Model           string         `json:"model,omitempty"`
	Objective       string         `json:"objective"`
	SensoryChunks   []SensoryChunk `json:"sensory_chunks,omitempty"`
	LongTermContext []string       `json:"long_term_context,omitempty"`
	Observation     string         `json:"observation,omitempty"`
	MaxTokens       int            `json:"max_tokens,omitempty"`
	Temperature     float64        `json:"temperature,omitempty"`
	// Prepacked marks that the caller already packed chunks and facts to fit; Node 2 keeps
	// everything it was sent unless the rendered prompt does not fit the window.
	Prepacked bool `json:"prepacked,omitempty"`
	// PromptBudgetTokens is the prompt budget the caller packed to (informational).
	PromptBudgetTokens int `json:"prompt_budget_tokens,omitempty"`
}

// ContextUsage reports how much of the request Node 2 actually placed in the prompt.
// A truncated chunk counts as kept, so SensoryKept + SensoryDropped == SensoryReceived.
type ContextUsage struct {
	SensoryReceived       int `json:"sensory_received"`
	SensoryKept           int `json:"sensory_kept"`
	SensoryDropped        int `json:"sensory_dropped"`
	SensoryTruncated      int `json:"sensory_truncated"`
	FactsReceived         int `json:"facts_received"`
	FactsKept             int `json:"facts_kept"`
	EstimatedPromptTokens int `json:"estimated_prompt_tokens"`
	ActualPromptTokens    int `json:"actual_prompt_tokens"`
	PromptWindowTokens    int `json:"prompt_window_tokens"`
}

// DeliberateResponse is returned upon step completion.
type DeliberateResponse struct {
	Status           string            `json:"status"`
	StepIndex        int               `json:"step_index"`
	Thought          string            `json:"thought"`
	ProposedAction   string            `json:"proposed_action,omitempty"`
	IsComplete       bool              `json:"is_complete"`
	CandidateActions []CandidateAction `json:"candidate_actions,omitempty"`
	PromptTokens     int               `json:"prompt_tokens"`
	CompletionTokens int               `json:"completion_tokens"`
	TotalTokens      int               `json:"total_tokens"`
	EvaluationRate   float64           `json:"prompt_eval_rate_tps,omitempty"`
	GenerationRate   float64           `json:"generation_rate_tps,omitempty"`
	ActiveGoal       string            `json:"active_goal"`
	TrajectoryLength int               `json:"trajectory_length"`
	ContextUsage     ContextUsage      `json:"context_usage"`
	Timestamp        time.Time         `json:"timestamp"`
}
