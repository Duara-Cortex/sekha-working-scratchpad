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
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/scratchpad"
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

	log.Printf("Starting Sekha Working Memory Deliberation Scratchpad Daemon...")
	log.Printf("Node Name:           %s", cfg.NodeName)
	log.Printf("Deliberation Port:   %d", cfg.Port)
	log.Printf("Inference Server:    %s", cfg.InferenceURL)
	log.Printf("Inference Model:     %s", cfg.InferenceModel)
	log.Printf("Inference Timeout:   %ds", cfg.InferenceTimeoutSec)
	log.Printf("Context Limit:       %d tokens", cfg.ContextLimit)
	log.Printf("Output Reserve:      %d tokens", cfg.OutputReserve)

	store := scratchpad.NewStore()

	bCfg := budgeter.DefaultConfig()
	bCfg.MaxContextTokens = cfg.ContextLimit
	bCfg.OutputReserve = cfg.OutputReserve
	bCfg.SystemPrompt = cfg.SystemPrompt
	bCfg.TrajectoryBudget = cfg.ContextLimit - cfg.OutputReserve - bCfg.SystemBudget - bCfg.GoalBudget - bCfg.SensoryBudget - bCfg.LongTermBudget
	bud := budgeter.New(bCfg)

	client, err := inference.NewClient(cfg.InferenceURL, cfg.InferenceModel, cfg.InferenceTimeout())
	if err != nil {
		log.Fatalf("Failed to initialize inference client: %v", err)
	}

	server := api.NewServer(store, bud, client)
	server.SetNodeInfo(cfg.NodeName, cfg.Port)

	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      server,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: cfg.InferenceTimeout() + 15*time.Second,
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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("Server shutdown error: %v", err)
	}

	log.Println("Working memory scratchpad daemon stopped.")
}
