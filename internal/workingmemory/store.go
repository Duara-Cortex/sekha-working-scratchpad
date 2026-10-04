package workingmemory

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/hashicorp/go-memdb"
)

var (
	// ErrMemoryNotFound means no memory with that memory_id is held: it is unknown, or Node 3 has
	// not pushed it yet.
	ErrMemoryNotFound = errors.New("memory not found (unknown, or not yet received from Node 3)")
	// ErrItemNotFound means the memory holds no item with that item_id.
	ErrItemNotFound = errors.New("item not found")
	// ErrCommitting means the memory is being committed and no longer accepts edits.
	ErrCommitting = errors.New("memory is being committed")
	// ErrSuperseded means the item was already corrected; correct the newer version instead.
	ErrSuperseded = errors.New("item already superseded")
	// ErrEmptyText means an add or correct carried no text.
	ErrEmptyText = errors.New("text is required")
	// ErrMissingAgent means an add or correct did not name the calling agent.
	ErrMissingAgent = errors.New("agent is required")
	// ErrFull means the temp DB is at its item limit; the sender should retry later.
	ErrFull = errors.New("working memory is full")
	// ErrNoCommitTarget means no commit target is configured, so pushed chunks could never leave;
	// they are refused and stay on Node 3.
	ErrNoCommitTarget = errors.New("no commit target configured")
	// ErrInvalidBatch means a pushed batch broke the push contract.
	ErrInvalidBatch = errors.New("invalid chunk batch")
	// ErrClosed means the store was closed.
	ErrClosed = errors.New("working memory store closed")
)

// StatusSuperseded marks a queued chunk whose queue slot passed to its correction.
const StatusSuperseded ChunkStatus = "superseded"

// maxCommitted is how many committed-memory summaries are kept for status polling.
const maxCommitted = 1024

const (
	tableMemories = "memories"
	tableItems    = "items"
)

// schema is the temp DB: one row per memory and one per item. Nothing else lives there.
var schema = &memdb.DBSchema{
	Tables: map[string]*memdb.TableSchema{
		tableMemories: {
			Name: tableMemories,
			Indexes: map[string]*memdb.IndexSchema{
				"id":      {Name: "id", Unique: true, Indexer: &memdb.StringFieldIndex{Field: "ID"}},
				"session": {Name: "session", AllowMissing: true, Indexer: &memdb.StringFieldIndex{Field: "Session"}},
			},
		},
		tableItems: {
			Name: tableItems,
			Indexes: map[string]*memdb.IndexSchema{
				"id":        {Name: "id", Unique: true, Indexer: &memdb.StringFieldIndex{Field: "Key"}},
				"memory_id": {Name: "memory_id", AllowMissing: true, Indexer: &memdb.StringFieldIndex{Field: "MemoryID"}},
			},
		},
	},
}

// memoryRecord is a memory's row in the temp DB. Rows are never modified in place: every change
// is a copy written back in a transaction.
type memoryRecord struct {
	ID                string
	Task              string
	Session           string
	NextItem          int
	CreatedAt         time.Time
	LastActivity      time.Time
	LastCommitError   string
	NextCommitAttempt time.Time
}

// memState is a memory's coordination state; it is not data and stays out of the temp DB.
type memState struct {
	queue        []string // queued strong chunk item IDs, oldest first
	inFlight     int
	committing   bool
	commitDone   chan struct{}
	commitResult CommitSummary
	commitErr    error
	late         []lateChunk // chunks that arrived while the memory was committing
}

type lateChunk struct {
	epoch string
	chunk Chunk
}

// Options configures the store.
type Options struct {
	// WaitLimit is how long a strong chunk may wait in the queue; <= 0 means no limit.
	WaitLimit time.Duration
	// IdleTimeout is how long a memory with no pending work waits for another call before it is
	// committed.
	IdleTimeout time.Duration
	// ReinforceStep is the strength added by one reinforcement.
	ReinforceStep float64
	// MaxItems caps the items held across all memories; <= 0 means no cap.
	MaxItems int
	// RelatedLimit caps the related items from the same memory offered to one model call.
	RelatedLimit int
	// Now returns the current time; tests replace it.
	Now func() time.Time
}

// CommitSummary describes a finished commit.
type CommitSummary struct {
	MemoryID       string    `json:"memory_id"`
	Reason         string    `json:"reason"`
	Items          int       `json:"items"`
	Reinforcements int       `json:"reinforcements"`
	CommittedAt    time.Time `json:"committed_at"`
}

