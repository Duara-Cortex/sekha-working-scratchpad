package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/budgeter"
)

// Config represents application runtime settings loaded strictly from .env and environment variables.
// There are no hardcoded default fallbacks: all required values must be explicitly supplied.
type Config struct {
	Port                int
	NodeName            string
	InferenceURL        string
	InferenceModel      string
	InferenceTimeoutSec int
	ContextLimit        int
	OutputReserve       int
	SystemPrompt        string

	// Optional prompt budgets (tokens). Each part is a cap; what a part leaves unused goes to sensory.
	GoalBudget        int // GOAL_BUDGET
	SensoryBudget     int // SENSORY_BUDGET; 0 = no cap, sensory gets the rest of the window
	LongTermBudget    int // LONG_TERM_BUDGET
	ObservationBudget int // OBSERVATION_BUDGET
	SafetyMargin      int // PROMPT_SAFETY_MARGIN

	// Working memory (all optional; see the Default* constants).
	CallBudget       int     // WM_CALL_BUDGET: per-call prompt budget (tokens), related items included
	WaitLimitSec     int     // WM_WAIT_LIMIT_SEC: longest a strong chunk may wait in the queue; 0 = none
	IdleTimeoutSec   int     // WM_IDLE_TIMEOUT_SEC: idle time before a finished memory is committed
	Workers          int     // WM_WORKERS: parallel model workers
	RelatedItems     int     // WM_RELATED_ITEMS: related items from the same memory offered per call
	MaxItems         int     // WM_MAX_ITEMS: items held across all memories; pushes beyond get 503
	ReinforceStep    float64 // WM_REINFORCE_STEP: strength added by one reinforcement
	CommitTimeoutSec int     // WM_COMMIT_TIMEOUT_SEC: longest one commit may take
	CommitJournal    string  // WM_COMMIT_JOURNAL: file that receives commits until Task 31's endpoint exists
	EmbedURL         string  // WM_EMBED_URL: OpenAI-compatible embeddings server for scoring harness items
	EmbedModel       string  // WM_EMBED_MODEL: embedding model name
}

// Working memory defaults.
const (
	DefaultCallBudget       = 1024
	DefaultWaitLimitSec     = 600
	DefaultIdleTimeoutSec   = 60
	DefaultWorkers          = 1
	DefaultRelatedItems     = 5
	DefaultMaxItems         = 50000
	DefaultReinforceStep    = 0.1
	DefaultCommitTimeoutSec = 30
)

// WaitLimit returns the queue wait limit; 0 means no limit.
func (c *Config) WaitLimit() time.Duration { return time.Duration(c.WaitLimitSec) * time.Second }

// IdleTimeout returns the idle time before a finished memory is committed.
func (c *Config) IdleTimeout() time.Duration { return time.Duration(c.IdleTimeoutSec) * time.Second }

// CommitTimeout returns the longest one commit may take.
func (c *Config) CommitTimeout() time.Duration {
	return time.Duration(c.CommitTimeoutSec) * time.Second
}

// InferenceTimeout returns the duration for inference requests.
func (c *Config) InferenceTimeout() time.Duration {
	return time.Duration(c.InferenceTimeoutSec) * time.Second
}

// DefaultSystemConfigPath points to the standard system-wide environment configuration file.
const DefaultSystemConfigPath = "/etc/default/sekha"

