// Package workingmemory is Node 2's working memory: a temp store that holds each memory's items
// between Node 3 and Node 1, a queue that feeds strong chunks to the model one at a time, and the
// rules that commit a memory to long-term memory and then delete it.
package workingmemory

import (
	"context"
	"time"
)

// Origin says where an item came from.
type Origin string

// Item origins.
const (
	OriginSensory  Origin = "node3"          // a chunk drained from Node 3
	OriginHarness  Origin = "harness"        // added or corrected by the harness
	OriginThought  Origin = "working-memory" // a thought produced by Node 2's model
	OriginLongTerm Origin = "node1"          // a Node 1 node the harness fetched and added (has NodeID)
)

// SpeakerWorkingMemory is the speaker label on thoughts produced by Node 2's model.
const SpeakerWorkingMemory = "working-memory"

// ChunkStatus is the processing state of a strong chunk.
type ChunkStatus string

// Chunk statuses. Weak chunks, harness items, thoughts and recalled items have no status.
const (
	StatusQueued      ChunkStatus = "queued"
	StatusProcessing  ChunkStatus = "processing"
	StatusDone        ChunkStatus = "done"
	StatusTimedOut    ChunkStatus = "timed_out"   // waited longer than the wait limit; committed unprocessed
	StatusFailed      ChunkStatus = "failed"      // the model call failed; committed unprocessed
	StatusUnprocessed ChunkStatus = "unprocessed" // still queued when the memory was committed
)

// Chunk is one record Node 3 pushes to Node 2 (the Task 29 chunk record).
type Chunk struct {
	ID          string    `json:"id"`
	MemoryID    string    `json:"memory_id"`
	Text        string    `json:"text"`
	Type        string    `json:"type"`
	Source      string    `json:"source"`
	Session     string    `json:"session"`
	Speaker     string    `json:"speaker,omitempty"`
	TurnID      string    `json:"turn_id,omitempty"`
	ParentID    string    `json:"parent_id,omitempty"`
	Part        int       `json:"part,omitempty"`
	Parts       int       `json:"parts,omitempty"`
	Heading     string    `json:"heading,omitempty"`
	Format      string    `json:"format,omitempty"`
	Seq         int64     `json:"seq"`
	TS          time.Time `json:"ts"`
	Task        string    `json:"task,omitempty"`
	TaskScore   *float64  `json:"task_score"`
	Strong      bool      `json:"strong"`
	ScoreStatus string    `json:"score_status"`
}

// Item is one entry of a memory in the temp store.
type Item struct {
	ItemID   string `json:"item_id"`
	MemoryID string `json:"memory_id"`
	Origin   Origin `json:"origin"`
	Text     string `json:"text"`

	// Provenance. Node 3 chunks carry Node 3's labels; harness items carry source "harness" and the
	// calling agent as speaker; thoughts carry speaker "working-memory".
	ChunkID     string    `json:"chunk_id,omitempty"`
	Type        string    `json:"type,omitempty"`
	Source      string    `json:"source,omitempty"`
	Session     string    `json:"session,omitempty"`
	Speaker     string    `json:"speaker,omitempty"`
	TurnID      string    `json:"turn_id,omitempty"`
	ParentID    string    `json:"parent_id,omitempty"`
	Part        int       `json:"part,omitempty"`
	Parts       int       `json:"parts,omitempty"`
	Heading     string    `json:"heading,omitempty"`
	Format      string    `json:"format,omitempty"`
	Epoch       string    `json:"epoch,omitempty"`
	Seq         int64     `json:"seq,omitempty"`
	TS          time.Time `json:"ts"`
	TaskScore   *float64  `json:"task_score"`
	ScoreStatus string    `json:"score_status,omitempty"`

	// NodeID is the Node 1 node a long-term item was fetched from by the harness. Such an item is
	// never written to Node 1 again; if reinforced, its node is reinforced instead.
	NodeID string `json:"node_id,omitempty"`

	// Strength starts at the task score and grows with each reinforcement.
	Strength       float64 `json:"strength"`
	Reinforcements int     `json:"reinforcements"`

	// Strong chunks only.
	Strong bool        `json:"strong"`
	Status ChunkStatus `json:"status,omitempty"`

	// Links.
	Supersedes   string   `json:"supersedes,omitempty"`    // item this one corrects
	SupersededBy string   `json:"superseded_by,omitempty"` // item that corrects this one
	DerivedFrom  string   `json:"derived_from,omitempty"`  // thoughts: the chunk the thought is about
	Uses         []string `json:"uses,omitempty"`          // thoughts: the items the thought relies on

	// Key is the item's primary key in the temp DB: memory_id + "/" + item_id.
	Key string `json:"-"`

	n          int       // insertion order within the memory
	start      float64   // starting strength
	enqueuedAt time.Time // strong chunks: when queued
}

// Scorer scores text against a task the same way Node 3 does. It returns a nil score and a status
// other than "scored" when it cannot score.
type Scorer interface {
	Score(ctx context.Context, task, text string) (score *float64, status string)
}

// Committer writes a memory to long-term memory (Node 1).
type Committer interface {
	Commit(ctx context.Context, batch CommitBatch) error
}

// Link relations used in a commit batch.
const (
	RelSupersedes   = "supersedes"
	RelSupersededBy = "superseded_by"
	RelDerivedFrom  = "derived_from"
	RelUses         = "uses"
)

// Link points from a committed item to another item in the same batch (Item) or to an existing
// Node 1 node (Node).
type Link struct {
	Rel  string `json:"rel"`
	Item string `json:"item,omitempty"`
	Node string `json:"node,omitempty"`
}

// CommitItem is one verbatim observation, correction or thought sent to Node 1.
type CommitItem struct {
	ItemID      string    `json:"item_id"`
	Origin      Origin    `json:"origin"`
	Text        string    `json:"text"`
	ChunkID     string    `json:"chunk_id,omitempty"`
	Type        string    `json:"type,omitempty"`
	Source      string    `json:"source,omitempty"`
	Session     string    `json:"session,omitempty"`
	Speaker     string    `json:"speaker,omitempty"`
	TurnID      string    `json:"turn_id,omitempty"`
	ParentID    string    `json:"parent_id,omitempty"`
	Part        int       `json:"part,omitempty"`
	Parts       int       `json:"parts,omitempty"`
	Heading     string    `json:"heading,omitempty"`
	Format      string    `json:"format,omitempty"`
	Seq         int64     `json:"seq,omitempty"`
	TS          time.Time `json:"ts"`
	ScoreStatus string    `json:"score_status,omitempty"`
	Strength    float64   `json:"strength"`
	// Processing is the chunk status for strong chunks ("done", "timed_out", ...); empty otherwise.
	Processing ChunkStatus `json:"processing,omitempty"`
	Links      []Link      `json:"links,omitempty"`
}

// Reinforcement strengthens an existing Node 1 node instead of writing a duplicate.
type Reinforcement struct {
	NodeID         string  `json:"node_id"`
	Reinforcements int     `json:"reinforcements"`
	Delta          float64 `json:"delta"`
}

// CommitBatch is everything one memory sends to Node 1. It carries no templates, goals or
// generated summaries: only the items and their provenance, strength and links.
type CommitBatch struct {
	MemoryID       string          `json:"memory_id"`
	Reason         string          `json:"reason"`
	Items          []CommitItem    `json:"items"`
	Reinforcements []Reinforcement `json:"reinforcements,omitempty"`
	CommittedAt    time.Time       `json:"committed_at"`
}