// Store is Node 2's working memory. Memories and items live in an in-memory NoSQL DB (go-memdb)
// until they are committed to Node 1, and are then deleted from it. Nothing survives a restart.
type Store struct {
	mu        sync.Mutex // serialises writes and guards the coordination state below
	cond      *sync.Cond
	db        *memdb.MemDB
	opts      Options
	committer Committer
	scorer    Scorer

	state     map[string]*memState
	ring      []string // memory IDs in arrival order, for queue rotation
	cursor    int      // ring index after the last memory served; taken modulo len(ring) when read
	items     int      // items held (including late chunks), for MaxItems
	highWater map[string]int64

	committed      map[string]CommitSummary
	committedOrder []string
	closed         bool
}

// NewStore creates an empty working memory. scorer may be nil; with a nil committer the store
// refuses pushed chunks.
func NewStore(opts Options, committer Committer, scorer Scorer) *Store {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	db, err := memdb.NewMemDB(schema)
	if err != nil {
		panic(fmt.Sprintf("working memory schema: %v", err)) // the schema is static
	}
	s := &Store{
		db:        db,
		opts:      opts,
		committer: committer,
		scorer:    scorer,
		state:     make(map[string]*memState),
		highWater: make(map[string]int64),
		committed: make(map[string]CommitSummary),
	}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// Close wakes every waiting worker; NextJob then returns ErrClosed.
func (s *Store) Close() {
	s.mu.Lock()
	s.closed = true
	s.cond.Broadcast()
	s.mu.Unlock()
}

// --- temp DB access. Everything read from the DB is copied before it is changed. ---

func itemKey(memoryID, itemID string) string { return memoryID + "/" + itemID }

func getMemory(txn *memdb.Txn, id string) *memoryRecord {
	raw, err := txn.First(tableMemories, "id", id)
	if err != nil || raw == nil {
		return nil
	}
	m := *raw.(*memoryRecord)
	return &m
}

func putMemory(txn *memdb.Txn, m *memoryRecord) {
	if err := txn.Insert(tableMemories, m); err != nil {
		panic(fmt.Sprintf("working memory: insert memory: %v", err))
	}
}

func getItem(txn *memdb.Txn, memoryID, itemID string) *Item {
	raw, err := txn.First(tableItems, "id", itemKey(memoryID, itemID))
	if err != nil || raw == nil {
		return nil
	}
	it := cloneItem(raw.(*Item))
	return &it
}

func putItem(txn *memdb.Txn, it *Item) {
	it.Key = itemKey(it.MemoryID, it.ItemID)
	if err := txn.Insert(tableItems, it); err != nil {
		panic(fmt.Sprintf("working memory: insert item: %v", err))
	}
}

// memoryItems returns copies of a memory's items in the order they were added.
func memoryItems(txn *memdb.Txn, memoryID string) []*Item {
	iter, err := txn.Get(tableItems, "memory_id", memoryID)
	if err != nil {
		return nil
	}
	var out []*Item
	for raw := iter.Next(); raw != nil; raw = iter.Next() {
		it := cloneItem(raw.(*Item))
		out = append(out, &it)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].n < out[b].n })
	return out
}

func cloneItem(it *Item) Item {
	c := *it
	c.Uses = slices.Clone(it.Uses)
	if it.TaskScore != nil {
		v := *it.TaskScore
		c.TaskScore = &v
	}
	return c
}

// addItem gives a new item its ID within the memory and writes it.
func addItem(txn *memdb.Txn, m *memoryRecord, it *Item) {
	m.NextItem++
	it.ItemID = fmt.Sprintf("i%d", m.NextItem)
	it.MemoryID = m.ID
	it.n = m.NextItem
	it.start = it.Strength
	putItem(txn, it)
}

// resolve follows superseded_by links to the newest version of an item.
func resolve(txn *memdb.Txn, memoryID, itemID string) *Item {
	it := getItem(txn, memoryID, itemID)
	for hops := 0; it != nil && it.SupersededBy != "" && hops < 1024; hops++ {
		next := getItem(txn, memoryID, it.SupersededBy)
		if next == nil {
			break
		}
		it = next
	}
	return it
}

func startStrength(score *float64, fallback float64) float64 {
	if score != nil {
		return *score
	}
	return fallback
}

// --- receiving chunks from Node 3 ---

// ReceiveResult tells Node 3 what it may evict.
type ReceiveResult struct {
	Epoch      string `json:"epoch"`
	Accepted   int    `json:"accepted"`
	Duplicates int    `json:"duplicates"`
	// AcceptedUpToSeq is the highest seq of the epoch Node 2 holds or has committed; Node 3 may
	// evict every chunk up to it.
	AcceptedUpToSeq int64 `json:"accepted_up_to_seq"`
}

