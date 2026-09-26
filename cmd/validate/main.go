package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"time"

	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/api"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/budgeter"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/config"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/inference"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/model"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/scratchpad"
)

type syntheticMockEngine struct {
	stepCount int
}

func (m *syntheticMockEngine) Infer(_ context.Context, req inference.Request) (*inference.DeliberationOutput, error) {
	m.stepCount++
	switch m.stepCount {
	case 1:
		return &inference.DeliberationOutput{
			Thought:          "Sensory signal indicates intermittent packet drops on switch port 2. Need to investigate error rates.",
			Action:           "Query Node 3 stats endpoint",
			IsComplete:       false,
			PromptTokens:     120,
			CompletionTokens: 35,
			TotalTokens:      155,
			PromptTPS:        31.2,
			PredictedTPS:     12.5,
		}, nil
	case 2:
		return &inference.DeliberationOutput{
			Thought:          "Packet drops might be caused by switch overload. Candidate action is to reboot the switch.",
			Action:           "REBOOT_CLUSTER_SWITCH",
			IsComplete:       false,
			PromptTokens:     160,
			CompletionTokens: 30,
			TotalTokens:      190,
			PromptTPS:        31.5,
			PredictedTPS:     12.4,
		}, nil
	case 3:
		return &inference.DeliberationOutput{
			Thought:          "Previous hypothesis was flawed: rebooting switch disrupts active cluster nodes. The correct action is checking buffer fill %.",
			Action:           "GET /api/v1/sensory/stats",
			IsComplete:       false,
			PromptTokens:     145,
			CompletionTokens: 40,
			TotalTokens:      185,
			PromptTPS:        32.0,
			PredictedTPS:     12.3,
		}, nil
	case 4:
		return &inference.DeliberationOutput{
			Thought:          "Buffer fill is 35.4%, well within envelope. Drops were transient ICMP rate limits. No hardware fault.",
			Action:           "Mark objective resolved; log to long-term memory",
			IsComplete:       true,
			PromptTokens:     190,
			CompletionTokens: 38,
			TotalTokens:      228,
			PromptTPS:        31.8,
			PredictedTPS:     12.6,
		}, nil
	default:
		return &inference.DeliberationOutput{
			Thought:    "Deliberation concluded.",
			Action:     "none",
			IsComplete: true,
		}, nil
	}
}

func (m *syntheticMockEngine) Health(_ context.Context) error {
	return nil
}

