package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
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

	return &Config{
		Port:                port,
		NodeName:            nodeName,
		InferenceURL:        strings.TrimRight(inferenceURL, "/"),
		InferenceModel:      inferenceModel,
		InferenceTimeoutSec: timeoutSec,
		ContextLimit:        contextLimit,
		OutputReserve:       outputReserve,
		SystemPrompt:        systemPrompt,
	}, nil
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