// Receive stores a batch pushed by Node 3. The batch is all-or-nothing: either every new chunk is
// in the temp DB when Receive returns, or none is. Chunks must arrive in seq order; any seq at or
// below the epoch's high-water mark is a redelivery and is skipped. Strong chunks are queued for
// the model; weak chunks are only stored.
func (s *Store) Receive(epoch string, chunks []Chunk) (ReceiveResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	res := ReceiveResult{Epoch: epoch, AcceptedUpToSeq: s.highWater[epoch]}
	if s.committer == nil {
		return res, ErrNoCommitTarget
	}
	if epoch == "" {
		return res, fmt.Errorf("%w: epoch is required", ErrInvalidBatch)
	}
	var fresh []Chunk
	last := int64(-1)
	for _, c := range chunks {
		switch {
		case c.MemoryID == "":
			return res, fmt.Errorf("%w: chunk seq %d has no memory_id", ErrInvalidBatch, c.Seq)
		case c.Seq <= 0:
			return res, fmt.Errorf("%w: chunk has no seq", ErrInvalidBatch)
		case c.Seq <= last:
			return res, fmt.Errorf("%w: seq %d is not after %d", ErrInvalidBatch, c.Seq, last)
		}
		last = c.Seq
		if c.Seq <= s.highWater[epoch] {
			res.Duplicates++
			continue
		}
		fresh = append(fresh, c)
	}
	if len(fresh) == 0 {
		return res, nil
	}
	if s.opts.MaxItems > 0 && s.items+len(fresh) > s.opts.MaxItems {
		return res, ErrFull
	}

	txn := s.db.Txn(true)
	defer txn.Abort()
	now := s.opts.Now()
	for _, c := range fresh {
		st := s.state[c.MemoryID]
		if st != nil && st.committing {
			st.late = append(st.late, lateChunk{epoch: epoch, chunk: c})
			continue
		}
		m := getMemory(txn, c.MemoryID)
		if m == nil {
			m = s.newMemoryLocked(c.MemoryID, c.Task, c.Session, now)
		}
		s.addChunkLocked(txn, m, epoch, c, now)
		putMemory(txn, m)
	}
	txn.Commit()

	s.items += len(fresh)
	s.highWater[epoch] = last
	res.Accepted = len(fresh)
	res.AcceptedUpToSeq = last
	s.cond.Broadcast()
	return res, nil
}

// AddChunks stores chunks under the given epoch and returns how many were new.
func (s *Store) AddChunks(epoch string, chunks []Chunk) int {
	res, _ := s.Receive(epoch, chunks)
	return res.Accepted
}

func (s *Store) newMemoryLocked(id, task, session string, now time.Time) *memoryRecord {
	s.state[id] = &memState{}
	s.ring = append(s.ring, id)
	return &memoryRecord{ID: id, Task: task, Session: session, CreatedAt: now, LastActivity: now}
}

func (s *Store) addChunkLocked(txn *memdb.Txn, m *memoryRecord, epoch string, c Chunk, now time.Time) {
	if m.Task == "" {
		m.Task = c.Task
	}
	if m.Session == "" {
		m.Session = c.Session
	}
	it := &Item{
		Origin:      OriginSensory,
		Text:        c.Text,
		ChunkID:     c.ID,
		Type:        c.Type,
		Source:      c.Source,
		Session:     c.Session,
		Speaker:     c.Speaker,
		TurnID:      c.TurnID,
		ParentID:    c.ParentID,
		Part:        c.Part,
		Parts:       c.Parts,
		Heading:     c.Heading,
		Format:      c.Format,
		Epoch:       epoch,
		Seq:         c.Seq,
		TS:          c.TS,
		TaskScore:   c.TaskScore,
		ScoreStatus: c.ScoreStatus,
		Strength:    startStrength(c.TaskScore, 0),
		Strong:      c.Strong,
	}
	if it.Strong {
		it.Status = StatusQueued
		it.enqueuedAt = now
	}
	addItem(txn, m, it)
	if it.Strong {
		st := s.state[m.ID]
		st.queue = append(st.queue, it.ItemID)
	}
	m.LastActivity = now
}

// touch records activity on a memory (resets its idle timer).
func touch(txn *memdb.Txn, memoryID string, now time.Time) {
	if m := getMemory(txn, memoryID); m != nil {
		m.LastActivity = now
		putMemory(txn, m)
	}
}

// --- the model queue ---

// Job is one model call: a snapshot of the strong chunk being processed, read-only context from its
// links, and related items from the same memory. Changes after the snapshot wait for the next call.
type Job struct {
	MemoryID string
	Task     string
	Chunk    Item
	Context  []Item
	Related  []Item
}

