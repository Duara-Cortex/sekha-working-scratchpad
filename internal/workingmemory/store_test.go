package workingmemory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Acceptance 1: a 10-turn dialogue with 3 strong turns is queued, processed, and committed with
// its 3 thoughts after the idle timeout.
func TestAsyncFlowCommitsAllTurnsAndThoughtsAfterIdle(t *testing.T) {
	h := newHarness(t, Options{IdleTimeout: 30 * time.Second})
	h.store.AddChunks("e1", dialogue("m1", 1, 10, 2, 5, 8))

	st := h.status(t, "m1")
	if st.Counts.Items != 10 || st.Counts.Strong != 3 || st.Counts.Queued != 3 {
		t.Fatalf("after ingest: %+v", st.Counts)
	}

	h.processN(t, 3)
	st = h.status(t, "m1")
	if st.Counts.Done != 3 || st.Counts.Queued != 0 || st.Counts.Thoughts != 3 || len(st.Thoughts) != 3 {
		t.Fatalf("after processing: %+v, %d thoughts", st.Counts, len(st.Thoughts))
	}
	for _, th := range st.Thoughts {
		if th.Speaker != SpeakerWorkingMemory || th.Origin != OriginThought {
			t.Fatalf("thought not labelled: %+v", th)
		}
	}

	if due := h.store.Sweep(); len(due) != 0 {
		t.Fatalf("committed before the idle timeout: %v", due)
	}
	h.clock.Advance(31 * time.Second)
	h.proc.SweepOnce(context.Background())

	b := h.committer.only(t)
	if len(b.Items) != 13 || b.Reason != "idle_timeout" {
		t.Fatalf("expected 10 turns + 3 thoughts committed on idle, got %d items (%s)", len(b.Items), b.Reason)
	}
	thoughts := 0
	for _, it := range b.Items {
		if it.Origin == OriginThought {
			thoughts++
			if len(it.Links) == 0 || it.Links[0].Rel != RelDerivedFrom {
				t.Fatalf("thought is not linked to its chunk: %+v", it)
			}
		}
	}
	if thoughts != 3 {
		t.Fatalf("expected 3 thoughts in the commit, got %d", thoughts)
	}
}

// Acceptance 2 (Task 27 canary): B's prompts are identical whether or not A ran first, and carry
// no trace of A.
func TestPromptsAreStatelessAcrossMemories(t *testing.T) {
	runB := func(runAFirst bool) ([]string, []string) {
		h := newHarness(t, Options{})
		if runAFirst {
			a := dialogue("mA", 1, 3, 0, 1, 2)
			a[1].Text = "CANARY-7f3a the lantern backup ran at 02:00"
			h.store.AddChunks("e1", a)
			h.processN(t, 3)
		}
		h.store.AddChunks("e1", dialogue("mB", 100, 4, 1, 3))
		before := h.engine.callCount()
		h.processN(t, 2)
		h.engine.mu.Lock()
		defer h.engine.mu.Unlock()
		if h.engine.calls-before != 2 {
			t.Fatalf("expected 2 calls for B, got %d", h.engine.calls-before)
		}
		return h.engine.systems[before:], h.engine.users[before:]
	}

	sysAlone, userAlone := runB(false)
	sysAfter, userAfter := runB(true)
	for i := range userAlone {
		if sysAlone[i] != sysAfter[i] || userAlone[i] != userAfter[i] {
			t.Fatalf("B's prompt %d changed when A ran first:\n--- alone ---\n%s\n--- after A ---\n%s", i, userAlone[i], userAfter[i])
		}
		for _, trace := range []string{"CANARY", "mA", "task of mA"} {
			if strings.Contains(userAfter[i], trace) || strings.Contains(sysAfter[i], trace) {
				t.Fatalf("B's prompt %d contains %q:\n%s", i, trace, userAfter[i])
			}
		}
	}
}

