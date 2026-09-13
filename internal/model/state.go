package model

import (
	"time"
)

// Status constants for reasoning steps and working memory lifecycle.
const (
	StatusIdle         = "idle"
	StatusDeliberating = "deliberating"
	StatusReady        = "ready"
	StatusError        = "error"
	StatusCompleted    = "completed"

	StepStatusProposed  = "proposed"
	StepStatusExecuting = "executing"
	StepStatusSuccess   = "success"
	StepStatusError     = "error"
	StepStatusRolledBack= "rolled_back"
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
	StepIndex   int       `json:"step_index"`
	Thought     string    `json:"thought"`
	Action      string    `json:"action,omitempty"`
	Observation string    `json:"observation,omitempty"`
	Status      string    `json:"status"`
	Timestamp   time.Time `json:"timestamp"`
}

// CandidateAction represents an uncommitted proposed action or tool call.
type CandidateAction struct {
	ID          string                 `json:"id"`
	Type        string                 `json:"type"`
	Payload     map[string]interface{} `json:"payload,omitempty"`
	Committed   bool                   `json:"committed"`
	CreatedAt   time.Time              `json:"created_at"`
}

// WorkingMemoryState is the core active deliberation state on Node 2.
type WorkingMemoryState struct {
	SessionID        string            `json:"session_id"`
	ActiveGoal       string            `json:"active_goal"`
	SensoryContext   []SensoryChunk    `json:"sensory_context"`
	LongTermContext  []string          `json:"long_term_context"`
	Trajectory       []ReasoningStep   `json:"trajectory"`
	CandidateActions []CandidateAction `json:"candidate_actions"`
	Status           string            `json:"status"`
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
	Objective        string            `json:"objective"`
	SensoryChunks    []SensoryChunk    `json:"sensory_chunks,omitempty"`
	LongTermContext  []string          `json:"long_term_context,omitempty"`
	Observation      string            `json:"observation,omitempty"`
	MaxTokens        int               `json:"max_tokens,omitempty"`
	Temperature      float64           `json:"temperature,omitempty"`
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
	Timestamp        time.Time         `json:"timestamp"`
}

// ScratchpadTelemetry provides high-level memory stats for cluster telemetry.
type ScratchpadTelemetry struct {
	ActiveSessions    int       `json:"active_sessions"`
	ActiveGoal        string    `json:"active_goal"`
	SensoryItemsCount int       `json:"sensory_items_count"`
	LongTermFactsCount int      `json:"long_term_facts_count"`
	TrajectorySteps   int       `json:"trajectory_steps"`
	CandidateActions  int       `json:"candidate_actions"`
	SnapshotCount     int       `json:"snapshot_count"`
	EstContextTokens  int       `json:"est_context_tokens"`
	UptimeSeconds     int64     `json:"uptime_seconds"`
	LastUpdated       time.Time `json:"last_updated"`
}
