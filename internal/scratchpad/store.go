package scratchpad

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/model"
)

var (
	ErrSnapshotNotFound = errors.New("snapshot not found")
	ErrStepNotFound     = errors.New("reasoning step not found")
)

// Store provides an in-memory thread-safe working memory store with rollback capability.
type Store struct {
	mu        sync.RWMutex
	state     model.WorkingMemoryState
	snapshots []model.Snapshot
	startTime time.Time
}

// NewStore initializes a blank working memory store.
func NewStore() *Store {
	now := time.Now()
	return &Store{
		state: model.WorkingMemoryState{
			SessionID:        generateSessionID(),
			ActiveGoal:       "",
			SensoryContext:   make([]model.SensoryChunk, 0),
			LongTermContext:  make([]string, 0),
			Trajectory:       make([]model.ReasoningStep, 0),
			CandidateActions: make([]model.CandidateAction, 0),
			Status:           model.StatusIdle,
			CreatedAt:        now,
			UpdatedAt:        now,
		},
		snapshots: make([]model.Snapshot, 0),
		startTime: now,
	}
}

func generateSessionID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("session-%d", time.Now().UnixNano())
	}
	return "wm-" + hex.EncodeToString(b)
}

// GetState returns a deep copy of the active working memory state.
func (s *Store) GetState() model.WorkingMemoryState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cloneState(s.state)
}

// SetGoal updates or initializes the active deliberation objective.
func (s *Store) SetGoal(goal string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.state.ActiveGoal = goal
	s.state.Status = model.StatusDeliberating
	s.state.UpdatedAt = time.Now()
}

// IngestSensory appends filtered sensory chunks from Node 3 without duplicates.
func (s *Store) IngestSensory(chunks []model.SensoryChunk) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	added := 0
	existing := make(map[string]bool)
	for _, c := range s.state.SensoryContext {
		if c.ID != "" {
			existing[c.ID] = true
		}
	}

	for _, c := range chunks {
		if c.ID != "" && existing[c.ID] {
			continue
		}
		if c.Timestamp.IsZero() {
			c.Timestamp = time.Now()
		}
		s.state.SensoryContext = append(s.state.SensoryContext, c)
		added++
	}

	s.state.UpdatedAt = time.Now()
	return added
}

// IngestLongTerm appends retrieved knowledge graph context from Node 1 without duplicates.
func (s *Store) IngestLongTerm(facts []string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	added := 0
	existing := make(map[string]bool)
	for _, f := range s.state.LongTermContext {
		existing[f] = true
	}

	for _, f := range facts {
		if f == "" || existing[f] {
			continue
		}
		s.state.LongTermContext = append(s.state.LongTermContext, f)
		existing[f] = true
		added++
	}

	s.state.UpdatedAt = time.Now()
	return added
}

// AddStep appends a new deliberate reasoning step to the active trajectory.
func (s *Store) AddStep(thought, action string, status string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	if status == "" {
		status = model.StepStatusProposed
	}

	stepIdx := len(s.state.Trajectory) + 1
	step := model.ReasoningStep{
		StepIndex: stepIdx,
		Thought:   thought,
		Action:    action,
		Status:    status,
		Timestamp: time.Now(),
	}

	s.state.Trajectory = append(s.state.Trajectory, step)
	s.state.UpdatedAt = time.Now()
	return stepIdx
}

// RecordObservation records the outcome of an action for a specific step.
func (s *Store) RecordObservation(stepIdx int, observation string, success bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if stepIdx <= 0 || stepIdx > len(s.state.Trajectory) {
		return ErrStepNotFound
	}

	step := &s.state.Trajectory[stepIdx-1]
	step.Observation = observation
	if success {
		step.Status = model.StepStatusSuccess
	} else {
		step.Status = model.StepStatusError
	}
	s.state.UpdatedAt = time.Now()
	return nil
}