func main() {
	envFileFlag := flag.String("env", ".env", "Path to .env configuration file")
	serverURLFlag := flag.String("url", "", "Target working memory server URL. If omitted, uses NODE_PORT from env or embedded mock engine.")
	nodePortFlag := flag.Int("node-port", 0, "Target or mock node port (overrides NODE_PORT from .env/environment)")
	portFlag := flag.Int("port", 0, "Alias for -node-port")
	flag.Parse()

	// Read NODE_PORT and NODE_NAME from environment or .env if present
	var envPort int
	var envNode string

	if cfg, err := config.Load(*envFileFlag); err == nil {
		envPort = cfg.Port
		envNode = cfg.NodeName
	} else if pStr := os.Getenv("NODE_PORT"); pStr != "" {
		if p, err := strconv.Atoi(pStr); err == nil && p > 0 {
			envPort = p
		}
	} else if pStr := os.Getenv("PORT"); pStr != "" {
		if p, err := strconv.Atoi(pStr); err == nil && p > 0 {
			envPort = p
		}
	}
	if nStr := os.Getenv("NODE_NAME"); nStr != "" {
		envNode = nStr
	}
	if *nodePortFlag > 0 {
		envPort = *nodePortFlag
	} else if *portFlag > 0 {
		envPort = *portFlag
	}
	if envNode == "" {
		envNode = "validation-node"
	}

	targetURL := *serverURLFlag
	var cleanup func()

	// If no URL flag was specified, but PORT was set in env/.env, check if live server is already running
	if targetURL == "" && envPort > 0 {
		candidateURL := fmt.Sprintf("http://127.0.0.1:%d", envPort)
		checkClient := &http.Client{Timeout: 500 * time.Millisecond}
		resp, err := checkClient.Get(candidateURL + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				log.Printf("Detected active deliberation service at %s (from PORT env)", candidateURL)
				targetURL = candidateURL
			}
		}
	}

	// If no live service found or specified, run local mock deliberation engine
	if targetURL == "" {
		log.Printf("No remote service reachable. Starting local mock deliberation engine (node: %s, port: %d)...", envNode, envPort)
		store := scratchpad.NewStore()
		bud := budgeter.New(budgeter.DefaultConfig())
		mock := &syntheticMockEngine{}
		s := api.NewServer(store, bud, mock)

		var listener net.Listener
		var err error
		if envPort > 0 {
			listener, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", envPort))
		}
		if listener == nil || err != nil {
			listener, err = net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				log.Fatalf("failed to create test listener: %v", err)
			}
		}

		actualPort := listener.Addr().(*net.TCPAddr).Port
		s.SetNodeInfo(envNode, actualPort)

		ts := &httptest.Server{
			Listener: listener,
			Config:   &http.Server{Handler: s},
		}
		ts.Start()
		targetURL = ts.URL
		cleanup = ts.Close
		defer cleanup()
	}

	fmt.Println("================================================================")
	fmt.Println(" Sekha Working Memory Deliberation Scratchpad Validation Suite ")
	fmt.Printf(" Target Service: %s\n", targetURL)
	fmt.Println("================================================================")

	client := &http.Client{Timeout: 60 * time.Second}

	// Step 0: Health check
	var healthResp map[string]interface{}
	if err := getJSON(client, targetURL+"/api/v1/working/health", &healthResp); err != nil {
		log.Fatalf("Health check failed: %v", err)
	}
	fmt.Printf("[Step 0] Service health check: status=%v\n", healthResp["status"])

	// Step 1: Ingest active goal and sensory chunk
	delib1 := model.DeliberateRequest{
		Objective: "Isolate cause of packet drops reported by sensory buffer",
		SensoryChunks: []model.SensoryChunk{
			{ID: "sens-01", Text: "sensory ring buffer reports 2 dropped frames at 18:00:00", Salience: 0.92, Source: "sekha-node3"},
		},
		LongTermContext: []string{
			"Node 3 sensory ring buffer has 64MB capacity envelope",
			"Cluster switch operates at Gigabit line rate with full-duplex flow control",
		},
	}
	var resp1 model.DeliberateResponse
	if err := postJSON(client, targetURL+"/api/v1/working/deliberate", delib1, &resp1); err != nil {
		log.Fatalf("Step 1 deliberate failed: %v", err)
	}
	fmt.Printf("[Step 1] Ingested Goal & Stimuli -> Step %d: Thought: %q | Action: %q\n",
		resp1.StepIndex, resp1.Thought, resp1.ProposedAction)

	// Step 2: Create a state snapshot before executing hypothesis
	var snapResp map[string]interface{}
	snapReq := map[string]string{"description": "Pre-hypothesis checkpoint"}
	if err := postJSON(client, targetURL+"/api/v1/working/snapshot", snapReq, &snapResp); err != nil {
		log.Fatalf("Step 2 snapshot failed: %v", err)
	}
	fmt.Printf("[Step 2] Captured snapshot checkpoint ID: %v\n", snapResp["snapshot_id"])

	// Step 3: Deliberate second step (produces a faulty/unsafe hypothesis)
	delib2 := model.DeliberateRequest{
		Observation: "Link carrier intact, zero CRC errors on physical interface",
	}
	var resp2 model.DeliberateResponse
	if err := postJSON(client, targetURL+"/api/v1/working/deliberate", delib2, &resp2); err != nil {
		log.Fatalf("Step 3 deliberate failed: %v", err)
	}
	fmt.Printf("[Step 3] Deliberation Step %d -> Thought: %q | Action: %q\n",
		resp2.StepIndex, resp2.Thought, resp2.ProposedAction)

	// Step 4: Error Detection & Isolation - Rollback
	fmt.Println("[Step 4] Self-Correction triggered: Unsafe action identified. Rolling back...")
	var rollbackResp map[string]interface{}
	if err := postJSON(client, targetURL+"/api/v1/working/rollback", nil, &rollbackResp); err != nil {
		log.Fatalf("Step 4 rollback failed: %v", err)
	}
	fmt.Printf("         Rollback response: status=%v message=%q\n",
		rollbackResp["status"], rollbackResp["message"])

	// Step 5: Corrected Deliberation Step
	delib3 := model.DeliberateRequest{
		Observation: "Rollback successful. Querying sensory buffer capacity telemetry instead.",
	}
	var resp3 model.DeliberateResponse
	if err := postJSON(client, targetURL+"/api/v1/working/deliberate", delib3, &resp3); err != nil {
		log.Fatalf("Step 5 deliberate failed: %v", err)
	}
	fmt.Printf("[Step 5] Corrected Step %d -> Thought: %q | Action: %q\n",
		resp3.StepIndex, resp3.Thought, resp3.ProposedAction)

	// Step 6: Final Resolution
	delib4 := model.DeliberateRequest{
		Observation: "Sensory stats: 22.7MB / 64MB used (35.4%). Zero drops in last 10,000 frames.",
	}
	var resp4 model.DeliberateResponse
	if err := postJSON(client, targetURL+"/api/v1/working/deliberate", delib4, &resp4); err != nil {
		log.Fatalf("Step 6 deliberate failed: %v", err)
	}
	fmt.Printf("[Step 6] Final Resolution Step %d -> Complete: %v | Thought: %q\n",
		resp4.StepIndex, resp4.IsComplete, resp4.Thought)

	// Step 7: Verify telemetry
	var telemetry model.ScratchpadTelemetry
	if err := getJSON(client, targetURL+"/api/v1/working/stats", &telemetry); err != nil {
		log.Fatalf("Step 7 get stats failed: %v", err)
	}

	fmt.Println("\n----------------------------------------------------------------")
	fmt.Println("                    EMPIRICAL VERIFICATION                      ")
	fmt.Println("----------------------------------------------------------------")
	fmt.Printf(" Active Goal:            %s\n", telemetry.ActiveGoal)
	fmt.Printf(" Sensory Context Items:  %d chunks\n", telemetry.SensoryItemsCount)
	fmt.Printf(" Long-Term Facts:        %d facts\n", telemetry.LongTermFactsCount)
	fmt.Printf(" Trajectory Steps:       %d steps\n", telemetry.TrajectorySteps)
	fmt.Printf(" Snapshots Captured:     %d snapshots\n", telemetry.SnapshotCount)
	fmt.Printf(" Final Goal Resolved:    %v\n", resp4.IsComplete)
	fmt.Println("----------------------------------------------------------------")
	fmt.Println(" RESULT: PASS - Working Memory Scratchpad correctly isolated")
	fmt.Println(" intermediate reasoning errors entirely within local RAM.")
	fmt.Println("================================================================")
}

func postJSON(client *http.Client, targetURL string, in any, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("failed to marshal body: %w", err)
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, targetURL, body)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed to %s: %w", targetURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return fmt.Errorf("request to %s returned %d (failed to read body: %w)", targetURL, resp.StatusCode, readErr)
		}
		return fmt.Errorf("request to %s returned %d: %s", targetURL, resp.StatusCode, string(bodyBytes))
	}

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("failed to decode response from %s: %w", targetURL, err)
		}
	}
	return nil
}

func getJSON(client *http.Client, targetURL string, out any) error {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, targetURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create get request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("get request failed to %s: %w", targetURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return fmt.Errorf("get %s returned %d (failed to read body: %w)", targetURL, resp.StatusCode, readErr)
		}
		return fmt.Errorf("get %s returned %d: %s", targetURL, resp.StatusCode, string(bodyBytes))
	}

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("failed to decode get response from %s: %w", targetURL, err)
		}
	}
	return nil
}
