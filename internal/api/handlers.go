package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/budgeter"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/inference"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/model"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/scratchpad"
)

// Server encapsulates the working memory HTTP service and dependencies.
type Server struct {
	Store     *scratchpad.Store
	Budgeter  *budgeter.ContextBudgeter
	Engine    inference.Engine
	mux       *http.ServeMux
	startTime time.Time
}

// NewServer initializes the deliberation scratchpad server.
func NewServer(store *scratchpad.Store, bud *budgeter.ContextBudgeter, engine inference.Engine) *Server {
	if bud == nil {
		bud = budgeter.New(budgeter.DefaultBudgetConfig())
	}
	s := &Server{
		Store:     store,
		Budgeter:  bud,
		Engine:    engine,
		mux:       http.NewServeMux(),
		startTime: time.Now(),
	}
	s.registerRoutes()
	return s
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("POST /api/v1/working/deliberate", s.handleDeliberate)
	s.mux.HandleFunc("POST /api/v1/working/rollback", s.handleRollback)
	s.mux.HandleFunc("POST /api/v1/working/snapshot", s.handleSnapshot)
	s.mux.HandleFunc("GET /api/v1/working/stats", s.handleStats)
	s.mux.HandleFunc("GET /api/v1/working/health", s.handleHealth)
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleDeliberate(w http.ResponseWriter, r *http.Request) {
	var req model.DeliberateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json request payload"}`, http.StatusBadRequest)
		return
	}

	// 1. Build budget-constrained prompt strictly and solely from incoming request
	prompt := s.Budgeter.BuildPrompt(req)

	// 2. Call inference engine
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	output, err := s.Engine.Infer(ctx, prompt.SystemPrompt, prompt.UserPrompt, req.MaxTokens, req.Temperature)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "error",
			"error":  err.Error(),
		})
		return
	}

	// 3. Return structured deliberation response strictly isolated to this step
	resp := model.DeliberateResponse{
		Status:           "ok",
		StepIndex:        1,
		Thought:          output.Thought,
		ProposedAction:   output.Action,
		IsComplete:       output.IsComplete,
		CandidateActions: []model.CandidateAction{},
		PromptTokens:     output.PromptTokens,
		CompletionTokens: output.CompletionTokens,
		TotalTokens:      output.TotalTokens,
		EvaluationRate:   output.PromptTPS,
		GenerationRate:   output.PredictedTPS,
		ActiveGoal:       req.Objective,
		TrajectoryLength: 1,
		Timestamp:        time.Now(),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	snapshotIDStr := r.URL.Query().Get("snapshot_id")
	snapshotID := -1
	if snapshotIDStr != "" {
		if id, err := strconv.Atoi(snapshotIDStr); err == nil {
			snapshotID = id
		}
	}

	err := s.Store.Rollback(snapshotID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "error",
			"error":  err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "rolled_back",
		"message": "working memory restored to snapshot",
		"state":   s.Store.GetState(),
	})
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Description string `json:"description"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Description == "" {
		body.Description = "Manual snapshot"
	}

	snapID := s.Store.CreateSnapshot(body.Description)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":      "ok",
		"snapshot_id": snapID,
		"description": body.Description,
	})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats := s.Store.GetTelemetry()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(stats)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	llamaStatus := "reachable"
	if err := s.Engine.Health(ctx); err != nil {
		llamaStatus = "unreachable: " + err.Error()
	}

	status := "healthy"
	httpStatus := http.StatusOK
	if strings.HasPrefix(llamaStatus, "unreachable") {
		status = "degraded"
		// Still return 200 for healthz so scratchpad daemon itself stays alive
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":          status,
		"service":         "sekha-working-scratchpad",
		"node":            "sekha-node2",
		"port":            8083,
		"uptime_seconds":  int64(time.Since(s.startTime).Seconds()),
		"llama_inference": llamaStatus,
		"timestamp":       time.Now(),
	})
}
