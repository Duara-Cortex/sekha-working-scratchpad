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
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/inference"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/scratchpad"
)

func main() {
	port := flag.Int("port", 8083, "HTTP port for working memory deliberation service")
	llamaURL := flag.String("llama-url", "http://127.0.0.1:8082", "Base URL of local llama.cpp server")
	contextLimit := flag.Int("context-limit", 2048, "Maximum SLM context token budget")
	outputReserve := flag.Int("output-reserve", 256, "Reserved tokens for next step generation")
	timeoutSec := flag.Int("timeout", 60, "HTTP timeout for inference calls in seconds")
	flag.Parse()

	log.Printf("Starting Sekha Working Memory Deliberation Scratchpad Daemon...")
	log.Printf("Deliberation Port:   %d", *port)
	log.Printf("Llama Server URL:    %s", *llamaURL)
	log.Printf("Context Limit:       %d tokens", *contextLimit)
	log.Printf("Output Reserve:      %d tokens", *outputReserve)

	store := scratchpad.NewStore()

	bCfg := budgeter.DefaultBudgetConfig()
	bCfg.MaxContextTokens = *contextLimit
	bCfg.OutputReserve = *outputReserve
	bCfg.TrajectoryBudget = *contextLimit - *outputReserve - bCfg.SystemBudget - bCfg.GoalBudget - bCfg.SensoryBudget - bCfg.LongTermBudget
	bud := budgeter.New(bCfg)

	client := inference.NewClient(*llamaURL, time.Duration(*timeoutSec)*time.Second)
	server := api.NewServer(store, bud, client)

	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", *port),
		Handler:      server,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: time.Duration(*timeoutSec+15) * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("Working memory deliberation scratchpad listening on http://0.0.0.0:%d", *port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server failed: %v", err)
		}
	}()

	<-stop
	log.Println("Shutting down scratchpad daemon gracefully...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		log.Fatalf("Server shutdown error: %v", err)
	}

	log.Println("Working memory scratchpad daemon stopped.")
}