// NextJob blocks until a strong chunk is queued, then takes it off the queue and returns a snapshot
// of it. Memories are served in rotation, one chunk from each in turn.
func (s *Store) NextJob(ctx context.Context) (*Job, error) {
	stop := context.AfterFunc(ctx, func() {
		s.mu.Lock()
		s.cond.Broadcast()
		s.mu.Unlock()
	})
	defer stop()

	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if s.closed {
			return nil, ErrClosed
		}
		s.expireLocked(s.opts.Now())
		if job := s.popLocked(); job != nil {
			return job, nil
		}
		s.cond.Wait()
	}
}

// popLocked takes the oldest queued chunk of the next memory in rotation.
func (s *Store) popLocked() *Job {
	n := len(s.ring)
	for i := 0; i < n; i++ {
		idx := (s.cursor + i) % n
		id := s.ring[idx]
		st := s.state[id]
		if st.committing || len(st.queue) == 0 {
			continue
		}
		itemID := st.queue[0]
		st.queue = st.queue[1:]
		// Not wrapped here: a memory appended before the next pop sits at idx+1 and is served next.
		s.cursor = idx + 1
		st.inFlight++

		txn := s.db.Txn(true)
		now := s.opts.Now()
		it := getItem(txn, id, itemID)
		it.Status = StatusProcessing
		putItem(txn, it)
		m := getMemory(txn, id)
		m.LastActivity = now
		putMemory(txn, m)
		items := memoryItems(txn, id)
		txn.Commit()

		job := &Job{MemoryID: id, Task: m.Task, Chunk: cloneItem(it)}
		job.Context = contextFor(items, it)
		job.Related = relatedFor(items, it, job.Context, m.Task, s.opts.RelatedLimit)
		return job
	}
	return nil
}

// contextFor returns the chunks next to it in its memory: the one before it (the turn it answers,
// or the previous part of a split turn) and the one after it.
func contextFor(items []*Item, it *Item) []Item {
	var prev, next *Item
	for _, o := range items {
		if o.Origin != OriginSensory || o.Epoch != it.Epoch || o.ItemID == it.ItemID {
			continue
		}
		if o.Seq < it.Seq && (prev == nil || o.Seq > prev.Seq) {
			prev = o
		}
		if o.Seq > it.Seq && (next == nil || o.Seq < next.Seq) {
			next = o
		}
	}
	var out []Item
	for _, o := range []*Item{prev, next} {
		if o != nil {
			out = append(out, cloneItem(o))
		}
	}
	return out
}

// relatedFor picks related items of the same memory. Items the harness added (notes and long-term
// memories it fetched from Node 1) always qualify and come first, since the harness put them there
// for the model; other chunks qualify if they share words with the task and the chunk. Ties go to
// more shared words, then to the stronger item. Thoughts and superseded items are left out, so the
// model never builds on an earlier thought or on text that was corrected.
func relatedFor(items []*Item, chunk *Item, context []Item, task string, limit int) []Item {
	if limit <= 0 {
		return nil
	}
	skip := map[string]bool{chunk.ItemID: true, chunk.Supersedes: true}
	for _, c := range context {
		skip[c.ItemID] = true
	}
	query := wordSet(task + " " + chunk.Text)
	type cand struct {
		it      *Item
		harness bool
		score   int
	}
	var cands []cand
	for _, o := range items {
		if skip[o.ItemID] || o.Origin == OriginThought || o.SupersededBy != "" {
			continue
		}
		harness := o.Origin == OriginHarness || o.Origin == OriginLongTerm
		if sc := overlap(query, o.Text); sc > 0 || harness {
			cands = append(cands, cand{o, harness, sc})
		}
	}
	sort.SliceStable(cands, func(a, b int) bool {
		if cands[a].harness != cands[b].harness {
			return cands[a].harness
		}
		if cands[a].score != cands[b].score {
			return cands[a].score > cands[b].score
		}
		return cands[a].it.Strength > cands[b].it.Strength
	})
	var out []Item
	for i := 0; i < len(cands) && i < limit; i++ {
		out = append(out, cloneItem(cands[i].it))
	}
	return out
}

// expireLocked marks every queued chunk that has waited longer than the wait limit as timed out.
// Timed-out chunks stay in the memory and are committed unprocessed with their starting strength.
func (s *Store) expireLocked(now time.Time) {
	if s.opts.WaitLimit <= 0 {
		return
	}
	var txn *memdb.Txn
	for id, st := range s.state {
		kept := st.queue[:0]
		for _, itemID := range st.queue {
			if txn == nil {
				txn = s.db.Txn(true)
			}
			it := getItem(txn, id, itemID)
			if now.Sub(it.enqueuedAt) >= s.opts.WaitLimit {
				it.Status = StatusTimedOut
				putItem(txn, it)
				touch(txn, id, now)
				continue
			}
			kept = append(kept, itemID)
		}
		st.queue = kept
	}
	if txn != nil {
		txn.Commit()
	}
}