// Acceptance 3: a correction reaches Node 1 linked from the original by superseded_by; a forgotten
// item never does.
func TestCorrectSupersedesAndForgetRemoves(t *testing.T) {
	h := newHarness(t, Options{})
	h.store.AddChunks("e1", dialogue("m1", 1, 4))
	items, _ := h.store.Items("m1")
	orig := itemByText(t, items, "m1 turn 1")
	gone := itemByText(t, items, "m1 turn 2")

	corr, err := h.store.Correct(context.Background(), "m1", orig.ItemID, "m1 turn 1, corrected", "agent-7")
	if err != nil {
		t.Fatal(err)
	}
	if corr.Source != "harness" || corr.Speaker != "agent-7" || corr.Origin != OriginHarness || corr.ScoreStatus != "scored" {
		t.Fatalf("correction not labelled/scored: %+v", corr)
	}
	if _, err := h.store.Correct(context.Background(), "m1", orig.ItemID, "again", "agent-7"); !errors.Is(err, ErrSuperseded) {
		t.Fatalf("correcting a superseded item: got %v", err)
	}
	if err := h.store.Forget("m1", gone.ItemID); err != nil {
		t.Fatal(err)
	}

	if _, err := h.store.Commit(context.Background(), "m1", "explicit"); err != nil {
		t.Fatal(err)
	}
	b := h.committer.only(t)
	o := commitItemByText(t, b, "m1 turn 1")
	c := commitItemByText(t, b, "m1 turn 1, corrected")
	if !hasLink(o.Links, RelSupersededBy, c.ItemID) || !hasLink(c.Links, RelSupersedes, o.ItemID) {
		t.Fatalf("supersede links missing: original %+v, correction %+v", o.Links, c.Links)
	}
	if o.Seq == 0 || c.Seq != 0 || c.Origin != OriginHarness || c.Source != "harness" || c.Speaker != "agent-7" {
		t.Fatalf("original seq %d, correction %+v", o.Seq, c)
	}
	for _, it := range b.Items {
		if it.Text == "m1 turn 2" {
			t.Fatal("forgotten item reached Node 1")
		}
	}
	if len(b.Items) != 4 {
		t.Fatalf("expected 3 turns + 1 correction, got %d", len(b.Items))
	}
}

// Acceptance 4: a 1-chunk memory queued behind a 40-chunk memory finishes within 2 model calls.
func TestQueueRotatesBetweenMemories(t *testing.T) {
	h := newHarness(t, Options{})
	strong := make([]int, 40)
	for i := range strong {
		strong[i] = i
	}
	h.store.AddChunks("e1", dialogue("big", 1, 40, strong...))
	h.store.AddChunks("e1", dialogue("small", 100, 1, 0))

	for calls := 1; calls <= 2; calls++ {
		h.processN(t, 1)
		if st := h.status(t, "small"); st.Counts.Done == 1 {
			return
		}
	}
	t.Fatal("the 1-chunk memory was not done within 2 model calls")
}

