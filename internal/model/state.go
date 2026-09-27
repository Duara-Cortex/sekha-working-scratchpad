package model

import (
	"time"
)

// Status represents the operational lifecycle status of working memory.
type Status string

// Status constants for reasoning steps and working memory lifecycle.
const (
	StatusIdle         Status = "idle"
	StatusDeliberating Status = "deliberating"
	StatusReady        Status = "ready"
	StatusError        Status = "error"
	StatusCompleted    Status = "completed"
)

// StepStatus represents the progression status of an individual reasoning step.
type StepStatus string

// StepStatus constants for individual reasoning step transitions.
const (
	StepStatusProposed   StepStatus = "proposed"
	StepStatusExecuting  StepStatus = "executing"
	StepStatusSuccess    StepStatus = "success"
	StepStatusError      StepStatus = "error"
	StepStatusRolledBack StepStatus = "rolled_back"
)

// SensoryChunk represents a filtered, high-salience text piece from Node 3.
type SensoryChunk struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	Salience  float64   `json:"salience"`
	Source    string    `json:"source,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// ReasoningStep captures a single deliberate reasoning step in the trajectory.
type ReasoningStep struct {
	StepIndex   int        `json:"step_index"`
	Thought     string     `json:"thought"`
	Action      string     `json:"action,omitempty"`
	Observation string     `json:"observation,omitempty"`
	Status      StepStatus `json:"status"`
	Timestamp   time.Time  `json:"timestamp"`
}

// CandidateAction represents an uncommitted proposed action or tool call.
type CandidateAction struct {
	ID        string                 `json:"id"`
	Type      string                 `json:"type"`
	Payload   map[string]interface{} `json:"payload,omitempty"`
	Committed bool                   `json:"committed"`
	CreatedAt time.Time              `json:"created_at"`
}

// WorkingMemoryState is the core active deliberation state on Node 2.
type WorkingMemoryState struct {
	SessionID        string            `json:"session_id"`
	ActiveGoal       string            `json:"active_goal"`
	SensoryContext   []SensoryChunk    `json:"sensory_context"`
	LongTermContext  []string          `json:"long_term_context"`
	Trajectory       []ReasoningStep   `json:"trajectory"`
	CandidateActions []CandidateAction `json:"candidate_actions"`
	Status           Status            `json:"status"`
	TokenEstimate    int               `json:"token_estimate"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
}

// Snapshot preserves working memory state for error isolation and rollback.
type Snapshot struct {
	SnapshotID  int                `json:"snapshot_id"`
	Description string             `json:"description"`
	State       WorkingMemoryState `json:"state"`
	Timestamp   time.Time          `json:"timestamp"`
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

// ScratchpadTelemetry provides high-level memory stats for cluster telemetry.
type ScratchpadTelemetry struct {
	ActiveSessions     int       `json:"active_sessions"`
	ActiveGoal         string    `json:"active_goal"`
	SensoryItemsCount  int       `json:"sensory_items_count"`
	LongTermFactsCount int       `json:"long_term_facts_count"`
	TrajectorySteps    int       `json:"trajectory_steps"`
	CandidateActions   int       `json:"candidate_actions"`
	SnapshotCount      int       `json:"snapshot_count"`
	EstContextTokens   int       `json:"est_context_tokens"`
	UptimeSeconds      int64     `json:"uptime_seconds"`
	LastUpdated        time.Time `json:"last_updated"`
}
