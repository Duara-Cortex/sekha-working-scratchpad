package budgeter

import (
	"fmt"
	"strings"

	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/model"
)

// Default budget constraints for Qwen2.5-1.5B-Instruct on ARM NEON.
const (
	DefaultContextLimit = 2048
	DefaultOutputReserve = 256
	CharsPerTokenHeuristic = 3.8
)

// BudgetConfig defines partition limits for each cognitive memory section.
type BudgetConfig struct {
	MaxContextTokens int
	OutputReserve    int
	SystemBudget     int
	GoalBudget       int
	SensoryBudget    int
	LongTermBudget   int
	TrajectoryBudget int
}

// DefaultBudgetConfig returns a balanced 2,048-token configuration.
func DefaultBudgetConfig() BudgetConfig {
	return BudgetConfig{
		MaxContextTokens: DefaultContextLimit, // 2048
		OutputReserve:    DefaultOutputReserve, // 256
		SystemBudget:     320,                  // Never truncated
		GoalBudget:       150,
		SensoryBudget:    400,
		LongTermBudget:   350,
		TrajectoryBudget: 572, // 2048 - 256 - 320 - 150 - 400 - 350 = 572
	}
}

// ContextBudgeter formats and constrains deliberation prompts within token envelopes.
type ContextBudgeter struct {
	cfg BudgetConfig
}

// New creates a budgeter with the specified configuration.
func New(cfg BudgetConfig) *ContextBudgeter {
	if cfg.MaxContextTokens <= 0 {
		cfg = DefaultBudgetConfig()
	}
	return &ContextBudgeter{cfg: cfg}
}

// EstimateTokens calculates an approximate token count for a string using BPE character heuristic.
func EstimateTokens(text string) int {
	if len(text) == 0 {
		return 0
	}
	// Add padding for subword boundaries and whitespace
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

// BuildPrompt constructs a strictly budget-compliant prompt from working memory state.
func (b *ContextBudgeter) BuildPrompt(state model.WorkingMemoryState, currentObservation string) FormattedPrompt {
	systemPrompt := b.buildSystemPrompt()

	// 1. Goal Section
	goalText := strings.TrimSpace(state.ActiveGoal)
	if goalText == "" {
		goalText = "Perform step-by-step cognitive analysis and plan the next action."
	}
	goalSection := fmt.Sprintf("### ACTIVE TASK GOAL\n%s\n", b.truncateText(goalText, b.cfg.GoalBudget))

	// 2. Sensory Section (prioritize highest salience / recent)
	sensorySection := b.formatSensorySection(state.SensoryContext, b.cfg.SensoryBudget)

	// 3. Long-Term Knowledge Section
	ltmSection := b.formatLongTermSection(state.LongTermContext, b.cfg.LongTermBudget)

	// 4. Trajectory Section (rolling window of recent deliberation steps)
	trajSection := b.formatTrajectorySection(state.Trajectory, currentObservation, b.cfg.TrajectoryBudget)

	// 5. Action Instruction
	instruction := `### DELIBERATION INSTRUCTION
Analyze the active goal, sensory signals, retrieved context, and previous steps.
Formulate your internal chain of thought, followed by the next discrete action or decision.
Format your response strictly as:
Thought: <internal monologue, hypothesis testing, error check>
Action: <next tool call, query to Node 1/3, or final resolution>
Complete: <true if goal achieved, false if more deliberation needed>`

	userBuilder := strings.Builder{}
	userBuilder.WriteString(goalSection)
	userBuilder.WriteString("\n")
	if sensorySection != "" {
		userBuilder.WriteString(sensorySection)
		userBuilder.WriteString("\n")
	}
	if ltmSection != "" {
		userBuilder.WriteString(ltmSection)
		userBuilder.WriteString("\n")
	}
	if trajSection != "" {
		userBuilder.WriteString(trajSection)
		userBuilder.WriteString("\n")
	}
	userBuilder.WriteString(instruction)

	userPrompt := userBuilder.String()
	totalTokens := EstimateTokens(systemPrompt) + EstimateTokens(userPrompt)

	return FormattedPrompt{
		SystemPrompt:   systemPrompt,
		UserPrompt:     userPrompt,
		EstimatedTotal: totalTokens,
		BudgetLimit:    b.cfg.MaxContextTokens - b.cfg.OutputReserve,
	}
}

func (b *ContextBudgeter) buildSystemPrompt() string {
	return `You are the Working Memory Deliberation Engine on Node 2 of the Sekha Tri-Node Edge Cognitive Cluster.
Your role is active short-term reasoning, hypothesis formulation, and step-by-step plan deliberation.
You receive filtered sensory stimuli from Node 3 and associative knowledge from Node 1.
Reason carefully, verify hypotheses before concluding, and isolate errors internally.`
}

func (b *ContextBudgeter) formatSensorySection(chunks []model.SensoryChunk, budgetTokens int) string {
	if len(chunks) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("### RECENT SENSORY SIGNALS (Node 3 Filtered)\n")

	// Iterate backwards from most recent
	accumulated := 0
	var selected []string
	for i := len(chunks) - 1; i >= 0; i-- {
		c := chunks[i]
		line := fmt.Sprintf("- [salience=%.2f] %s", c.Salience, strings.TrimSpace(c.Text))
		t := EstimateTokens(line)
		if accumulated+t > budgetTokens && len(selected) > 0 {
			break
		}
		selected = append([]string{line}, selected...)
		accumulated += t
	}

	for _, l := range selected {
		sb.WriteString(l)
		sb.WriteString("\n")
	}
	return sb.String()
}

func (b *ContextBudgeter) formatLongTermSection(facts []string, budgetTokens int) string {
	if len(facts) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("### ASSOCIATIVE KNOWLEDGE (Node 1 Long-Term)\n")

	accumulated := 0
	for _, f := range facts {
		line := fmt.Sprintf("- %s", strings.TrimSpace(f))
		t := EstimateTokens(line)
		if accumulated+t > budgetTokens && accumulated > 0 {
			break
		}
		sb.WriteString(line)
		sb.WriteString("\n")
		accumulated += t
	}
	return sb.String()
}

func (b *ContextBudgeter) formatTrajectorySection(steps []model.ReasoningStep, currentObs string, budgetTokens int) string {
	if len(steps) == 0 && currentObs == "" {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("### DELIBERATION TRAJECTORY (Past Steps)\n")

	// Keep rolling window of steps that fit in budgetTokens
	var stepStrs []string
	accumulated := 0
	for i := len(steps) - 1; i >= 0; i-- {
		s := steps[i]
		line := fmt.Sprintf("Step %d [%s]:\n  Thought: %s\n  Action: %s", s.StepIndex, s.Status, s.Thought, s.Action)
		if s.Observation != "" {
			line += fmt.Sprintf("\n  Observation: %s", s.Observation)
		}
		t := EstimateTokens(line)
		if accumulated+t > budgetTokens && len(stepStrs) > 0 {
			break
		}
		stepStrs = append([]string{line}, stepStrs...)
		accumulated += t
	}

	for _, s := range stepStrs {
		sb.WriteString(s)
		sb.WriteString("\n\n")
	}

	if currentObs != "" {
		sb.WriteString(fmt.Sprintf("Latest Observation: %s\n", currentObs))
	}

	return sb.String()
}

func (b *ContextBudgeter) truncateText(text string, maxTokens int) string {
	t := EstimateTokens(text)
	if t <= maxTokens {
		return text
	}
	maxChars := int(float64(maxTokens) * CharsPerTokenHeuristic)
	if len(text) > maxChars {
		return text[:maxChars] + "... [truncated]"
	}
	return text
}
