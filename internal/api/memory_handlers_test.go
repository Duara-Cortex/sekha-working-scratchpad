package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/budgeter"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/version"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/workingmemory"
)

type recordingCommitter struct {
	mu      sync.Mutex
	batches []workingmemory.CommitBatch
}

func (r *recordingCommitter) Commit(_ context.Context, b workingmemory.CommitBatch) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.batches = append(r.batches, b)
	return nil
}

func do(t *testing.T, s *Server, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestMemoryEndpoints(t *testing.T) {
	committer := &recordingCommitter{}
	store := workingmemory.NewStore(workingmemory.Options{IdleTimeout: time.Minute, ReinforceStep: 0.1}, committer, nil)
	s := NewServer(store, budgeter.New(budgeter.DefaultConfig()), &mockEngine{})

	strong := 0.8
	store.AddChunks("e1", []workingmemory.Chunk{
		{ID: "a", MemoryID: "m1", Text: "did the backup run?", Type: "dialogue", Source: "t", Session: "conv", Speaker: "user", Seq: 1},
		{ID: "b", MemoryID: "m1", Text: "it ran at 02:00", Type: "dialogue", Source: "t", Session: "conv", Speaker: "assistant", Seq: 2, TaskScore: &strong, Strong: true, Task: "when did the backup run?"},
	})
	const base = "/api/v1/working/memories/m1"

	if code, st := do(t, s, http.MethodGet, base, ""); code != 200 || st["state"] != "open" {
		t.Fatalf("status: %d %v", code, st)
	}
	if code, _ := do(t, s, http.MethodPost, base+"/items", `{"text":"note","agent":"a1"}`); code != http.StatusCreated {
		t.Fatalf("add: %d", code)
	}
	if code, _ := do(t, s, http.MethodPost, base+"/items", `{"text":"note"}`); code != http.StatusBadRequest {
		t.Fatalf("add without agent: %d", code)
	}
	if code, _ := do(t, s, http.MethodPost, base+"/items", `{"text":"x","agent":"a","extra":1}`); code != http.StatusBadRequest {
		t.Fatalf("add with unknown field: %d", code)
	}
	if code, it := do(t, s, http.MethodPost, base+"/items/i2/correct", `{"text":"it ran at 02:10","agent":"a1"}`); code != http.StatusCreated || it["supersedes"] != "i2" {
		t.Fatalf("correct: %d %v", code, it)
	}
	if code, _ := do(t, s, http.MethodPost, base+"/items/i2/correct", `{"text":"again","agent":"a1"}`); code != http.StatusConflict {
		t.Fatalf("second correct: %d", code)
	}
	if code, it := do(t, s, http.MethodPost, base+"/items/i2/reinforce", ""); code != 200 || it["item_id"] != "i4" {
		t.Fatalf("reinforce of a corrected item should reach the correction: %d %v", code, it)
	}
	if code, _ := do(t, s, http.MethodDelete, base+"/items/i3", ""); code != 200 {
		t.Fatalf("forget: %d", code)
	}
	if code, _ := do(t, s, http.MethodDelete, base+"/items/i3", ""); code != http.StatusNotFound {
		t.Fatalf("forget twice: %d", code)
	}
	if code, list := do(t, s, http.MethodGet, base+"/items", ""); code != 200 || len(list["items"].([]any)) != 3 {
		t.Fatalf("items: %d %v", code, list)
	}

	if code, sum := do(t, s, http.MethodPost, base+"/commit", ""); code != 200 || sum["items"] != float64(3) {
		t.Fatalf("commit: %d %v", code, sum)
	}
	if code, _ := do(t, s, http.MethodGet, base+"/items", ""); code != http.StatusNotFound {
		t.Fatalf("items after commit: %d", code)
	}
	if code, st := do(t, s, http.MethodGet, base, ""); code != 200 || st["state"] != "committed" {
		t.Fatalf("status after commit: %d %v", code, st)
	}
	if code, _ := do(t, s, http.MethodGet, "/api/v1/working/memories/nope", ""); code != http.StatusNotFound {
		t.Fatalf("unknown memory: %d", code)
	}
	if len(committer.batches) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(committer.batches))
	}
}

func TestEndSessionEndpoint(t *testing.T) {
	store := workingmemory.NewStore(workingmemory.Options{IdleTimeout: time.Minute}, &recordingCommitter{}, nil)
	s := NewServer(store, nil, &mockEngine{})
	store.AddChunks("e1", []workingmemory.Chunk{
		{MemoryID: "m1", Text: "a", Session: "conv", Seq: 1},
		{MemoryID: "m2", Text: "b", Session: "conv", Seq: 2},
		{MemoryID: "m3", Text: "c", Session: "other", Seq: 3},
	})
	code, resp := do(t, s, http.MethodPost, "/api/v1/working/sessions/conv/end", "")
	if code != 200 || len(resp["committed"].([]any)) != 2 {
		t.Fatalf("end session: %d %v", code, resp)
	}
	if st := store.Stats(); st.Memories != 1 {
		t.Fatalf("expected 1 memory left, got %+v", st)
	}
}

