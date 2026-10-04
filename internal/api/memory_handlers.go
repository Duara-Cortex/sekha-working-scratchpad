package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/workingmemory"
)

func (s *Server) registerMemoryRoutes() {
	const base = "/api/v1/working/memories"
	s.mux.HandleFunc("POST /api/v1/working/chunks", s.withMemory(s.handleReceiveChunks))
	s.mux.HandleFunc("POST /api/v1/working/recall", s.withMemory(s.handleRecall))
	s.mux.HandleFunc("GET "+base, s.withMemory(s.handleListMemories))
	s.mux.HandleFunc("GET "+base+"/{memory_id}", s.withMemory(s.handleMemoryStatus))
	s.mux.HandleFunc("GET "+base+"/{memory_id}/items", s.withMemory(s.handleListItems))
	s.mux.HandleFunc("POST "+base+"/{memory_id}/items", s.withMemory(s.handleAddItem))
	s.mux.HandleFunc("POST "+base+"/{memory_id}/items/{item_id}/correct", s.withMemory(s.handleCorrectItem))
	s.mux.HandleFunc("DELETE "+base+"/{memory_id}/items/{item_id}", s.withMemory(s.handleForgetItem))
	s.mux.HandleFunc("POST "+base+"/{memory_id}/items/{item_id}/reinforce", s.withMemory(s.handleReinforceItem))
	s.mux.HandleFunc("POST "+base+"/{memory_id}/commit", s.withMemory(s.handleCommit))
	s.mux.HandleFunc("POST /api/v1/working/sessions/{session}/end", s.withMemory(s.handleEndSession))
}

func (s *Server) withMemory(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Memory == nil {
			writeError(w, http.StatusServiceUnavailable, "working memory is not enabled")
			return
		}
		h(w, r)
	}
}

// writeMemoryError maps store errors to HTTP statuses.
func writeMemoryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, workingmemory.ErrMemoryNotFound), errors.Is(err, workingmemory.ErrItemNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, workingmemory.ErrCommitting), errors.Is(err, workingmemory.ErrSuperseded):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, workingmemory.ErrEmptyText), errors.Is(err, workingmemory.ErrMissingAgent),
		errors.Is(err, workingmemory.ErrInvalidBatch):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, workingmemory.ErrFull), errors.Is(err, workingmemory.ErrNoCommitTarget):
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds))
		writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		writeError(w, http.StatusBadGateway, err.Error())
	}
}

// retryAfterSeconds is the Retry-After sent when working memory cannot take more chunks.
const retryAfterSeconds = 5

// maxChunkBatchBytes caps one pushed batch (Node 3 pages hold at most 4096 chunks of ~1 KiB).
const maxChunkBatchBytes = 8 << 20

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	return decodeLimited(w, r, dst, 1<<20, true)
}

func decodeLimited(w http.ResponseWriter, r *http.Request, dst any, limit int64, strict bool) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json request payload: "+err.Error())
		return false
	}
	return true
}

// handleReceiveChunks is where Node 3 pushes chunks. A 200 means every chunk up to
// accepted_up_to_seq is in working memory and Node 3 may evict it. Unknown chunk fields are
// tolerated so Node 3 can add fields without breaking Node 2.
func (s *Server) handleReceiveChunks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Epoch  string                `json:"epoch"`
		Chunks []workingmemory.Chunk `json:"chunks"`
	}
	if !decodeLimited(w, r, &req, maxChunkBatchBytes, false) {
		return
	}
	res, err := s.Memory.Receive(req.Epoch, req.Chunks)
	if err != nil {
		writeMemoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleRecall is the harness's short-term recall: it searches the items held in the temp DB.
func (s *Server) handleRecall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query    string `json:"query"`
		MemoryID string `json:"memory_id,omitempty"`
		Session  string `json:"session,omitempty"`
		Limit    int    `json:"limit,omitempty"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}
	hits := s.Memory.Recall(workingmemory.RecallQuery{Query: req.Query, MemoryID: req.MemoryID, Session: req.Session, Limit: req.Limit})
	if hits == nil {
		hits = []workingmemory.RecallHit{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"query": req.Query, "items": hits})
}

func (s *Server) handleListMemories(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"memories": s.Memory.Memories()})
}

func (s *Server) handleMemoryStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.Memory.Status(r.PathValue("memory_id"))
	if err != nil {
		writeMemoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleListItems(w http.ResponseWriter, r *http.Request) {
	items, err := s.Memory.Items(r.PathValue("memory_id"))
	if err != nil {
		writeMemoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"memory_id": r.PathValue("memory_id"), "items": items})
}

type itemRequest struct {
	Text  string `json:"text"`
	Agent string `json:"agent"`
	Type  string `json:"type,omitempty"`
	// NodeID marks a long-term memory the harness fetched from Node 1 (add only).
	NodeID string `json:"node_id,omitempty"`
}

func (s *Server) handleAddItem(w http.ResponseWriter, r *http.Request) {
	var req itemRequest
	if !decodeBody(w, r, &req) {
		return
	}
	it, err := s.Memory.Add(r.Context(), r.PathValue("memory_id"), workingmemory.AddRequest{
		Text: req.Text, Agent: req.Agent, Type: req.Type, NodeID: req.NodeID,
	})
	if err != nil {
		writeMemoryError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, it)
}

func (s *Server) handleCorrectItem(w http.ResponseWriter, r *http.Request) {
	var req itemRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Type != "" || req.NodeID != "" {
		writeError(w, http.StatusBadRequest, "a correction keeps the original's type and has no node_id")
		return
	}
	it, err := s.Memory.Correct(r.Context(), r.PathValue("memory_id"), r.PathValue("item_id"), req.Text, req.Agent)
	if err != nil {
		writeMemoryError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, it)
}

func (s *Server) handleForgetItem(w http.ResponseWriter, r *http.Request) {
	if err := s.Memory.Forget(r.PathValue("memory_id"), r.PathValue("item_id")); err != nil {
		writeMemoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "forgotten", "item_id": r.PathValue("item_id")})
}

func (s *Server) handleReinforceItem(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Amount float64 `json:"amount,omitempty"`
	}
	if r.ContentLength != 0 && !decodeBody(w, r, &req) {
		return
	}
	if req.Amount < 0 {
		writeError(w, http.StatusBadRequest, "amount must not be negative")
		return
	}
	it, err := s.Memory.Reinforce(r.PathValue("memory_id"), r.PathValue("item_id"), req.Amount)
	if err != nil {
		writeMemoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, it)
}

func (s *Server) handleCommit(w http.ResponseWriter, r *http.Request) {
	sum, err := s.Memory.Commit(r.Context(), r.PathValue("memory_id"), "explicit")
	if err != nil {
		writeMemoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

func (s *Server) handleEndSession(w http.ResponseWriter, r *http.Request) {
	sums, err := s.Memory.EndSession(r.Context(), r.PathValue("session"))
	resp := map[string]any{"session": r.PathValue("session"), "committed": sums}
	if err != nil {
		resp["error"] = err.Error()
		writeJSON(w, http.StatusBadGateway, resp)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