// Load reads and validates configuration from the specified .env file (default ".env", or "/etc/default/sekha")
// and environment variables. Returns an error if any required configuration key is missing.
func Load(envPaths ...string) (*Config, error) {
	envPath := ".env"
	if len(envPaths) > 0 && envPaths[0] != "" {
		envPath = envPaths[0]
	} else if custom := os.Getenv("ENV_FILE"); custom != "" {
		envPath = custom
	} else if _, err := os.Stat(".env"); os.IsNotExist(err) {
		if _, err := os.Stat(DefaultSystemConfigPath); err == nil {
			envPath = DefaultSystemConfigPath
		}
	}

	envMap := make(map[string]string)
	if err := parseEnvFile(envPath, envMap); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to read env file %q: %w", envPath, err)
		}
		// If neither .env nor /etc/default/sekha exists, environment variables in os.Environ must fulfill all requirements
	}

	var missing []string

	portStr, ok := lookupKey("NODE_PORT", envMap)
	if !ok || portStr == "" {
		if alias, aliasOk := lookupKey("PORT", envMap); aliasOk && alias != "" {
			portStr = alias
			ok = true
		} else {
			missing = append(missing, "NODE_PORT")
		}
	}

	nodeName, ok := lookupKey("NODE_NAME", envMap)
	if !ok || nodeName == "" {
		missing = append(missing, "NODE_NAME")
	}

	inferenceURL, ok := lookupKey("INFERENCE_URL", envMap)
	if !ok || inferenceURL == "" {
		// check alias LLAMA_URL
		if alias, aliasOk := lookupKey("LLAMA_URL", envMap); aliasOk && alias != "" {
			inferenceURL = alias
		} else {
			missing = append(missing, "INFERENCE_URL")
		}
	}

	inferenceModel, ok := lookupKey("INFERENCE_MODEL", envMap)
	if !ok || inferenceModel == "" {
		// check alias LLAMA_MODEL
		if alias, aliasOk := lookupKey("LLAMA_MODEL", envMap); aliasOk && alias != "" {
			inferenceModel = alias
		} else {
			missing = append(missing, "INFERENCE_MODEL")
		}
	}

	timeoutStr, ok := lookupKey("INFERENCE_TIMEOUT_SEC", envMap)
	if !ok || timeoutStr == "" {
		missing = append(missing, "INFERENCE_TIMEOUT_SEC")
	}

	contextLimitStr, ok := lookupKey("CONTEXT_LIMIT", envMap)
	if !ok || contextLimitStr == "" {
		missing = append(missing, "CONTEXT_LIMIT")
	}

	outputReserveStr, ok := lookupKey("OUTPUT_RESERVE", envMap)
	if !ok || outputReserveStr == "" {
		missing = append(missing, "OUTPUT_RESERVE")
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required configuration: %s (must be set in %s or environment)", strings.Join(missing, ", "), envPath)
	}

	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		return nil, fmt.Errorf("invalid NODE_PORT: %q must be a positive integer", portStr)
	}

	timeoutSec, err := strconv.Atoi(timeoutStr)
	if err != nil || timeoutSec <= 0 {
		return nil, fmt.Errorf("invalid INFERENCE_TIMEOUT_SEC: %q must be a positive integer", timeoutStr)
	}

	contextLimit, err := strconv.Atoi(contextLimitStr)
	if err != nil || contextLimit <= 0 {
		return nil, fmt.Errorf("invalid CONTEXT_LIMIT: %q must be a positive integer", contextLimitStr)
	}

	outputReserve, err := strconv.Atoi(outputReserveStr)
	if err != nil || outputReserve <= 0 {
		return nil, fmt.Errorf("invalid OUTPUT_RESERVE: %q must be a positive integer", outputReserveStr)
	}

	systemPrompt, _ := lookupKey("SYSTEM_PROMPT", envMap)

	var goalBudget, sensoryBudget, longTermBudget, observationBudget, safetyMargin int
	for _, opt := range []struct {
		key    string
		target *int
		def    int
	}{
		{"GOAL_BUDGET", &goalBudget, budgeter.DefaultGoalBudget},
		{"SENSORY_BUDGET", &sensoryBudget, budgeter.DefaultSensoryBudget},
		{"LONG_TERM_BUDGET", &longTermBudget, budgeter.DefaultLongTermBudget},
		{"OBSERVATION_BUDGET", &observationBudget, budgeter.DefaultTrajectoryBudget},
		{"PROMPT_SAFETY_MARGIN", &safetyMargin, budgeter.DefaultSafetyMargin},
	} {
		if *opt.target, err = optionalNonNegativeInt(opt.key, opt.def, envMap); err != nil {
			return nil, err
		}
	}

	var wm struct {
		callBudget, waitLimit, idle, workers, related, maxItems, commitTimeout int
	}
	for _, opt := range []struct {
		key      string
		target   *int
		def      int
		positive bool
	}{
		{"WM_CALL_BUDGET", &wm.callBudget, DefaultCallBudget, true},
		{"WM_WAIT_LIMIT_SEC", &wm.waitLimit, DefaultWaitLimitSec, false},
		{"WM_IDLE_TIMEOUT_SEC", &wm.idle, DefaultIdleTimeoutSec, true},
		{"WM_WORKERS", &wm.workers, DefaultWorkers, true},
		{"WM_RELATED_ITEMS", &wm.related, DefaultRelatedItems, false},
		{"WM_MAX_ITEMS", &wm.maxItems, DefaultMaxItems, true},
		{"WM_COMMIT_TIMEOUT_SEC", &wm.commitTimeout, DefaultCommitTimeoutSec, true},
	} {
		if *opt.target, err = optionalNonNegativeInt(opt.key, opt.def, envMap); err != nil {
			return nil, err
		}
		if opt.positive && *opt.target == 0 {
			return nil, fmt.Errorf("invalid %s: must be greater than zero", opt.key)
		}
	}
	reinforceStep := DefaultReinforceStep
	if raw, ok := lookupKey("WM_REINFORCE_STEP", envMap); ok && raw != "" {
		if reinforceStep, err = strconv.ParseFloat(raw, 64); err != nil || reinforceStep <= 0 {
			return nil, fmt.Errorf("invalid WM_REINFORCE_STEP: %q must be a positive number", raw)
		}
	}
	commitJournal, _ := lookupKey("WM_COMMIT_JOURNAL", envMap)
	embedURL, _ := lookupKey("WM_EMBED_URL", envMap)
	embedModel, _ := lookupKey("WM_EMBED_MODEL", envMap)

	return &Config{
		Port:                port,
		NodeName:            nodeName,
		InferenceURL:        strings.TrimRight(inferenceURL, "/"),
		InferenceModel:      inferenceModel,
		InferenceTimeoutSec: timeoutSec,
		ContextLimit:        contextLimit,
		OutputReserve:       outputReserve,
		SystemPrompt:        systemPrompt,
		GoalBudget:          goalBudget,
		SensoryBudget:       sensoryBudget,
		LongTermBudget:      longTermBudget,
		ObservationBudget:   observationBudget,
		SafetyMargin:        safetyMargin,
		CallBudget:          wm.callBudget,
		WaitLimitSec:        wm.waitLimit,
		IdleTimeoutSec:      wm.idle,
		Workers:             wm.workers,
		RelatedItems:        wm.related,
		MaxItems:            wm.maxItems,
		ReinforceStep:       reinforceStep,
		CommitTimeoutSec:    wm.commitTimeout,
		CommitJournal:       commitJournal,
		EmbedURL:            strings.TrimRight(embedURL, "/"),
		EmbedModel:          embedModel,
	}, nil
}

// optionalNonNegativeInt reads an optional integer key, returning def when it is unset or empty.
func optionalNonNegativeInt(key string, def int, envMap map[string]string) (int, error) {
	raw, ok := lookupKey(key, envMap)
	if !ok || raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("invalid %s: %q must be a non-negative integer", key, raw)
	}
	return v, nil
}

func parseEnvFile(path string, target map[string]string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, val, found := strings.Cut(line, "=")
		if !found {
			continue
		}

		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)

		// Strip optional surrounding single or double quotes
		if (strings.HasPrefix(val, "\"") && strings.HasSuffix(val, "\"")) ||
			(strings.HasPrefix(val, "'") && strings.HasSuffix(val, "'")) {
			if len(val) >= 2 {
				val = val[1 : len(val)-1]
			}
		}

		target[key] = val
	}

	return scanner.Err()
}

func lookupKey(key string, fileMap map[string]string) (string, bool) {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		return val, true
	}
	val, ok := fileMap[key]
	return val, ok
}
