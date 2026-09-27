package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/budgeter"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/inference"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/model"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/scratchpad"
)

const (
	defaultDeliberateTimeout = 60 * time.Second
	defaultHealthTimeout     = 2 * time.Second
)

// Server encapsulates the working memory HTTP service and dependencies.
type Server struct {
	Store     *scratchpad.Store
	Budgeter  *budgeter.Budgeter
	Engine    inference.Engine
	mux       *http.ServeMux
	startTime time.Time
	nodeName  string
	port      int
	// deliberateTimeout bounds one inference call; set from INFERENCE_TIMEOUT_SEC.
	deliberateTimeout time.Duration
}

// NewServer initializes the deliberation scratchpad server.
func NewServer(store *scratchpad.Store, bud *budgeter.Budgeter, engine inference.Engine) *Server {
	if bud == nil {
		bud = budgeter.New(budgeter.DefaultConfig())
	}
	s := &Server{
		Store:     store,
		Budgeter:  bud,
		Engine:    engine,
		mux:       http.NewServeMux(),
		startTime: time.Now(),

		deliberateTimeout: defaultDeliberateTimeout,
	}
	s.registerRoutes()
	return s
}

// SetNodeInfo configures the runtime node identity and service port.
func (s *Server) SetNodeInfo(nodeName string, port int) {
	s.nodeName = nodeName
	s.port = port
}

// SetDeliberateTimeout sets how long one deliberation may wait for the inference engine.
func (s *Server) SetDeliberateTimeout(d time.Duration) {
	if d > 0 {
		s.deliberateTimeout = d
	}
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
		writeError(w, http.StatusBadRequest, "invalid json request payload")
		return
	}

	prompt := s.Budgeter.BuildPrompt(req)

	ctx, cancel := context.WithTimeout(r.Context(), s.deliberateTimeout)
	defer cancel()

	output, err := s.Engine.Infer(ctx, inference.Request{
		Model:        req.Model,
		SystemPrompt: prompt.SystemPrompt,
		UserPrompt:   prompt.UserPrompt,
		MaxTokens:    req.MaxTokens,
		Temperature:  req.Temperature,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	usage := prompt.Usage
	usage.ActualPromptTokens = output.PromptTokens
	if usage.SensoryDropped > 0 || usage.FactsKept < usage.FactsReceived {
		log.Printf("deliberate: trimmed context (prepacked=%t, caller budget=%d): sensory kept %d/%d (truncated %d), facts kept %d/%d, est %d, actual %d, window %d",
			req.Prepacked, req.PromptBudgetTokens, usage.SensoryKept, usage.SensoryReceived, usage.SensoryTruncated,
			usage.FactsKept, usage.FactsReceived, usage.EstimatedPromptTokens, usage.ActualPromptTokens, usage.PromptWindowTokens)
	}

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
		ContextUsage:     usage,
		Timestamp:        time.Now(),
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	snapshotID := -1
	if snapshotIDStr := r.URL.Query().Get("snapshot_id"); snapshotIDStr != "" {
		if id, err := strconv.Atoi(snapshotIDStr); err == nil {
			snapshotID = id
		}
	}

	if err := s.Store.Rollback(snapshotID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "rolled_back",
		"message": "working memory restored to snapshot",
		"state":   s.Store.GetState(),
	})
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Description == "" {
		body.Description = "Manual snapshot"
	}

	snapID := s.Store.CreateSnapshot(body.Description)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":      "ok",
		"snapshot_id": snapID,
		"description": body.Description,
	})
}

func (s *Server) handleStats(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.Store.GetTelemetry())
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), defaultHealthTimeout)
	defer cancel()

	llamaStatus := "reachable"
	status := "healthy"
	if err := s.Engine.Health(ctx); err != nil {
		llamaStatus = "unreachable: " + err.Error()
		status = "degraded"
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":          status,
		"service":         "sekha-working-scratchpad",
		"node":            s.nodeName,
		"port":            s.port,
		"uptime_seconds":  int64(time.Since(s.startTime).Seconds()),
		"llama_inference": llamaStatus,
		"timestamp":       time.Now(),
	})
}

func writeJSON(w http.ResponseWriter, statusCode int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		http.Error(w, `{"error":"failed to encode response"}`, http.StatusInternalServerError)
	}
}

func writeError(w http.ResponseWriter, statusCode int, message string) {
	writeJSON(w, statusCode, map[string]string{
		"status": "error",
		"error":  message,
	})
}
