package budgeter

import (
	"fmt"
	"strings"

	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/model"
)

// Default budget constraints for Qwen2.5-1.5B-Instruct on ARM NEON.
const (
	DefaultContextLimit    = 2048
	DefaultOutputReserve   = 256
	CharsPerTokenHeuristic = 3.8

	DefaultSystemBudget     = 320
	DefaultGoalBudget       = 150
	DefaultSensoryBudget    = 400
	DefaultLongTermBudget   = 350
	DefaultTrajectoryBudget = 572 // 2048 - 256 - 320 - 150 - 400 - 350 = 572
)

// Config defines partition limits for each cognitive memory section.
type Config struct {
	MaxContextTokens int
	OutputReserve    int
	SystemBudget     int
	GoalBudget       int
	SensoryBudget    int
	LongTermBudget   int
	TrajectoryBudget int
	SystemPrompt     string
}

// BudgetConfig provides a backwards-compatible type alias for Config.
type BudgetConfig = Config

// DefaultConfig returns a balanced 2,048-token configuration.
func DefaultConfig() Config {
	return Config{
		MaxContextTokens: DefaultContextLimit,
		OutputReserve:    DefaultOutputReserve,
		SystemBudget:     DefaultSystemBudget,
		GoalBudget:       DefaultGoalBudget,
		SensoryBudget:    DefaultSensoryBudget,
		LongTermBudget:   DefaultLongTermBudget,
		TrajectoryBudget: DefaultTrajectoryBudget,
	}
}

// DefaultBudgetConfig provides a backwards-compatible constructor for DefaultConfig.
func DefaultBudgetConfig() Config {
	return DefaultConfig()
}

// Budgeter formats and constrains deliberation prompts within token envelopes.
type Budgeter struct {
	cfg Config
}

// ContextBudgeter provides a backwards-compatible type alias for Budgeter.
type ContextBudgeter = Budgeter

// New creates a budgeter with the specified configuration.
func New(cfg Config) *Budgeter {
	if cfg.MaxContextTokens <= 0 {
		cfg = DefaultConfig()
	}
	return &Budgeter{cfg: cfg}
}

// EstimateTokens calculates an approximate token count for a string using BPE character heuristic.
func EstimateTokens(text string) int {
	if len(text) == 0 {
		return 0
	}
	words := len(strings.Fields(text))
	charTokens := int(float64(len(text)) / CharsPerTokenHeuristic)
	if words > charTokens {
		return words
	}
	return charTokens
}

// FormattedPrompt represents the budget-safe prompt payload ready for the inference engine.
type FormattedPrompt struct {
	SystemPrompt   string
	UserPrompt     string
	EstimatedTotal int
	BudgetLimit    int
}

// BuildPrompt constructs a strictly budget-compliant prompt from the incoming deliberation request.
func (b *Budgeter) BuildPrompt(req model.DeliberateRequest) FormattedPrompt {
	systemPrompt := b.buildSystemPrompt()

	var userBuilder strings.Builder
	b.appendGoalSection(&userBuilder, req.Objective)
	b.appendSensorySection(&userBuilder, req.SensoryChunks)
	b.appendLongTermSection(&userBuilder, req.LongTermContext)
	b.appendObservationSection(&userBuilder, req.Observation)
	b.appendInstructionSection(&userBuilder)

	userPrompt := userBuilder.String()
	totalTokens := EstimateTokens(systemPrompt) + EstimateTokens(userPrompt)

	return FormattedPrompt{
		SystemPrompt:   systemPrompt,
		UserPrompt:     userPrompt,
		EstimatedTotal: totalTokens,
		BudgetLimit:    b.cfg.MaxContextTokens - b.cfg.OutputReserve,
	}
}

func (b *Budgeter) buildSystemPrompt() string {
	if b.cfg.SystemPrompt != "" {
		return b.cfg.SystemPrompt
	}
	return `You are the Working Memory Deliberation Engine on Node 2 of the Sekha Tri-Node Edge Cognitive Cluster.
Your role is active short-term reasoning, hypothesis formulation, and step-by-step plan deliberation.
You receive filtered sensory stimuli from Node 3 and associative knowledge from Node 1.
Reason carefully, verify hypotheses before concluding, and isolate errors internally.`
}

func (b *Budgeter) appendGoalSection(sb *strings.Builder, objective string) {
	goalText := strings.TrimSpace(objective)
	if goalText == "" {
		goalText = "Perform step-by-step cognitive analysis and plan the next action."
	}
	sb.WriteString("### ACTIVE TASK GOAL\n")
	sb.WriteString(b.truncateText(goalText, b.cfg.GoalBudget))
	sb.WriteString("\n\n")
}

func (b *Budgeter) appendSensorySection(sb *strings.Builder, chunks []model.SensoryChunk) {
	if len(chunks) == 0 {
		return
	}

	// Collect most recent signals up to sensory budget
	var selected []string
	accumulated := 0
	for i := len(chunks) - 1; i >= 0; i-- {
		c := chunks[i]
		line := fmt.Sprintf("- [salience=%.2f] %s", c.Salience, strings.TrimSpace(c.Text))
		t := EstimateTokens(line)
		if accumulated+t > b.cfg.SensoryBudget && len(selected) > 0 {
			break
		}
		selected = append(selected, line)
		accumulated += t
	}

	sb.WriteString("### RECENT SENSORY SIGNALS (Node 3 Filtered)\n")
	for i := len(selected) - 1; i >= 0; i-- {
		sb.WriteString(selected[i])
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
}

func (b *Budgeter) appendLongTermSection(sb *strings.Builder, facts []string) {
	if len(facts) == 0 {
		return
	}

	var factsBuilder strings.Builder
	accumulated := 0
	for _, f := range facts {
		line := fmt.Sprintf("- %s", strings.TrimSpace(f))
		t := EstimateTokens(line)
		if accumulated+t > b.cfg.LongTermBudget && accumulated > 0 {
			break
		}
		factsBuilder.WriteString(line)
		factsBuilder.WriteString("\n")
		accumulated += t
	}

	if factsBuilder.Len() > 0 {
		sb.WriteString("### ASSOCIATIVE KNOWLEDGE (Node 1 Long-Term)\n")
		sb.WriteString(factsBuilder.String())
		sb.WriteString("\n")
	}
}

func (b *Budgeter) appendObservationSection(sb *strings.Builder, currentObs string) {
	obs := strings.TrimSpace(currentObs)
	if obs == "" {
		return
	}

	sb.WriteString("### CURRENT OBSERVATION\n")
	sb.WriteString(b.truncateText(obs, b.cfg.TrajectoryBudget))
	sb.WriteString("\n\n")
}

func (b *Budgeter) appendInstructionSection(sb *strings.Builder) {
	sb.WriteString(`### DELIBERATION INSTRUCTION
Analyze the active goal, sensory signals, retrieved context, and observations.
Formulate your internal chain of thought, followed by the next discrete action or decision.
Format your response strictly as:
Thought: <internal monologue, hypothesis testing, error check>
Action: <next tool call, query to Node 1/3, or final resolution>
Complete: <true if goal achieved, false if more deliberation needed>`)
}

func (b *Budgeter) truncateText(text string, maxTokens int) string {
	if EstimateTokens(text) <= maxTokens {
		return text
	}
	maxChars := int(float64(maxTokens) * CharsPerTokenHeuristic)
	runes := []rune(text)
	if len(runes) > maxChars {
		return string(runes[:maxChars]) + "... [truncated]"
	}
	return text
}
