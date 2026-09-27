package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_SuccessWithNodePort(t *testing.T) {
	content := `NODE_PORT=9000
NODE_NAME=node-test
INFERENCE_URL=http://10.0.0.1:8000
INFERENCE_MODEL=deepseek-r1
INFERENCE_TIMEOUT_SEC=45
CONTEXT_LIMIT=4096
OUTPUT_RESERVE=512
SYSTEM_PROMPT=Custom system prompt
`
	tmpDir := t.TempDir()
	envPath := filepath.Join(tmpDir, ".env")
	if err := os.WriteFile(envPath, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write test env file: %v", err)
	}

	cfg, err := Load(envPath)
	if err != nil {
		t.Fatalf("expected successful load, got error: %v", err)
	}

	if cfg.Port != 9000 {
		t.Errorf("expected port 9000, got %d", cfg.Port)
	}
	if cfg.NodeName != "node-test" {
		t.Errorf("expected node-test, got %s", cfg.NodeName)
	}
	if cfg.InferenceURL != "http://10.0.0.1:8000" {
		t.Errorf("expected inference URL http://10.0.0.1:8000, got %s", cfg.InferenceURL)
	}
	if cfg.InferenceModel != "deepseek-r1" {
		t.Errorf("expected model deepseek-r1, got %s", cfg.InferenceModel)
	}
	if cfg.InferenceTimeoutSec != 45 {
		t.Errorf("expected timeout 45, got %d", cfg.InferenceTimeoutSec)
	}
	if cfg.ContextLimit != 4096 {
		t.Errorf("expected context limit 4096, got %d", cfg.ContextLimit)
	}
	if cfg.OutputReserve != 512 {
		t.Errorf("expected output reserve 512, got %d", cfg.OutputReserve)
	}
	if cfg.SystemPrompt != "Custom system prompt" {
		t.Errorf("expected custom system prompt, got %s", cfg.SystemPrompt)
	}
}

func TestLoad_SuccessWithPortAlias(t *testing.T) {
	content := `PORT=9001
NODE_NAME=node-alias
INFERENCE_URL=http://10.0.0.1:8000
INFERENCE_MODEL=qwen
INFERENCE_TIMEOUT_SEC=30
CONTEXT_LIMIT=2048
OUTPUT_RESERVE=256
`
	tmpDir := t.TempDir()
	envPath := filepath.Join(tmpDir, ".env")
	if err := os.WriteFile(envPath, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write test env file: %v", err)
	}

	cfg, err := Load(envPath)
	if err != nil {
		t.Fatalf("expected successful load with PORT alias, got error: %v", err)
	}

	if cfg.Port != 9001 {
		t.Errorf("expected port 9001, got %d", cfg.Port)
	}
}

func TestLoad_MissingRequiredKeys(t *testing.T) {
	tmpDir := t.TempDir()
	envPath := filepath.Join(tmpDir, ".env")
	if err := os.WriteFile(envPath, []byte("NODE_PORT=8080\n"), 0600); err != nil {
		t.Fatalf("failed to write test env file: %v", err)
	}

	_, err := Load(envPath)
	if err == nil {
		t.Fatalf("expected error due to missing required keys, got nil")
	}
}

func TestLoad_InvalidPort(t *testing.T) {
	content := `NODE_PORT=not-a-number
NODE_NAME=node-test
INFERENCE_URL=http://localhost:8080
INFERENCE_MODEL=test-model
INFERENCE_TIMEOUT_SEC=30
CONTEXT_LIMIT=2048
OUTPUT_RESERVE=256
`
	tmpDir := t.TempDir()
	envPath := filepath.Join(tmpDir, ".env")
	if err := os.WriteFile(envPath, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write test env file: %v", err)
	}

	_, err := Load(envPath)
	if err == nil {
		t.Fatalf("expected error due to invalid port, got nil")
	}
}

const requiredKeys = `NODE_PORT=9000
NODE_NAME=node-test
INFERENCE_URL=http://localhost:8080
INFERENCE_MODEL=test-model
INFERENCE_TIMEOUT_SEC=30
CONTEXT_LIMIT=4096
OUTPUT_RESERVE=512
`

func writeEnv(t *testing.T, content string) string {
	t.Helper()
	envPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envPath, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write test env file: %v", err)
	}
	return envPath
}

func TestLoad_BudgetDefaults(t *testing.T) {
	cfg, err := Load(writeEnv(t, requiredKeys))
	if err != nil {
		t.Fatalf("expected successful load without budget keys, got error: %v", err)
	}
	if cfg.GoalBudget != 150 || cfg.SensoryBudget != 0 || cfg.LongTermBudget != 350 ||
		cfg.ObservationBudget != 572 || cfg.SafetyMargin != 32 {
		t.Fatalf("unexpected budget defaults: %+v", cfg)
	}
}

func TestLoad_BudgetOverrides(t *testing.T) {
	content := requiredKeys + `GOAL_BUDGET=100
SENSORY_BUDGET=1500
LONG_TERM_BUDGET=500
OBSERVATION_BUDGET=200
PROMPT_SAFETY_MARGIN=64
`
	envPath := writeEnv(t, content)
	cfg, err := Load(envPath)
	if err != nil {
		t.Fatalf("expected successful load, got error: %v", err)
	}
	if cfg.GoalBudget != 100 || cfg.SensoryBudget != 1500 || cfg.LongTermBudget != 500 ||
		cfg.ObservationBudget != 200 || cfg.SafetyMargin != 64 {
		t.Fatalf("budget overrides not applied: %+v", cfg)
	}

	// Process environment wins over the file, as for the required keys.
	t.Setenv("SENSORY_BUDGET", "900")
	cfg, err = Load(envPath)
	if err != nil {
		t.Fatalf("expected successful load, got error: %v", err)
	}
	if cfg.SensoryBudget != 900 {
		t.Fatalf("expected SENSORY_BUDGET from environment 900, got %d", cfg.SensoryBudget)
	}
}

func TestLoad_InvalidBudget(t *testing.T) {
	for _, bad := range []string{"LONG_TERM_BUDGET=-1\n", "GOAL_BUDGET=lots\n"} {
		if _, err := Load(writeEnv(t, requiredKeys+bad)); err == nil {
			t.Fatalf("expected error for %q, got nil", strings.TrimSpace(bad))
		}
	}
}