// Result is the outcome of one model call.
type Result struct {
	Thought     string
	Used        []string // item IDs from the job's snapshot that the thought relies on
	Score       *float64 // the thought's score against the task, if it could be scored
	ScoreStatus string
	Err         error
}

// FinishJob applies a model call's result to the memory: the chunk is marked done (or failed), the
// thought is stored as a new item, and the items it used are reinforced. A reinforcement of an item
// forgotten mid-call is dropped; one of an item corrected mid-call goes to the newest version. If
// the chunk itself was forgotten mid-call, its thought is dropped too. It returns the stored
// thought, if any.
func (s *Store) FinishJob(job *Job, res Result) *Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.cond.Broadcast()

	st := s.state[job.MemoryID]
	if st == nil {
		return nil
	}
	st.inFlight--

	txn := s.db.Txn(true)
	defer txn.Abort()
	now := s.opts.Now()
	m := getMemory(txn, job.MemoryID)
	m.LastActivity = now
	putMemory(txn, m)

	chunk := getItem(txn, job.MemoryID, job.Chunk.ItemID)
	if chunk == nil {
		txn.Commit()
		return nil
	}
	if res.Err != nil || res.Thought == "" {
		chunk.Status = StatusFailed
		putItem(txn, chunk)
		txn.Commit()
		return nil
	}
	chunk.Status = StatusDone
	putItem(txn, chunk)

	var uses []string
	for _, id := range res.Used {
		if getItem(txn, job.MemoryID, id) == nil {
			continue // forgotten mid-call
		}
		uses = append(uses, id)
		if live := resolve(txn, job.MemoryID, id); live != nil {
			s.reinforce(live, 0)
			putItem(txn, live)
		}
	}

	thought := &Item{
		Origin:      OriginThought,
		Text:        res.Thought,
		Type:        "thought",
		Source:      SpeakerWorkingMemory,
		Session:     chunk.Session,
		Speaker:     SpeakerWorkingMemory,
		TS:          now,
		TaskScore:   res.Score,
		ScoreStatus: res.ScoreStatus,
		Strength:    startStrength(res.Score, chunk.start),
		DerivedFrom: chunk.ItemID,
		Uses:        uses,
	}
	addItem(txn, m, thought)
	putMemory(txn, m)
	txn.Commit()
	s.items++
	out := cloneItem(thought)
	return &out
}

func (s *Store) reinforce(it *Item, amount float64) {
	if amount <= 0 {
		amount = s.opts.ReinforceStep
	}
	it.Strength += amount
	it.Reinforcements++
}

// --- harness operations ---

// openLocked checks that a memory exists and accepts edits.
func (s *Store) openLocked(memoryID string) error {
	st := s.state[memoryID]
	if st == nil {
		return ErrMemoryNotFound
	}
	if st.committing {
		return ErrCommitting
	}
	return nil
}

// Items lists a memory's items in the order they were added.
func (s *Store) Items(memoryID string) ([]Item, error) {
	txn := s.db.Txn(false)
	if getMemory(txn, memoryID) == nil {
		return nil, ErrMemoryNotFound
	}
	items := memoryItems(txn, memoryID)
	out := make([]Item, len(items))
	for i, it := range items {
		out[i] = *it
	}
	return out, nil
}

func (s *Store) memoryTask(memoryID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(memoryID); err != nil {
		return "", err
	}
	return getMemory(s.db.Txn(false), memoryID).Task, nil
}

// Score scores text against a task with the store's scorer.
func (s *Store) Score(ctx context.Context, task, text string) (*float64, string) {
	if task == "" {
		return nil, "no_task"
	}
	if s.scorer == nil {
		return nil, "disabled"
	}
	return s.scorer.Score(ctx, task, text)
}

// AddRequest is a harness add.
type AddRequest struct {
	Text  string
	Agent string // the calling agent; stored as the item's speaker
	Type  string // defaults to "note"
	// NodeID marks a long-term item the harness fetched from Node 1. It is never written to Node 1
	// again: if the model or the harness reinforces it, the commit reinforces its node instead.
	NodeID string
}

