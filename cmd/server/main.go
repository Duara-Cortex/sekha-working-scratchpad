package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/api"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/budgeter"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/config"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/inference"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/version"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/workingmemory"
)

func main() {
	envFileFlag := flag.String("env", "", "Path to .env configuration file (defaults to .env or /etc/default/sekha)")
	nodePortFlag := flag.Int("node-port", 0, "HTTP node port (overrides .env NODE_PORT)")
	portFlag := flag.Int("port", 0, "Alias for -node-port")
	nodeFlag := flag.String("node", "", "Node name (overrides .env NODE_NAME)")
	urlFlag := flag.String("inference-url", "", "Inference server URL (overrides .env INFERENCE_URL)")
	llamaURLFlag := flag.String("llama-url", "", "Alias for -inference-url")
	modelFlag := flag.String("model", "", "Inference model name (overrides .env INFERENCE_MODEL)")
	timeoutFlag := flag.Int("timeout", 0, "Inference timeout in seconds (overrides .env INFERENCE_TIMEOUT_SEC)")
	contextLimitFlag := flag.Int("context-limit", 0, "Context token limit (overrides .env CONTEXT_LIMIT)")
	outputReserveFlag := flag.Int("output-reserve", 0, "Output token reserve (overrides .env OUTPUT_RESERVE)")
	systemPromptFlag := flag.String("system-prompt", "", "Custom system prompt (overrides .env SYSTEM_PROMPT)")
	flag.Parse()

	// Load configuration strictly from .env, /etc/default/sekha, or environment variables.
	// Returns an error if any required configuration key is missing.
	var cfg *config.Config
	var err error
	if *envFileFlag != "" {
		cfg, err = config.Load(*envFileFlag)
	} else {
		cfg, err = config.Load()
	}
	if err != nil {
		log.Fatalf("Configuration error: %v\nPlease provide required settings in .env, /etc/default/sekha, or environment variables. See .env.example for details.", err)
	}

	// Apply CLI flag overrides if explicitly specified
	if *nodePortFlag > 0 {
		cfg.Port = *nodePortFlag
	} else if *portFlag > 0 {
		cfg.Port = *portFlag
	}
	if *nodeFlag != "" {
		cfg.NodeName = *nodeFlag
	}
	if *urlFlag != "" {
		cfg.InferenceURL = *urlFlag
	} else if *llamaURLFlag != "" {
		cfg.InferenceURL = *llamaURLFlag
	}
	if *modelFlag != "" {
		cfg.InferenceModel = *modelFlag
	}
	if *timeoutFlag > 0 {
		cfg.InferenceTimeoutSec = *timeoutFlag
	}
	if *contextLimitFlag > 0 {
		cfg.ContextLimit = *contextLimitFlag
	}
	if *outputReserveFlag > 0 {
		cfg.OutputReserve = *outputReserveFlag
	}
	if *systemPromptFlag != "" {
		cfg.SystemPrompt = *systemPromptFlag
	}

	log.Printf("Starting Sekha Working Memory Deliberation Scratchpad Daemon v%s...", version.Version)
	log.Printf("Node Name:           %s", cfg.NodeName)
	log.Printf("Deliberation Port:   %d", cfg.Port)
	log.Printf("Inference Server:    %s", cfg.InferenceURL)
	log.Printf("Inference Model:     %s", cfg.InferenceModel)
	log.Printf("Inference Timeout:   %ds", cfg.InferenceTimeoutSec)
	log.Printf("Context Limit:       %d tokens", cfg.ContextLimit)
	log.Printf("Output Reserve:      %d tokens", cfg.OutputReserve)
	log.Printf("Prompt Window:       %d tokens", cfg.ContextLimit-cfg.OutputReserve)
	log.Printf("Safety Margin:       %d tokens", cfg.SafetyMargin)
	log.Printf("Goal Budget:         %d tokens (cap)", cfg.GoalBudget)
	log.Printf("Observation Budget:  %d tokens (cap)", cfg.ObservationBudget)
	log.Printf("Long-Term Budget:    %d tokens (cap)", cfg.LongTermBudget)
	if cfg.SensoryBudget > 0 {
		log.Printf("Sensory Budget:      %d tokens (cap)", cfg.SensoryBudget)
	} else {
		log.Printf("Sensory Budget:      rest of window (no cap)")
	}

	log.Printf("WM Call Budget:      %d tokens", cfg.CallBudget)
	log.Printf("WM Wait Limit:       %ds (0 = none)", cfg.WaitLimitSec)
	log.Printf("WM Idle Timeout:     %ds", cfg.IdleTimeoutSec)
	log.Printf("WM Workers:          %d", cfg.Workers)
	log.Printf("WM Related Items:    %d per call (same memory only)", cfg.RelatedItems)
	log.Printf("WM Max Items:        %d (Node 3 pushes beyond this get 503)", cfg.MaxItems)

	// With no commit target the store refuses Node 3's pushes (503), so chunks stay on Node 3.
	var committer workingmemory.Committer
	if cfg.CommitJournal != "" {
		committer = &workingmemory.JournalCommitter{Path: cfg.CommitJournal}
		log.Printf("WM Commit Journal:   %s (interim target until Node 1's write endpoint, Task 31)", cfg.CommitJournal)
	} else {
		log.Printf("WM Commit Journal:   none; pushes from Node 3 are refused until WM_COMMIT_JOURNAL is set")
	}
	var scorer workingmemory.Scorer
	if cfg.EmbedURL != "" {
		scorer = &workingmemory.EmbeddingScorer{BaseURL: cfg.EmbedURL, Model: cfg.EmbedModel, HTTP: &http.Client{Timeout: 10 * time.Second}}
		log.Printf("WM Scorer:           %s (%s)", cfg.EmbedURL, cfg.EmbedModel)
	} else {
		log.Printf("WM Scorer:           none; harness items and thoughts are stored unscored")
	}
	store := workingmemory.NewStore(workingmemory.Options{
		WaitLimit:     cfg.WaitLimit(),
		IdleTimeout:   cfg.IdleTimeout(),
		ReinforceStep: cfg.ReinforceStep,
		MaxItems:      cfg.MaxItems,
		RelatedLimit:  cfg.RelatedItems,
	}, committer, scorer)

	bCfg := budgeter.DefaultConfig()
	bCfg.MaxContextTokens = cfg.ContextLimit
	bCfg.OutputReserve = cfg.OutputReserve
	bCfg.SystemPrompt = cfg.SystemPrompt
	bCfg.GoalBudget = cfg.GoalBudget
	bCfg.SensoryBudget = cfg.SensoryBudget
	bCfg.LongTermBudget = cfg.LongTermBudget
	bCfg.TrajectoryBudget = cfg.ObservationBudget
	bCfg.SafetyMargin = cfg.SafetyMargin
	bud := budgeter.New(bCfg)

	client, err := inference.NewClient(cfg.InferenceURL, cfg.InferenceModel, cfg.InferenceTimeout())
	if err != nil {
		log.Fatalf("Failed to initialize inference client: %v", err)
	}

	server := api.NewServer(store, bud, client)
	server.SetNodeInfo(cfg.NodeName, cfg.Port)
	server.SetDeliberateTimeout(cfg.InferenceTimeout())

	processor := &workingmemory.Processor{
		Store:  store,
		Engine: client,
		Prompt: workingmemory.PromptConfig{
			Budget:     cfg.CallBudget,
			TaskBudget: cfg.GoalBudget,
		},
		Workers:       cfg.Workers,
		CallTimeout:   cfg.InferenceTimeout(),
		CommitTimeout: cfg.CommitTimeout(),
		SweepInterval: time.Second,
		MaxTokens:     cfg.OutputReserve,
	}
	wmCtx, stopWM := context.WithCancel(context.Background())
	defer stopWM()
	go processor.Run(wmCtx)

	httpServer := &http.Server{
		Addr:        fmt.Sprintf(":%d", cfg.Port),
		Handler:     server,
		ReadTimeout: 15 * time.Second,
		// An explicit commit may wait for an in-flight model call and then for the commit itself.
		WriteTimeout: cfg.InferenceTimeout() + cfg.CommitTimeout() + 15*time.Second,
		IdleTimeout:  60 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("Working memory deliberation scratchpad listening on http://0.0.0.0:%d", cfg.Port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server failed: %v", err)
		}
	}()

	<-stop
	log.Println("Shutting down scratchpad daemon gracefully...")
	// Working memory is RAM only: memories not yet committed are lost with the process.
	stopWM()
	store.Close()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("Server shutdown error: %v", err)
	}

	log.Println("Working memory scratchpad daemon stopped.")
}