// The usual production case: the big memory's first chunk is already in flight when the small
// memory arrives.
func TestQueueRotationWhenNewMemoryArrivesMidCall(t *testing.T) {
	h := newHarness(t, Options{})
	strong := make([]int, 40)
	for i := range strong {
		strong[i] = i
	}
	h.store.AddChunks("e1", dialogue("big", 1, 40, strong...))
	first, err := h.store.NextJob(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h.store.AddChunks("e1", dialogue("small", 100, 1, 0))
	h.store.FinishJob(first, Result{Thought: "t"})

	next, err := h.store.NextJob(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if next.MemoryID != "small" {
		t.Fatalf("second call served %s, want small", next.MemoryID)
	}
}

func TestQueueRotationSurvivesCommitOfServedMemory(t *testing.T) {
	h := newHarness(t, Options{})
	h.store.AddChunks("e1", dialogue("a", 1, 2, 0, 1))
	h.store.AddChunks("e1", dialogue("b", 10, 2, 0, 1))
	h.store.AddChunks("e1", dialogue("c", 20, 2, 0, 1))

	job, _ := h.store.NextJob(context.Background()) // a
	h.store.FinishJob(job, Result{Thought: "t"})
	job, _ = h.store.NextJob(context.Background()) // b
	h.store.FinishJob(job, Result{Thought: "t"})
	if _, err := h.store.Commit(context.Background(), "b", "explicit"); err != nil {
		t.Fatal(err)
	}
	job, _ = h.store.NextJob(context.Background())
	if job.MemoryID != "c" {
		t.Fatalf("after b was committed the queue served %s, want c", job.MemoryID)
	}
}

func TestQueueRotationServesEachMemoryInTurn(t *testing.T) {
	h := newHarness(t, Options{})
	h.store.AddChunks("e1", dialogue("a", 1, 3, 0, 1, 2))
	h.store.AddChunks("e1", dialogue("b", 10, 2, 0, 1))
	h.store.AddChunks("e1", dialogue("c", 20, 1, 0))

	var order []string
	for i := 0; i < 6; i++ {
		job, err := h.store.NextJob(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		order = append(order, job.MemoryID)
		h.store.FinishJob(job, Result{Thought: "t"})
	}
	if got := strings.Join(order, ","); got != "a,b,c,a,b,a" {
		t.Fatalf("rotation order = %s", got)
	}
}

// Acceptance 5: a chunk not processed within the wait limit is committed unprocessed with its
// starting strength.
func TestTimedOutChunkIsCommittedUnprocessed(t *testing.T) {
	h := newHarness(t, Options{WaitLimit: 5 * time.Second, IdleTimeout: 30 * time.Second})
	chunks := dialogue("m1", 1, 2, 1)
	chunks[1].TaskScore = score(0.73)
	h.store.AddChunks("e1", chunks)

	h.clock.Advance(6 * time.Second)
	h.store.Sweep()
	if st := h.status(t, "m1"); st.Counts.TimedOut != 1 || st.Counts.Queued != 0 {
		t.Fatalf("expected the chunk timed out: %+v", st.Counts)
	}

	h.clock.Advance(31 * time.Second)
	h.proc.SweepOnce(context.Background())
	b := h.committer.only(t)
	it := commitItemByText(t, b, "m1 turn 1")
	if it.Processing != StatusTimedOut || it.Strength != 0.73 {
		t.Fatalf("timed-out chunk committed as %+v", it)
	}
	if h.engine.callCount() != 0 {
		t.Fatal("a timed-out chunk reached the model")
	}
}

// Acceptance 6: an explicit commit during a model call completes only after the call ends, and
// includes that call's thought.
func TestCommitWaitsForInFlightCall(t *testing.T) {
	h := newHarness(t, Options{})
	h.engine.block = make(chan struct{})
	h.engine.started = make(chan struct{}, 1)
	h.store.AddChunks("e1", dialogue("m1", 1, 3, 1))

	callDone := make(chan error, 1)
	go func() { callDone <- h.proc.ProcessNext(context.Background()) }()
	<-h.engine.started

	commitDone := make(chan CommitSummary, 1)
	go func() {
		sum, err := h.store.Commit(context.Background(), "m1", "explicit")
		if err != nil {
			t.Error(err)
		}
		commitDone <- sum
	}()

	select {
	case <-commitDone:
		t.Fatal("commit completed while the model call was in flight")
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := h.store.Add(context.Background(), "m1", AddRequest{Text: "late edit", Agent: "agent"}); !errors.Is(err, ErrCommitting) {
		t.Fatalf("edit during commit: got %v", err)
	}

	close(h.engine.block)
	if err := <-callDone; err != nil {
		t.Fatal(err)
	}
	sum := <-commitDone
	if sum.Items != 4 {
		t.Fatalf("expected 3 turns + the in-flight call's thought, got %d items", sum.Items)
	}
	b := h.committer.only(t)
	found := false
	for _, it := range b.Items {
		if it.Origin == OriginThought {
			found = true
		}
	}
	if !found {
		t.Fatal("the in-flight call's thought was not committed")
	}
}

// Acceptance 7: after commit the memory's entries no longer exist in the temp store.
func TestTempStoreEmptyAfterCommit(t *testing.T) {
	h := newHarness(t, Options{})
	h.store.AddChunks("e1", dialogue("m1", 1, 5, 0))
	h.processN(t, 1)
	if _, err := h.store.Commit(context.Background(), "m1", "explicit"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Items("m1"); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatalf("items still listable after commit: %v", err)
	}
	if st := h.store.Stats(); st.Memories != 0 || st.Items != 0 {
		t.Fatalf("store not empty after commit: %+v", st)
	}
	st := h.status(t, "m1")
	if st.State != StateCommitted || st.Counts.Items != 0 || st.Committed == nil || st.Committed.Items != 6 {
		t.Fatalf("status after commit: %+v", st)
	}
	// A second commit reports the finished one instead of failing.
	if sum, err := h.store.Commit(context.Background(), "m1", "explicit"); err != nil || sum.Items != 6 {
		t.Fatalf("repeat commit: %+v, %v", sum, err)
	}
	if len(h.committer.batches) != 1 {
		t.Fatal("repeat commit wrote again")
	}
}

func TestExplicitCommitCommitsQueuedChunksUnprocessed(t *testing.T) {
	h := newHarness(t, Options{})
	h.store.AddChunks("e1", dialogue("m1", 1, 3, 0, 2))
	if _, err := h.store.Commit(context.Background(), "m1", "explicit"); err != nil {
		t.Fatal(err)
	}
	b := h.committer.only(t)
	for _, text := range []string{"m1 turn 0", "m1 turn 2"} {
		if it := commitItemByText(t, b, text); it.Processing != StatusUnprocessed || it.Strength != 0.8 {
			t.Fatalf("%s committed as %+v", text, it)
		}
	}
	if it := commitItemByText(t, b, "m1 turn 1"); it.Processing != "" {
		t.Fatalf("weak chunk has a processing status: %+v", it)
	}
}

func TestFailedCommitKeepsMemoryAndRetries(t *testing.T) {
	h := newHarness(t, Options{IdleTimeout: 10 * time.Second})
	h.store.AddChunks("e1", dialogue("m1", 1, 2))
	h.committer.err = errBoom

	if _, err := h.store.Commit(context.Background(), "m1", "explicit"); !errors.Is(err, errBoom) {
		t.Fatalf("expected the commit error, got %v", err)
	}
	st := h.status(t, "m1")
	if st.State != StateOpen || st.Counts.Items != 2 || st.LastCommitError == "" {
		t.Fatalf("memory not kept after failed commit: %+v", st)
	}

	h.committer.err = nil
	h.clock.Advance(5 * time.Second)
	if due := h.store.Sweep(); len(due) != 0 {
		t.Fatalf("retried before the backoff: %v", due)
	}
	h.clock.Advance(6 * time.Second)
	h.proc.SweepOnce(context.Background())
	if b := h.committer.only(t); len(b.Items) != 2 {
		t.Fatalf("retry committed %d items", len(b.Items))
	}
}

func TestSnapshotRules(t *testing.T) {
	t.Run("forgotten mid-call drops the reinforcement", func(t *testing.T) {
		h := newHarness(t, Options{})
		h.store.AddChunks("e1", dialogue("m1", 1, 3, 1))
		items, _ := h.store.Items("m1")
		prev := itemByText(t, items, "m1 turn 0")
		h.engine.used = "c1, x1"
		h.engine.onInfer = func() {
			if err := h.store.Forget("m1", prev.ItemID); err != nil {
				t.Error(err)
			}
		}
		h.processN(t, 1)

		items, _ = h.store.Items("m1")
		for _, it := range items {
			if it.Text == "m1 turn 0" {
				t.Fatal("forgotten item came back")
			}
			if it.Origin == OriginThought {
				if len(it.Uses) != 1 || it.Uses[0] == prev.ItemID {
					t.Fatalf("thought still uses the forgotten item: %v", it.Uses)
				}
			}
		}
	})

	t.Run("corrected mid-call reinforces the new version", func(t *testing.T) {
		h := newHarness(t, Options{})
		h.store.AddChunks("e1", dialogue("m1", 1, 3, 1))
		items, _ := h.store.Items("m1")
		prev := itemByText(t, items, "m1 turn 0")
		h.engine.used = "x1"
		var corrID string
		h.engine.onInfer = func() {
			c, err := h.store.Correct(context.Background(), "m1", prev.ItemID, "turn 0 corrected", "agent")
			if err != nil {
				t.Error(err)
			}
			corrID = c.ItemID
		}
		h.processN(t, 1)

		items, _ = h.store.Items("m1")
		for _, it := range items {
			switch it.ItemID {
			case prev.ItemID:
				if it.Reinforcements != 0 || it.Strength != 0.1 {
					t.Fatalf("original was reinforced: %+v", it)
				}
			case corrID:
				if it.Reinforcements != 1 || it.Strength < 0.599 || it.Strength > 0.601 { // scored 0.5 + one step
					t.Fatalf("correction not reinforced: %+v", it)
				}
			}
		}
	})

	t.Run("items added mid-call wait for the next call", func(t *testing.T) {
		h := newHarness(t, Options{})
		h.store.AddChunks("e1", dialogue("m1", 1, 3, 0, 2))
		h.engine.onInfer = func() {
			if _, err := h.store.Add(context.Background(), "m1", AddRequest{Text: "ADDED-MID-CALL", Agent: "agent"}); err != nil {
				t.Error(err)
			}
		}
		h.processN(t, 1)
		h.engine.mu.Lock()
		first := h.engine.users[0]
		h.engine.mu.Unlock()
		if strings.Contains(first, "ADDED-MID-CALL") {
			t.Fatal("an item added mid-call reached the running call's prompt")
		}
		items, _ := h.store.Items("m1")
		itemByText(t, items, "ADDED-MID-CALL")
	})

	t.Run("chunk forgotten mid-call loses its thought", func(t *testing.T) {
		h := newHarness(t, Options{})
		h.store.AddChunks("e1", dialogue("m1", 1, 2, 1))
		items, _ := h.store.Items("m1")
		chunk := itemByText(t, items, "m1 turn 1")
		h.engine.onInfer = func() { _ = h.store.Forget("m1", chunk.ItemID) }
		h.processN(t, 1)
		if st := h.status(t, "m1"); st.Counts.Thoughts != 0 || st.Counts.Items != 1 {
			t.Fatalf("thought of a forgotten chunk was kept: %+v", st.Counts)
		}
	})
}

func TestModelReinforcesOnlyItemsInItsSnapshot(t *testing.T) {
	h := newHarness(t, Options{})
	h.store.AddChunks("e1", dialogue("m1", 1, 3, 1))
	h.engine.used = "c1, x2, x9, r1" // x9 and r1 were not in the prompt
	h.processN(t, 1)

	items, _ := h.store.Items("m1")
	chunk := itemByText(t, items, "m1 turn 1")
	next := itemByText(t, items, "m1 turn 2")
	prev := itemByText(t, items, "m1 turn 0")
	if chunk.Reinforcements != 1 || next.Reinforcements != 1 || prev.Reinforcements != 0 {
		t.Fatalf("reinforcements: chunk %d, next %d, prev %d", chunk.Reinforcements, next.Reinforcements, prev.Reinforcements)
	}
	if chunk.Strength < 0.89 || chunk.Strength > 0.91 {
		t.Fatalf("chunk strength %v, want 0.9", chunk.Strength)
	}
}

// Long-term memories reach working memory only through the harness: it fetches them from Node 1
// and adds them with their node_id. Such an item is offered to the model like any other item of
// the memory, and a reinforced one reinforces its Node 1 node on commit instead of being written
// again.
func TestHarnessAddedLongTermItemReinforcesItsNode(t *testing.T) {
	h := newHarness(t, Options{RelatedLimit: 5})
	h.store.AddChunks("e1", dialogue("m1", 1, 5, 4))
	lt, err := h.store.Add(context.Background(), "m1", AddRequest{Text: "Backups run nightly at 02:00", Agent: "cortex", NodeID: "n-17"})
	if err != nil {
		t.Fatal(err)
	}
	if lt.Origin != OriginLongTerm || lt.NodeID != "n-17" || lt.Source != "harness" || lt.Speaker != "cortex" {
		t.Fatalf("long-term item: %+v", lt)
	}
	again, _ := h.store.Add(context.Background(), "m1", AddRequest{Text: "same node", Agent: "cortex", NodeID: "n-17"})
	if again.ItemID != lt.ItemID {
		t.Fatal("adding the same Node 1 node twice made a second item")
	}

	h.engine.used = "c1, r1"
	h.processN(t, 1)
	h.engine.mu.Lock()
	prompt := h.engine.users[0]
	h.engine.mu.Unlock()
	if !strings.Contains(prompt, "### RELATED ITEMS IN THIS MEMORY") || !strings.Contains(prompt, "[r1] (cortex) Backups run nightly at 02:00") {
		t.Fatalf("long-term item not offered as related:\n%s", prompt)
	}

	if _, err := h.store.Commit(context.Background(), "m1", "explicit"); err != nil {
		t.Fatal(err)
	}
	b := h.committer.only(t)
	for _, it := range b.Items {
		if it.Text == lt.Text {
			t.Fatal("a Node 1 node was written again")
		}
	}
	if len(b.Reinforcements) != 1 || b.Reinforcements[0].NodeID != "n-17" || b.Reinforcements[0].Reinforcements != 1 {
		t.Fatalf("reinforcements: %+v", b.Reinforcements)
	}
	thought := b.Items[len(b.Items)-1]
	found := false
	for _, l := range thought.Links {
		if l.Rel == RelUses && l.Node == "n-17" {
			found = true
		}
	}
	if !found {
		t.Fatalf("thought does not link the Node 1 node: %+v", thought.Links)
	}
}

func TestRelatedItemsStayInTheirMemoryAndSkipThoughts(t *testing.T) {
	h := newHarness(t, Options{RelatedLimit: 5})
	h.store.AddChunks("e1", dialogue("a", 1, 6, 0, 5))
	h.store.AddChunks("e1", dialogue("b", 100, 3))
	if _, err := h.store.Add(context.Background(), "b", AddRequest{Text: "task of a turn leaked", Agent: "x"}); err != nil {
		t.Fatal(err)
	}
	h.processN(t, 1) // a turn 0: its thought mentions "a turn 0"
	job, err := h.store.NextJob(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if job.Chunk.Text != "a turn 5" {
		t.Fatalf("unexpected job %q", job.Chunk.Text)
	}
	for _, r := range job.Related {
		if r.MemoryID != "a" || r.Origin == OriginThought {
			t.Fatalf("related item from another memory or a thought: %+v", r)
		}
		if r.ItemID == job.Chunk.ItemID || r.Text == "a turn 4" {
			t.Fatalf("the chunk or its context was offered as related: %+v", r)
		}
	}
	if len(job.Related) == 0 {
		t.Fatal("no related items offered")
	}
}

func TestRecallSearchesTheTempDB(t *testing.T) {
	h := newHarness(t, Options{})
	h.store.AddChunks("e1", dialogue("a", 1, 3))
	h.store.AddChunks("e1", dialogue("b", 10, 3))
	if _, err := h.store.Add(context.Background(), "b", AddRequest{Text: "the lantern backup failed", Agent: "x"}); err != nil {
		t.Fatal(err)
	}

	hits := h.store.Recall(RecallQuery{Query: "lantern backup"})
	if len(hits) != 1 || hits[0].MemoryID != "b" || hits[0].Score != 2 {
		t.Fatalf("recall across the temp DB: %+v", hits)
	}
	if hits := h.store.Recall(RecallQuery{Query: "lantern backup", MemoryID: "a"}); len(hits) != 0 {
		t.Fatalf("memory filter ignored: %+v", hits)
	}
	if hits := h.store.Recall(RecallQuery{Query: "turn", Session: "s-a", Limit: 2}); len(hits) != 2 || hits[0].MemoryID != "a" {
		t.Fatalf("session filter or limit ignored: %+v", hits)
	}
	if _, err := h.store.Commit(context.Background(), "b", "explicit"); err != nil {
		t.Fatal(err)
	}
	if hits := h.store.Recall(RecallQuery{Query: "lantern backup"}); len(hits) != 0 {
		t.Fatalf("committed memory still recalled: %+v", hits)
	}
}

func TestHarnessAddIsLabelledAndScored(t *testing.T) {
	h := newHarness(t, Options{})
	h.store.scorer = fakeScorer{scores: map[string]float64{"the backup failed at 02:10": 0.66}}
	h.store.AddChunks("e1", dialogue("m1", 1, 1))

	it, err := h.store.Add(context.Background(), "m1", AddRequest{Text: "the backup failed at 02:10", Agent: "claude-agent"})
	if err != nil {
		t.Fatal(err)
	}
	if it.Source != "harness" || it.Speaker != "claude-agent" || it.Strength != 0.66 || it.Session != "s-m1" || it.Strong {
		t.Fatalf("harness item: %+v", it)
	}
	if _, err := h.store.Add(context.Background(), "m1", AddRequest{Text: "x", Agent: ""}); !errors.Is(err, ErrMissingAgent) {
		t.Fatalf("add without agent: %v", err)
	}
	if _, err := h.store.Add(context.Background(), "nope", AddRequest{Text: "x", Agent: "a"}); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatalf("add to unknown memory: %v", err)
	}
}

func TestCorrectingQueuedChunkQueuesTheCorrection(t *testing.T) {
	h := newHarness(t, Options{})
	h.store.AddChunks("e1", dialogue("m1", 1, 3, 1))
	items, _ := h.store.Items("m1")
	orig := itemByText(t, items, "m1 turn 1")
	if _, err := h.store.Correct(context.Background(), "m1", orig.ItemID, "turn 1 corrected", "agent"); err != nil {
		t.Fatal(err)
	}
	h.processN(t, 1)

	h.engine.mu.Lock()
	prompt := h.engine.users[0]
	h.engine.mu.Unlock()
	if !strings.Contains(prompt, "[c1] (agent) turn 1 corrected") || !strings.Contains(prompt, "m1 turn 0") {
		t.Fatalf("the correction was not processed in the original's place:\n%s", prompt)
	}
	st := h.status(t, "m1")
	if st.Counts.Superseded != 1 || st.Counts.Done != 1 {
		t.Fatalf("counts: %+v", st.Counts)
	}
	if _, err := h.store.Commit(context.Background(), "m1", "explicit"); err != nil {
		t.Fatal(err)
	}
	seqs := map[int64]int{}
	for _, it := range h.committer.only(t).Items {
		if it.Seq != 0 {
			seqs[it.Seq]++
		}
	}
	for seq, n := range seqs {
		if n > 1 {
			t.Fatalf("seq %d committed %d times", seq, n)
		}
	}
}

func TestMemoriesAreIsolated(t *testing.T) {
	h := newHarness(t, Options{})
	h.store.AddChunks("e1", dialogue("a", 1, 2))
	h.store.AddChunks("e1", dialogue("b", 10, 2))
	itemsA, _ := h.store.Items("a")
	if err := h.store.Forget("b", itemsA[0].ItemID); err != nil {
		t.Fatal(err) // same item ID exists in b: forgetting there must leave a alone
	}
	if again, _ := h.store.Items("a"); len(again) != 2 {
		t.Fatalf("an edit to b changed a: %d items", len(again))
	}
}

func TestEndSessionCommitsItsMemories(t *testing.T) {
	h := newHarness(t, Options{})
	a := dialogue("a", 1, 2)
	b := dialogue("b", 10, 2)
	c := dialogue("c", 20, 2)
	for i := range a {
		a[i].Session, b[i].Session, c[i].Session = "conv-1", "conv-1", "conv-2"
	}
	h.store.AddChunks("e1", append(append(a, b...), c...))

	sums, err := h.store.EndSession(context.Background(), "conv-1")
	if err != nil || len(sums) != 2 {
		t.Fatalf("end session: %+v, %v", sums, err)
	}
	if st := h.store.Stats(); st.Memories != 1 {
		t.Fatalf("expected only conv-2 left, got %+v", st)
	}
}

func TestDuplicateDeliveryIsIgnored(t *testing.T) {
	h := newHarness(t, Options{})
	chunks := dialogue("m1", 1, 3, 1)
	if n := h.store.AddChunks("e1", chunks); n != 3 {
		t.Fatalf("stored %d", n)
	}
	if n := h.store.AddChunks("e1", chunks); n != 0 {
		t.Fatalf("redelivery stored %d again", n)
	}
	if st := h.status(t, "m1"); st.Counts.Items != 3 || st.Counts.Queued != 1 {
		t.Fatalf("counts after redelivery: %+v", st.Counts)
	}
}

func TestReceiveIsAllOrNothingAndDedupesByHighWater(t *testing.T) {
	h := newHarness(t, Options{MaxItems: 5})
	res, err := h.store.Receive("e1", dialogue("a", 1, 3))
	if err != nil || res.Accepted != 3 || res.AcceptedUpToSeq != 3 {
		t.Fatalf("first push: %+v %v", res, err)
	}
	// A retry of the same batch, plus two new chunks: only the new ones are stored.
	res, err = h.store.Receive("e1", dialogue("a", 1, 5))
	if err != nil || res.Accepted != 2 || res.Duplicates != 3 || res.AcceptedUpToSeq != 5 {
		t.Fatalf("retry push: %+v %v", res, err)
	}
	// Full: nothing of the batch is stored, and the high-water mark does not move.
	if _, err := h.store.Receive("e1", dialogue("b", 6, 2)); !errors.Is(err, ErrFull) {
		t.Fatalf("push beyond MaxItems: %v", err)
	}
	if _, err := h.store.Status("b"); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatal("a refused batch left a memory behind")
	}
	// Out of order and missing fields are refused.
	bad := dialogue("c", 20, 2)
	bad[0].Seq, bad[1].Seq = 21, 20
	if _, err := h.store.Receive("e1", bad); !errors.Is(err, ErrInvalidBatch) {
		t.Fatalf("out-of-order batch: %v", err)
	}
	if _, err := h.store.Receive("", dialogue("c", 20, 1)); !errors.Is(err, ErrInvalidBatch) {
		t.Fatalf("batch without epoch: %v", err)
	}
	// After a commit frees space, a retry that arrives late is still recognised as a duplicate.
	if _, err := h.store.Commit(context.Background(), "a", "explicit"); err != nil {
		t.Fatal(err)
	}
	if res, _ := h.store.Receive("e1", dialogue("a", 1, 5)); res.Accepted != 0 || res.Duplicates != 5 {
		t.Fatalf("late retry after commit recreated the memory: %+v", res)
	}
	// A new epoch (Node 3 restarted) starts its seqs again.
	if res, err := h.store.Receive("e2", dialogue("d", 1, 2)); err != nil || res.Accepted != 2 {
		t.Fatalf("new epoch: %+v %v", res, err)
	}
}

func TestReceiveWithoutCommitTargetIsRefused(t *testing.T) {
	s := NewStore(Options{IdleTimeout: time.Minute}, nil, nil)
	if _, err := s.Receive("e1", dialogue("a", 1, 1)); !errors.Is(err, ErrNoCommitTarget) {
		t.Fatalf("got %v", err)
	}
}

func TestCommitDeletesEverythingFromTheTempDB(t *testing.T) {
	h := newHarness(t, Options{})
	h.store.AddChunks("e1", dialogue("a", 1, 4, 1))
	h.store.AddChunks("e1", dialogue("b", 10, 2))
	h.processN(t, 1)
	if _, err := h.store.Commit(context.Background(), "a", "explicit"); err != nil {
		t.Fatal(err)
	}

	txn := h.store.db.Txn(false)
	iter, err := txn.Get(tableItems, "memory_id", "a")
	if err != nil {
		t.Fatal(err)
	}
	if raw := iter.Next(); raw != nil {
		t.Fatalf("item of a committed memory is still in the temp DB: %+v", raw)
	}
	if raw, _ := txn.First(tableMemories, "id", "a"); raw != nil {
		t.Fatal("committed memory row is still in the temp DB")
	}
	all, _ := txn.Get(tableItems, "id")
	n := 0
	for raw := all.Next(); raw != nil; raw = all.Next() {
		n++
	}
	if n != 2 || h.store.Stats().Items != 2 {
		t.Fatalf("expected only b's 2 items left, DB has %d, stats %d", n, h.store.Stats().Items)
	}
}

func TestFailedModelCallIsCommittedUnprocessed(t *testing.T) {
	h := newHarness(t, Options{})
	h.engine.err = errBoom
	h.store.AddChunks("e1", dialogue("m1", 1, 1, 0))
	if err := h.proc.ProcessNext(context.Background()); !errors.Is(err, errBoom) {
		t.Fatalf("expected the engine error, got %v", err)
	}
	if st := h.status(t, "m1"); st.Counts.Failed != 1 || st.Counts.Thoughts != 0 {
		t.Fatalf("counts: %+v", st.Counts)
	}
	if _, err := h.store.Commit(context.Background(), "m1", "explicit"); err != nil {
		t.Fatal(err)
	}
	if it := h.committer.only(t).Items[0]; it.Processing != StatusFailed || it.Strength != 0.8 {
		t.Fatalf("failed chunk committed as %+v", it)
	}
}

func TestNextJobReturnsOnCancel(t *testing.T) {
	h := newHarness(t, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := h.store.NextJob(ctx)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("NextJob did not return after cancel")
	}
}
