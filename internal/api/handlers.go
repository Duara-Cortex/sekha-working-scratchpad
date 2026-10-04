package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/budgeter"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/inference"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/model"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/version"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/workingmemory"
)

const (
	defaultDeliberateTimeout = 60 * time.Second
	defaultHealthTimeout     = 2 * time.Second
)

// Server encapsulates the working memory HTTP service and dependencies.
type Server struct {
	// Memory is the working memory temp store; nil disables the /memories endpoints.
	Memory    *workingmemory.Store
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
func NewServer(memory *workingmemory.Store, bud *budgeter.Budgeter, engine inference.Engine) *Server {
	if bud == nil {
		bud = budgeter.New(budgeter.DefaultConfig())
	}
	s := &Server{
		Memory:    memory,
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
	s.mux.HandleFunc("GET /api/v1/working/stats", s.handleStats)
	s.registerMemoryRoutes()
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

func (s *Server) handleStats(w http.ResponseWriter, _ *http.Request) {
	resp := map[string]any{
		"version":        version.Version,
		"uptime_seconds": int64(time.Since(s.startTime).Seconds()),
	}
	if s.Memory != nil {
		resp["working_memory"] = s.Memory.Stats()
	}
	writeJSON(w, http.StatusOK, resp)
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
		"version":         version.Version,
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
