package scratchpad

import (
	"testing"

	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/model"
)

func TestStore_BasicOperations(t *testing.T) {
	store := NewStore()

	store.SetGoal("Investigate sensor anomaly on Node 3")
	state := store.GetState()
	if state.ActiveGoal != "Investigate sensor anomaly on Node 3" {
		t.Fatalf("unexpected goal: %s", state.ActiveGoal)
	}
	if state.Status != model.StatusDeliberating {
		t.Fatalf("expected status %s, got %s", model.StatusDeliberating, state.Status)
	}

	// Add sensory chunk
	chunks := []model.SensoryChunk{
		{ID: "c1", Text: "packet loss detected on eth0", Salience: 0.85},
	}
	added := store.IngestSensory(chunks)
	if added != 1 {
		t.Fatalf("expected 1 chunk added, got %d", added)
	}

	// Deduplication check
	added2 := store.IngestSensory(chunks)
	if added2 != 0 {
		t.Fatalf("expected duplicate to be ignored, got %d", added2)
	}

	// Add long term fact
	facts := []string{"Node 3 hosts sensory ring buffer on port 8081"}
	fAdded := store.IngestLongTerm(facts)
	if fAdded != 1 {
		t.Fatalf("expected 1 fact added, got %d", fAdded)
	}

	// Add reasoning step
	idx := store.AddStep("Check physical link status", "ping -c 3 192.168.8.183", model.StepStatusExecuting)
	if idx != 1 {
		t.Fatalf("expected step index 1, got %d", idx)
	}

	err := store.RecordObservation(1, "3 packets transmitted, 0 received, 100% loss", false)
	if err != nil {
		t.Fatalf("failed to record observation: %v", err)
	}

	state = store.GetState()
	if len(state.Trajectory) != 1 {
		t.Fatalf("expected trajectory length 1, got %d", len(state.Trajectory))
	}
	if state.Trajectory[0].Status != model.StepStatusError {
		t.Fatalf("expected step status %s, got %s", model.StepStatusError, state.Trajectory[0].Status)
	}
}

func TestStore_SnapshotAndRollback(t *testing.T) {
	store := NewStore()
	store.SetGoal("Execute cluster routine")

	store.AddStep("Step 1 valid plan", "noop", model.StepStatusSuccess)
	snapID := store.CreateSnapshot("Baseline before risky hypothesis")
	if snapID != 1 {
		t.Fatalf("expected snapID 1, got %d", snapID)
	}

	// Add flawed step
	store.AddStep("Flawed step 2", "drop table users", model.StepStatusError)
	state := store.GetState()
	if len(state.Trajectory) != 2 {
		t.Fatalf("expected 2 steps before rollback, got %d", len(state.Trajectory))
	}

	// Rollback
	err := store.Rollback(snapID)
	if err != nil {
		t.Fatalf("rollback failed: %v", err)
	}

	state = store.GetState()
	if len(state.Trajectory) != 1 {
		t.Fatalf("expected 1 step after rollback, got %d", len(state.Trajectory))
	}
	if state.Trajectory[0].Thought != "Step 1 valid plan" {
		t.Fatalf("expected original step restored, got: %s", state.Trajectory[0].Thought)
	}
}

func TestStore_Clear(t *testing.T) {
	store := NewStore()
	store.SetGoal("Task to clear")
	store.AddStep("Thought 1", "Action 1", model.StepStatusSuccess)
	store.CreateSnapshot("Snap 1")

	store.Clear()

	state := store.GetState()
	if state.ActiveGoal != "" {
		t.Fatalf("expected empty goal after clear, got %s", state.ActiveGoal)
	}
	if len(state.Trajectory) != 0 {
		t.Fatalf("expected empty trajectory after clear, got %d", len(state.Trajectory))
	}
	snaps := store.ListSnapshots()
	if len(snaps) != 0 {
		t.Fatalf("expected 0 snapshots after clear, got %d", len(snaps))
	}
}