// AddCandidateAction appends an uncommitted candidate action.
func (s *Store) AddCandidateAction(action model.CandidateAction) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if action.CreatedAt.IsZero() {
		action.CreatedAt = time.Now()
	}
	s.state.CandidateActions = append(s.state.CandidateActions, action)
	s.state.UpdatedAt = time.Now()
}

// CreateSnapshot captures a snapshot of current working memory for rollback isolation.
func (s *Store) CreateSnapshot(desc string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapID := len(s.snapshots) + 1
	snap := model.Snapshot{
		SnapshotID:  snapID,
		Description: desc,
		State:       s.cloneState(s.state),
		Timestamp:   time.Now(),
	}

	s.snapshots = append(s.snapshots, snap)
	return snapID
}

// Rollback restores working memory to the specified snapshot (or latest if id <= 0).
func (s *Store) Rollback(snapshotID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.snapshots) == 0 {
		return ErrSnapshotNotFound
	}

	var target *model.Snapshot
	if snapshotID <= 0 {
		// Roll back to the most recent snapshot
		target = &s.snapshots[len(s.snapshots)-1]
	} else {
		for i := range s.snapshots {
			if s.snapshots[i].SnapshotID == snapshotID {
				target = &s.snapshots[i]
				break
			}
		}
	}

	if target == nil {
		return ErrSnapshotNotFound
	}

	// Restore state from target snapshot
	s.state = s.cloneState(target.State)
	s.state.UpdatedAt = time.Now()
	return nil
}

// ListSnapshots returns summary list of current snapshots.
func (s *Store) ListSnapshots() []model.Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]model.Snapshot, len(s.snapshots))
	for i, snap := range s.snapshots {
		out[i] = model.Snapshot{
			SnapshotID:  snap.SnapshotID,
			Description: snap.Description,
			Timestamp:   snap.Timestamp,
		}
	}
	return out
}

// Clear flushes the scratchpad state and snapshots, creating a fresh session.
func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	s.state = model.WorkingMemoryState{
		SessionID:        generateSessionID(),
		ActiveGoal:       "",
		SensoryContext:   make([]model.SensoryChunk, 0),
		LongTermContext:  make([]string, 0),
		Trajectory:       make([]model.ReasoningStep, 0),
		CandidateActions: make([]model.CandidateAction, 0),
		Status:           model.StatusIdle,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	s.snapshots = make([]model.Snapshot, 0)
}

// GetTelemetry returns live working memory metrics.
func (s *Store) GetTelemetry() model.ScratchpadTelemetry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return model.ScratchpadTelemetry{
		ActiveSessions:     1,
		ActiveGoal:         s.state.ActiveGoal,
		SensoryItemsCount:  len(s.state.SensoryContext),
		LongTermFactsCount: len(s.state.LongTermContext),
		TrajectorySteps:    len(s.state.Trajectory),
		CandidateActions:   len(s.state.CandidateActions),
		SnapshotCount:      len(s.snapshots),
		EstContextTokens:   s.state.TokenEstimate,
		UptimeSeconds:      int64(time.Since(s.startTime).Seconds()),
		LastUpdated:        s.state.UpdatedAt,
	}
}

// SetTokenEstimate stores the latest computed token estimate.
func (s *Store) SetTokenEstimate(count int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.TokenEstimate = count
}

func (s *Store) cloneState(src model.WorkingMemoryState) model.WorkingMemoryState {
	dst := src

	if src.SensoryContext != nil {
		dst.SensoryContext = make([]model.SensoryChunk, len(src.SensoryContext))
		copy(dst.SensoryContext, src.SensoryContext)
	}
	if src.LongTermContext != nil {
		dst.LongTermContext = make([]string, len(src.LongTermContext))
		copy(dst.LongTermContext, src.LongTermContext)
	}
	if src.Trajectory != nil {
		dst.Trajectory = make([]model.ReasoningStep, len(src.Trajectory))
		copy(dst.Trajectory, src.Trajectory)
	}
	if src.CandidateActions != nil {
		dst.CandidateActions = make([]model.CandidateAction, len(src.CandidateActions))
		copy(dst.CandidateActions, src.CandidateActions)
	}
	return dst
}
