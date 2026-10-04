package workingmemory

import (
	"sort"
	"strings"
	"time"
	"unicode"
)

// Memory states reported by Status.
const (
	StateOpen       = "open"
	StateCommitting = "committing"
	StateCommitted  = "committed"
)

// ChunkProgress is the processing state of one strong chunk.
type ChunkProgress struct {
	ItemID string      `json:"item_id"`
	Seq    int64       `json:"seq,omitempty"`
	Status ChunkStatus `json:"status"`
}

// Counts summarises a memory's items.
type Counts struct {
	Items       int `json:"items"`
	Strong      int `json:"strong"`
	Queued      int `json:"queued"`
	Processing  int `json:"processing"`
	Done        int `json:"done"`
	TimedOut    int `json:"timed_out"`
	Failed      int `json:"failed"`
	Unprocessed int `json:"unprocessed"`
	Superseded  int `json:"superseded"`
	Thoughts    int `json:"thoughts"`
}

// MemoryStatus reports one memory's progress for the harness to poll.
type MemoryStatus struct {
	MemoryID        string          `json:"memory_id"`
	State           string          `json:"state"`
	Task            string          `json:"task,omitempty"`
	Session         string          `json:"session,omitempty"`
	Counts          Counts          `json:"counts"`
	Chunks          []ChunkProgress `json:"chunks,omitempty"`
	Thoughts        []Item          `json:"thoughts,omitempty"`
	CreatedAt       time.Time       `json:"created_at,omitempty"`
	LastActivity    time.Time       `json:"last_activity,omitempty"`
	LastCommitError string          `json:"last_commit_error,omitempty"`
	Committed       *CommitSummary  `json:"committed,omitempty"`
}

// Status reports a memory's progress: per-chunk status, counts and the thoughts produced so far. A
// recently committed memory reports state "committed" and holds no items.
func (s *Store) Status(memoryID string) (MemoryStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := s.state[memoryID]
	if st == nil {
		if sum, ok := s.committed[memoryID]; ok {
			return MemoryStatus{MemoryID: memoryID, State: StateCommitted, Committed: &sum}, nil
		}
		return MemoryStatus{}, ErrMemoryNotFound
	}
	return s.statusLocked(memoryID, st, true), nil
}

// Memories reports every memory held, without per-chunk detail.
func (s *Store) Memories() []MemoryStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]MemoryStatus, 0, len(s.ring))
	for _, id := range s.ring {
		out = append(out, s.statusLocked(id, s.state[id], false))
	}
	return out
}

// Stats summarises the whole store.
type Stats struct {
	Memories  int `json:"memories"`
	Items     int `json:"items"`
	Queued    int `json:"queued"`
	InFlight  int `json:"in_flight"`
	MaxItems  int `json:"max_items,omitempty"`
	Committed int `json:"committed_recent"`
}

// Stats reports totals across the store.
func (s *Store) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := Stats{Memories: len(s.state), Items: s.items, MaxItems: s.opts.MaxItems, Committed: len(s.committed)}
	for _, m := range s.state {
		st.Queued += len(m.queue)
		st.InFlight += m.inFlight
	}
	return st
}

func (s *Store) statusLocked(memoryID string, ms *memState, detail bool) MemoryStatus {
	txn := s.db.Txn(false)
	m := getMemory(txn, memoryID)
	st := MemoryStatus{
		MemoryID:        m.ID,
		State:           StateOpen,
		Task:            m.Task,
		Session:         m.Session,
		CreatedAt:       m.CreatedAt,
		LastActivity:    m.LastActivity,
		LastCommitError: m.LastCommitError,
	}
	if ms.committing {
		st.State = StateCommitting
	}
	c := &st.Counts
	for _, it := range memoryItems(txn, memoryID) {
		c.Items++
		if it.Origin == OriginThought {
			c.Thoughts++
			if detail {
				st.Thoughts = append(st.Thoughts, *it)
			}
		}
		if !it.Strong {
			continue
		}
		c.Strong++
		switch it.Status {
		case StatusQueued:
			c.Queued++
		case StatusProcessing:
			c.Processing++
		case StatusDone:
			c.Done++
		case StatusTimedOut:
			c.TimedOut++
		case StatusFailed:
			c.Failed++
		case StatusUnprocessed:
			c.Unprocessed++
		case StatusSuperseded:
			c.Superseded++
		}
		if detail {
			st.Chunks = append(st.Chunks, ChunkProgress{ItemID: it.ItemID, Seq: it.Seq, Status: it.Status})
		}
	}
	return st
}

// RecallQuery is a harness recall against short-term memory.
type RecallQuery struct {
	Query    string
	MemoryID string // optional: only this memory
	Session  string // optional: only memories of this session
	Limit    int    // default 10
}

// RecallHit is one item found by Recall.
type RecallHit struct {
	Item
	Score int `json:"score"` // words shared with the query
}

// Recall searches the items held in the temp DB for the words of the query and returns the best
// matches, most shared words first (ties: stronger first). It is the harness's short-term recall;
// Node 2 never recalls from Node 1.
func (s *Store) Recall(q RecallQuery) []RecallHit {
	limit := q.Limit
	if limit <= 0 {
		limit = 10
	}
	words := wordSet(q.Query)
	if len(words) == 0 {
		return nil
	}

	txn := s.db.Txn(false)
	var memoryIDs []string
	iter, err := txn.Get(tableMemories, "id")
	if err != nil {
		return nil
	}
	for raw := iter.Next(); raw != nil; raw = iter.Next() {
		m := raw.(*memoryRecord)
		if (q.MemoryID == "" || m.ID == q.MemoryID) && (q.Session == "" || m.Session == q.Session) {
			memoryIDs = append(memoryIDs, m.ID)
		}
	}

	var hits []RecallHit
	for _, id := range memoryIDs {
		for _, it := range memoryItems(txn, id) {
			if sc := overlap(words, it.Text); sc > 0 {
				hits = append(hits, RecallHit{Item: *it, Score: sc})
			}
		}
	}
	sort.SliceStable(hits, func(a, b int) bool {
		if hits[a].Score != hits[b].Score {
			return hits[a].Score > hits[b].Score
		}
		return hits[a].Strength > hits[b].Strength
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "was": true, "are": true, "that": true, "this": true,
	"with": true, "you": true, "not": true, "but": true, "have": true, "has": true, "had": true,
	"its": true, "from": true, "they": true, "will": true, "what": true, "when": true, "which": true,
	"who": true, "how": true, "did": true, "does": true, "were": true, "been": true, "into": true,
}

// wordSet returns the distinct lower-case words of at least 3 letters or digits, without common
// stop words.
func wordSet(text string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	}) {
		if len([]rune(w)) >= 3 && !stopWords[w] {
			out[w] = true
		}
	}
	return out
}

// overlap counts the distinct words of text that are in words.
func overlap(words map[string]bool, text string) int {
	n := 0
	for w := range wordSet(text) {
		if words[w] {
			n++
		}
	}
	return n
}