// Add stores a new item from the harness, labelled source "harness" with the calling agent as
// speaker and scored against the memory's task for its starting strength. Adding a Node 1 node the
// memory already holds returns the held item.
func (s *Store) Add(ctx context.Context, memoryID string, req AddRequest) (Item, error) {
	if req.Text == "" {
		return Item{}, ErrEmptyText
	}
	if req.Agent == "" {
		return Item{}, ErrMissingAgent
	}
	task, err := s.memoryTask(memoryID)
	if err != nil {
		return Item{}, err
	}
	score, status := s.Score(ctx, task, req.Text)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(memoryID); err != nil {
		return Item{}, err
	}
	txn := s.db.Txn(true)
	defer txn.Abort()
	m := getMemory(txn, memoryID)
	if req.NodeID != "" {
		for _, it := range memoryItems(txn, memoryID) {
			if it.NodeID == req.NodeID {
				return *it, nil
			}
		}
	}
	if s.opts.MaxItems > 0 && s.items >= s.opts.MaxItems {
		return Item{}, ErrFull
	}
	itemType := req.Type
	if itemType == "" {
		itemType = "note"
	}
	now := s.opts.Now()
	it := &Item{
		Origin:      OriginHarness,
		Text:        req.Text,
		Type:        itemType,
		Source:      string(OriginHarness),
		Session:     m.Session,
		Speaker:     req.Agent,
		NodeID:      req.NodeID,
		TS:          now,
		TaskScore:   score,
		ScoreStatus: status,
		Strength:    startStrength(score, 0),
	}
	if req.NodeID != "" {
		it.Origin = OriginLongTerm
	}
	addItem(txn, m, it)
	m.LastActivity = now
	putMemory(txn, m)
	txn.Commit()
	s.items++
	return cloneItem(it), nil
}

// Correct supersedes an item: the correction is stored as a new harness item and the original is
// linked superseded_by it. Both are committed. If the original was still queued, the correction
// takes its place in the queue.
func (s *Store) Correct(ctx context.Context, memoryID, itemID, text, agent string) (Item, error) {
	if text == "" {
		return Item{}, ErrEmptyText
	}
	if agent == "" {
		return Item{}, ErrMissingAgent
	}
	task, err := s.memoryTask(memoryID)
	if err != nil {
		return Item{}, err
	}
	score, status := s.Score(ctx, task, text)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(memoryID); err != nil {
		return Item{}, err
	}
	txn := s.db.Txn(true)
	defer txn.Abort()
	orig := getItem(txn, memoryID, itemID)
	if orig == nil {
		return Item{}, ErrItemNotFound
	}
	if orig.SupersededBy != "" {
		return Item{}, fmt.Errorf("%w by %s", ErrSuperseded, orig.SupersededBy)
	}
	if s.opts.MaxItems > 0 && s.items >= s.opts.MaxItems {
		return Item{}, ErrFull
	}

	m := getMemory(txn, memoryID)
	now := s.opts.Now()
	it := &Item{
		Origin:      OriginHarness,
		Text:        text,
		Type:        orig.Type,
		Source:      string(OriginHarness),
		Session:     orig.Session,
		Speaker:     agent,
		TurnID:      orig.TurnID,
		TS:          now,
		TaskScore:   score,
		ScoreStatus: status,
		Strength:    startStrength(score, orig.Strength),
		Supersedes:  orig.ItemID,
	}
	if it.Session == "" {
		it.Session = m.Session
	}
	st := s.state[memoryID]
	queued := slices.Index(st.queue, orig.ItemID)
	if queued >= 0 {
		it.Strong = true
		it.Status = StatusQueued
		it.enqueuedAt = orig.enqueuedAt
		it.Epoch, it.Seq = orig.Epoch, orig.Seq // keeps its neighbours as context
	}
	addItem(txn, m, it)
	orig.SupersededBy = it.ItemID
	if queued >= 0 {
		st.queue[queued] = it.ItemID
		orig.Status = StatusSuperseded
	}
	putItem(txn, orig)
	m.LastActivity = now
	putMemory(txn, m)
	txn.Commit()
	s.items++
	return cloneItem(it), nil
}

// Forget deletes an item from the temp DB before commit; it is never sent to Node 1. An item
// forgotten while it is being processed loses its thought as well.
func (s *Store) Forget(memoryID, itemID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(memoryID); err != nil {
		return err
	}
	txn := s.db.Txn(true)
	defer txn.Abort()
	it := getItem(txn, memoryID, itemID)
	if it == nil {
		return ErrItemNotFound
	}
	if err := txn.Delete(tableItems, it); err != nil {
		return fmt.Errorf("forget: %w", err)
	}
	for _, o := range memoryItems(txn, memoryID) {
		changed := false
		if o.SupersededBy == itemID {
			o.SupersededBy, changed = "", true
		}
		if o.Supersedes == itemID {
			o.Supersedes, changed = "", true
		}
		if changed {
			putItem(txn, o)
		}
	}
	touch(txn, memoryID, s.opts.Now())
	txn.Commit()

	st := s.state[memoryID]
	st.queue = slices.DeleteFunc(st.queue, func(id string) bool { return id == itemID })
	s.items--
	s.cond.Broadcast()
	return nil
}