func TestHealthAndStatsReportVersion(t *testing.T) {
	s := NewServer(newTestMemory(), nil, &mockEngine{})
	for _, path := range []string{"/api/v1/working/health", "/api/v1/working/stats"} {
		if code, resp := do(t, s, http.MethodGet, path, ""); code != 200 || resp["version"] != version.Version {
			t.Fatalf("%s: %d %v", path, code, resp)
		}
	}
}

func TestMemoryEndpointsWithoutStore(t *testing.T) {
	s := NewServer(nil, nil, &mockEngine{})
	if code, _ := do(t, s, http.MethodGet, "/api/v1/working/memories", ""); code != http.StatusServiceUnavailable {
		t.Fatalf("got %d", code)
	}
}

// Node 3 pushes chunks to Node 2; a 200 tells it what it may evict.
func TestReceiveChunksEndpoint(t *testing.T) {
	store := workingmemory.NewStore(workingmemory.Options{IdleTimeout: time.Minute, MaxItems: 3}, &recordingCommitter{}, nil)
	s := NewServer(store, nil, &mockEngine{})
	batch := `{"epoch":"e1","chunks":[
		{"id":"h1","memory_id":"m1","text":"did the lantern backup run?","type":"dialogue","source":"t","session":"conv","speaker":"user","seq":1,"task_score":null,"strong":false,"score_status":"no_task","new_node3_field":1},
		{"id":"h2","memory_id":"m1","text":"yes, at 02:00","type":"dialogue","source":"t","session":"conv","speaker":"assistant","seq":2,"task_score":null,"strong":false,"score_status":"no_task"}]}`

	code, res := do(t, s, http.MethodPost, "/api/v1/working/chunks", batch)
	if code != 200 || res["accepted"] != float64(2) || res["accepted_up_to_seq"] != float64(2) {
		t.Fatalf("push: %d %v", code, res)
	}
	if code, res := do(t, s, http.MethodPost, "/api/v1/working/chunks", batch); code != 200 || res["duplicates"] != float64(2) {
		t.Fatalf("retried push: %d %v", code, res)
	}
	full := `{"epoch":"e1","chunks":[{"memory_id":"m2","text":"a","seq":3},{"memory_id":"m2","text":"b","seq":4}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/working/chunks", strings.NewReader(full))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Fatalf("push beyond capacity: %d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
	if code, _ := do(t, s, http.MethodPost, "/api/v1/working/chunks", `{"chunks":[{"memory_id":"m3","seq":9}]}`); code != http.StatusBadRequest {
		t.Fatalf("push without epoch: %d", code)
	}

	code, hits := do(t, s, http.MethodPost, "/api/v1/working/recall", `{"query":"lantern backup","session":"conv"}`)
	if code != 200 || len(hits["items"].([]any)) != 1 {
		t.Fatalf("recall: %d %v", code, hits)
	}
	if code, _ := do(t, s, http.MethodPost, "/api/v1/working/recall", `{}`); code != http.StatusBadRequest {
		t.Fatalf("recall without query: %d", code)
	}
}

func TestReceiveChunksWithoutCommitTarget(t *testing.T) {
	s := NewServer(newTestMemory(), nil, &mockEngine{})
	code, _ := do(t, s, http.MethodPost, "/api/v1/working/chunks", `{"epoch":"e1","chunks":[{"memory_id":"m1","text":"a","seq":1}]}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("push with no commit target: %d", code)
	}
}

func TestAddLongTermItemEndpoint(t *testing.T) {
	store := workingmemory.NewStore(workingmemory.Options{IdleTimeout: time.Minute}, &recordingCommitter{}, nil)
	s := NewServer(store, nil, &mockEngine{})
	store.AddChunks("e1", []workingmemory.Chunk{{MemoryID: "m1", Text: "a", Seq: 1}})
	code, it := do(t, s, http.MethodPost, "/api/v1/working/memories/m1/items", `{"text":"backups run nightly","agent":"cortex","node_id":"n-17"}`)
	if code != http.StatusCreated || it["node_id"] != "n-17" || it["origin"] != "node1" {
		t.Fatalf("add long-term item: %d %v", code, it)
	}
	if code, _ := do(t, s, http.MethodPost, "/api/v1/working/memories/m1/items/i1/correct", `{"text":"x","agent":"a","node_id":"n-1"}`); code != http.StatusBadRequest {
		t.Fatalf("correct with node_id: %d", code)
	}
}
