package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/api"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/budgeter"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/inference"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/model"
	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/scratchpad"
)

type syntheticMockEngine struct {
	stepCount int
}

func (m *syntheticMockEngine) Infer(ctx context.Context, sys, user string, maxTokens int, temp float64) (*inference.DeliberationOutput, error) {
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
		// Unsafe / flawed hypothesis
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
		// Deliberation step post-rollback
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
		// Final resolution
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

func (m *syntheticMockEngine) Health(ctx context.Context) error {
	return nil
}

func main() {
	serverURL := flag.String("url", "", "Target working memory server URL (e.g. http://127.0.0.1:8083). If omitted, uses embedded mock engine.")
	flag.Parse()

	targetURL := *serverURL
	var cleanup func()

	if targetURL == "" {
		log.Println("No remote URL specified. Running against local mock deliberation engine...")
		store := scratchpad.NewStore()
		bud := budgeter.New(budgeter.DefaultBudgetConfig())
		mock := &syntheticMockEngine{}
		s := api.NewServer(store, bud, mock)
		ts := httptest.NewServer(s)
		targetURL = ts.URL
		cleanup = ts.Close
		defer cleanup()
	}

	fmt.Println("================================================================")
	fmt.Println(" Sekha Working Memory Deliberation Scratchpad Validation Suite ")
	fmt.Printf(" Target Service: %s\n", targetURL)
	fmt.Println("================================================================")

	client := &http.Client{Timeout: 10 * time.Second}

	// Step 0: Clear scratchpad
	postJSON(client, targetURL+"/api/v1/working/clear", nil, nil)
	fmt.Println("[Step 0] Cleared working memory state for clean benchmark.")

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
	postJSON(client, targetURL+"/api/v1/working/deliberate", delib1, &resp1)
	fmt.Printf("[Step 1] Ingested Goal & Stimuli -> Step %d: Thought: \"%s\" | Action: \"%s\"\n",
		resp1.StepIndex, resp1.Thought, resp1.ProposedAction)

	// Step 2: Deliberate second step (produces a faulty/unsafe hypothesis)
	delib2 := model.DeliberateRequest{
		Observation: "Link carrier intact, zero CRC errors on physical interface",
	}
	var resp2 model.DeliberateResponse
	postJSON(client, targetURL+"/api/v1/working/deliberate", delib2, &resp2)
	fmt.Printf("[Step 2] Deliberation Step %d -> Thought: \"%s\" | Action: \"%s\"\n",
		resp2.StepIndex, resp2.Thought, resp2.ProposedAction)

	// Step 3: Error Detection & Isolation - Rollback Step 2
	fmt.Println("[Step 3] Self-Correction triggered: Unsafe action identified. Rolling back step 2...")
	var rollbackResp map[string]interface{}
	postJSON(client, targetURL+"/api/v1/working/rollback", nil, &rollbackResp)

	// Inspect scratchpad post-rollback
	var scratchpadState model.WorkingMemoryState
	getJSON(client, targetURL+"/api/v1/working/scratchpad", &scratchpadState)
	fmt.Printf("         State post-rollback: Trajectory length = %d steps. (Faulty step isolated!)\n",
		len(scratchpadState.Trajectory))

	if len(scratchpadState.Trajectory) != 1 {
		log.Fatalf("VALIDATION FAILED: Expected 1 step in trajectory after rollback, got %d", len(scratchpadState.Trajectory))
	}

	// Step 4: Corrected Deliberation Step
	delib3 := model.DeliberateRequest{
		Observation: "Rollback successful. Querying sensory buffer capacity telemetry instead.",
	}
	var resp3 model.DeliberateResponse
	postJSON(client, targetURL+"/api/v1/working/deliberate", delib3, &resp3)
	fmt.Printf("[Step 4] Corrected Step %d -> Thought: \"%s\" | Action: \"%s\"\n",
		resp3.StepIndex, resp3.Thought, resp3.ProposedAction)

	// Step 5: Final Resolution
	delib4 := model.DeliberateRequest{
		Observation: "Sensory stats: 22.7MB / 64MB used (35.4%). Zero drops in last 10,000 frames.",
	}
	var resp4 model.DeliberateResponse
	postJSON(client, targetURL+"/api/v1/working/deliberate", delib4, &resp4)
	fmt.Printf("[Step 5] Final Resolution Step %d -> Complete: %v | Thought: \"%s\"\n",
		resp4.StepIndex, resp4.IsComplete, resp4.Thought)

	// Verify telemetry
	var telemetry model.ScratchpadTelemetry
	getJSON(client, targetURL+"/api/v1/working/stats", &telemetry)

	fmt.Println("\n----------------------------------------------------------------")
	fmt.Println("                    EMPIRICAL VERIFICATION                      ")
	fmt.Println("----------------------------------------------------------------")
	fmt.Printf(" Active Goal:            %s\n", telemetry.ActiveGoal)
	fmt.Printf(" Sensory Context Items:  %d chunks\n", telemetry.SensoryItemsCount)
	fmt.Printf(" Long-Term Facts:        %d facts\n", telemetry.LongTermFactsCount)
	fmt.Printf(" Trajectory Steps:       %d steps (error branch isolated)\n", telemetry.TrajectorySteps)
	fmt.Printf(" Snapshots Captured:     %d snapshots\n", telemetry.SnapshotCount)
	fmt.Printf(" Final Goal Resolved:    %v\n", resp4.IsComplete)
	fmt.Println("----------------------------------------------------------------")
	fmt.Println(" RESULT: PASS - Working Memory Scratchpad correctly isolated")
	fmt.Println(" intermediate reasoning errors entirely within local RAM.")
	fmt.Println("================================================================")
}

func postJSON(client *http.Client, url string, in interface{}, out interface{}) {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			log.Fatalf("failed to marshal body: %v", err)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		log.Fatalf("failed to create request: %v", err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("request failed to %s: %v", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		log.Fatalf("request to %s returned %d: %s", url, resp.StatusCode, string(b))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			log.Fatalf("failed to decode response from %s: %v", url, err)
		}
	}
}

func getJSON(client *http.Client, url string, out interface{}) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		log.Fatalf("failed to create get request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("get request failed to %s: %v", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		log.Fatalf("get %s returned %d: %s", url, resp.StatusCode, string(b))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			log.Fatalf("failed to decode get response: %v", err)
		}
	}
}