// Reinforce strengthens an item (the newest version if it was corrected). amount <= 0 uses the
// configured step.
func (s *Store) Reinforce(memoryID, itemID string, amount float64) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(memoryID); err != nil {
		return Item{}, err
	}
	txn := s.db.Txn(true)
	defer txn.Abort()
	it := resolve(txn, memoryID, itemID)
	if it == nil {
		return Item{}, ErrItemNotFound
	}
	s.reinforce(it, amount)
	putItem(txn, it)
	touch(txn, memoryID, s.opts.Now())
	txn.Commit()
	return cloneItem(it), nil
}

// --- commit ---

// Commit sends a memory to Node 1 and then deletes it from the temp DB. It never commits mid-call:
// it first waits for the memory's in-flight model calls to finish, and their thoughts are included.
// Chunks still queued are committed unprocessed. If the write fails the memory is kept, and the
// idle sweep retries later. A commit of a memory already being committed waits for that commit.
func (s *Store) Commit(ctx context.Context, memoryID, reason string) (CommitSummary, error) {
	s.mu.Lock()
	st := s.state[memoryID]
	if st == nil {
		sum, ok := s.committed[memoryID]
		s.mu.Unlock()
		if ok {
			return sum, nil
		}
		return CommitSummary{}, ErrMemoryNotFound
	}
	if st.committing {
		done := st.commitDone
		s.mu.Unlock()
		select {
		case <-done:
			return st.commitResult, st.commitErr
		case <-ctx.Done():
			return CommitSummary{}, ctx.Err()
		}
	}

	st.committing = true
	st.commitDone = make(chan struct{})
	if len(st.queue) > 0 {
		txn := s.db.Txn(true)
		for _, id := range st.queue {
			it := getItem(txn, memoryID, id)
			it.Status = StatusUnprocessed
			putItem(txn, it)
		}
		txn.Commit()
		st.queue = nil
	}
	for st.inFlight > 0 {
		s.cond.Wait()
	}
	batch := buildBatch(s.db.Txn(false), memoryID, reason, s.opts.Now())
	s.mu.Unlock()

	err := s.committer.Commit(ctx, batch)

	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.cond.Broadcast()
	late := st.late
	st.late = nil
	now := s.opts.Now()
	if err != nil {
		txn := s.db.Txn(true)
		m := getMemory(txn, memoryID)
		m.LastCommitError = err.Error()
		m.NextCommitAttempt = now.Add(s.opts.IdleTimeout)
		for _, c := range late {
			s.addChunkLocked(txn, m, c.epoch, c.chunk, now)
		}
		putMemory(txn, m)
		txn.Commit()
		st.committing = false
		st.commitErr = fmt.Errorf("commit %s: %w", memoryID, err)
		close(st.commitDone)
		return CommitSummary{}, st.commitErr
	}

	// Delete the memory and all its items from the temp DB in one transaction.
	txn := s.db.Txn(true)
	m := getMemory(txn, memoryID)
	deleted, derr := txn.DeleteAll(tableItems, "memory_id", memoryID)
	if derr != nil {
		txn.Abort()
		panic(fmt.Sprintf("working memory: delete items of %s: %v", memoryID, derr))
	}
	if derr := txn.Delete(tableMemories, &memoryRecord{ID: memoryID}); derr != nil {
		txn.Abort()
		panic(fmt.Sprintf("working memory: delete memory %s: %v", memoryID, derr))
	}
	s.items -= deleted
	s.removeMemoryLocked(memoryID)

	sum := CommitSummary{
		MemoryID:       memoryID,
		Reason:         reason,
		Items:          len(batch.Items),
		Reinforcements: len(batch.Reinforcements),
		CommittedAt:    batch.CommittedAt,
	}
	s.rememberCommitLocked(sum)
	st.commitResult = sum
	close(st.commitDone)

	// Chunks that arrived during the commit start a new generation of the memory.
	if len(late) > 0 {
		nm := s.newMemoryLocked(memoryID, m.Task, m.Session, now)
		for _, c := range late {
			s.addChunkLocked(txn, nm, c.epoch, c.chunk, now)
		}
		putMemory(txn, nm)
	}
	txn.Commit()
	return sum, nil
}

func (s *Store) removeMemoryLocked(memoryID string) {
	delete(s.state, memoryID)
	if i := slices.Index(s.ring, memoryID); i >= 0 {
		s.ring = slices.Delete(s.ring, i, i+1)
		if i < s.cursor {
			s.cursor--
		}
	}
}

func (s *Store) rememberCommitLocked(sum CommitSummary) {
	if _, ok := s.committed[sum.MemoryID]; !ok {
		s.committedOrder = append(s.committedOrder, sum.MemoryID)
	}
	s.committed[sum.MemoryID] = sum
	for len(s.committedOrder) > maxCommitted {
		delete(s.committed, s.committedOrder[0])
		s.committedOrder = s.committedOrder[1:]
	}
}

// buildBatch turns a memory into its commit batch. Items the harness fetched from Node 1 are never
// written again: a reinforced one becomes a reinforcement of its existing node.
func buildBatch(txn *memdb.Txn, memoryID, reason string, now time.Time) CommitBatch {
	batch := CommitBatch{MemoryID: memoryID, Reason: reason, CommittedAt: now}

	link := func(rel, id string) (Link, bool) {
		t := getItem(txn, memoryID, id)
		if t == nil {
			return Link{}, false
		}
		if t.NodeID != "" {
			return Link{Rel: rel, Node: t.NodeID}, true
		}
		return Link{Rel: rel, Item: id}, true
	}

	for _, it := range memoryItems(txn, memoryID) {
		if it.NodeID != "" {
			if it.Reinforcements > 0 {
				batch.Reinforcements = append(batch.Reinforcements, Reinforcement{
					NodeID:         it.NodeID,
					Reinforcements: it.Reinforcements,
					Delta:          it.Strength - it.start,
				})
			}
			continue
		}

		ci := CommitItem{
			ItemID:      it.ItemID,
			Origin:      it.Origin,
			Text:        it.Text,
			ChunkID:     it.ChunkID,
			Type:        it.Type,
			Source:      it.Source,
			Session:     it.Session,
			Speaker:     it.Speaker,
			TurnID:      it.TurnID,
			ParentID:    it.ParentID,
			Part:        it.Part,
			Parts:       it.Parts,
			Heading:     it.Heading,
			Format:      it.Format,
			TS:          it.TS,
			ScoreStatus: it.ScoreStatus,
			Strength:    it.Strength,
		}
		if it.Origin == OriginSensory {
			// A correction holds its original's seq only to keep the same neighbours as context;
			// (memory_id, seq) stays unique to Node 3 chunks.
			ci.Seq = it.Seq
		}
		if it.Strong {
			ci.Processing = it.Status
		}
		for _, l := range []struct{ rel, id string }{
			{RelSupersedes, it.Supersedes},
			{RelSupersededBy, it.SupersededBy},
			{RelDerivedFrom, it.DerivedFrom},
		} {
			if l.id == "" {
				continue
			}
			if lk, ok := link(l.rel, l.id); ok {
				ci.Links = append(ci.Links, lk)
			}
		}
		for _, u := range it.Uses {
			if lk, ok := link(RelUses, u); ok {
				ci.Links = append(ci.Links, lk)
			}
		}
		batch.Items = append(batch.Items, ci)
	}
	return batch
}

// EndSession commits every memory of a session (the conversation) and returns what was committed.
func (s *Store) EndSession(ctx context.Context, session string) ([]CommitSummary, error) {
	var ids []string
	iter, err := s.db.Txn(false).Get(tableMemories, "session", session)
	if err == nil {
		for raw := iter.Next(); raw != nil; raw = iter.Next() {
			ids = append(ids, raw.(*memoryRecord).ID)
		}
	}
	var out []CommitSummary
	var errs []error
	for _, id := range ids {
		sum, err := s.Commit(ctx, id, "session_end")
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, sum)
	}
	return out, errors.Join(errs...)
}

// Sweep marks queued chunks past the wait limit as timed out and returns the memories that are due
// for an idle commit: nothing queued or in flight and no activity for the idle timeout.
func (s *Store) Sweep() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.opts.Now()
	s.expireLocked(now)
	txn := s.db.Txn(false)
	var due []string
	for _, id := range s.ring {
		st := s.state[id]
		if st.committing || len(st.queue) > 0 || st.inFlight > 0 {
			continue
		}
		m := getMemory(txn, id)
		if now.Sub(m.LastActivity) < s.opts.IdleTimeout || now.Before(m.NextCommitAttempt) {
			continue
		}
		due = append(due, id)
	}
	return due
}
