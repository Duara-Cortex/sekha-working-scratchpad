package budgeter

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/model"
)

// Default budget constraints for Qwen2.5-1.5B-Instruct on ARM NEON.
// Every per-part budget is a cap on that part: whatever a part does not use flows to sensory.
const (
	DefaultContextLimit    = 2048
	DefaultOutputReserve   = 256
	CharsPerTokenHeuristic = 3.8

	DefaultSystemBudget     = 320 // Deprecated: the system prompt is counted at its actual size.
	DefaultGoalBudget       = 150
	DefaultSensoryBudget    = 0 // 0 = no cap: sensory gets the rest of the window.
	DefaultLongTermBudget   = 350
	DefaultTrajectoryBudget = 572 // cap on the current observation
	DefaultSafetyMargin     = 32  // headroom for chat-template tokens and estimator error
)

// Config defines partition limits for each cognitive memory section.
type Config struct {
	MaxContextTokens int
	OutputReserve    int
	// SystemBudget is deprecated and unused: the system prompt is counted at its actual size.
	SystemBudget   int
	GoalBudget     int
	SensoryBudget  int // cap on the sensory section; 0 means the rest of the window
	LongTermBudget int
	// TrajectoryBudget caps the current observation.
	TrajectoryBudget int
	// SafetyMargin is subtracted from the prompt window before packing.
	SafetyMargin int
	SystemPrompt string
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
		SafetyMargin:     DefaultSafetyMargin,
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
	// Usage reports what was kept and dropped. ActualPromptTokens is left for the caller to fill.
	Usage model.ContextUsage
}

const (
	sensoryHeader     = "### RECENT SENSORY SIGNALS (Node 3 Filtered)\n"
	longTermHeader    = "### ASSOCIATIVE KNOWLEDGE (Node 1 Long-Term)\n"
	truncationMarker  = "... [truncated]"
	instructionPrompt = `### DELIBERATION INSTRUCTION
Analyze the active goal, sensory signals, retrieved context, and observations.
Formulate your internal chain of thought, followed by the next discrete action or decision.
Format your response strictly as:
Thought: <internal monologue, hypothesis testing, error check>
Action: <next tool call, query to Node 1/3, or final resolution>
Complete: <true if goal achieved, false if more deliberation needed>`
)

// promptParts holds the rendered sections of one prompt.
type promptParts struct {
	goal         string
	sensoryLines []string
	factLines    []string
	observation  string
}

// BuildPrompt constructs a budget-compliant prompt from the incoming deliberation request.
//
// Goal and observation are capped by their budgets. The prompt window (context limit minus output
// reserve minus safety margin) is then filled with facts (in rank order, capped by LongTermBudget)
// and sensory chunks, which get the rest. When chunks must be dropped, the lowest-salience ones go
// first, at most one chunk is truncated, and the kept chunks stay in their original order.
//
// With req.Prepacked, every chunk and fact is kept if the rendered prompt fits the window. If it
// does not, the per-part caps are ignored, chunks give way first (by salience) and facts are only
// dropped from the lowest-ranked end when they alone overflow the window.
func (b *Budgeter) BuildPrompt(req model.DeliberateRequest) FormattedPrompt {
	systemPrompt := b.buildSystemPrompt()
	window := b.cfg.MaxContextTokens - b.cfg.OutputReserve
	if window < 0 {
		window = 0
	}
	limit := window - b.cfg.SafetyMargin

	parts := promptParts{
		goal:        b.goalText(req.Objective),
		observation: b.observationText(req.Observation),
	}
	allFacts := formatFacts(req.LongTermContext)
	total := func(p promptParts) int {
		return EstimateTokens(systemPrompt) + EstimateTokens(renderUser(p))
	}

	var sel sensorySelection
	factsKept := 0
	fitsWhole := false
	if req.Prepacked {
		whole := parts
		whole.factLines = allFacts
		whole.sensoryLines = formatChunks(req.SensoryChunks)
		if total(whole) <= limit {
			parts = whole
			fitsWhole = true
			factsKept = len(allFacts)
			sel = sensorySelection{kept: len(req.SensoryChunks)}
		}
	}

	if !fitsWhole {
		avail := limit - total(parts)

		factCap := b.cfg.LongTermBudget
		if req.Prepacked || factCap > avail {
			factCap = avail
		}
		factsKept = fitFacts(allFacts, factCap)
		parts.factLines = allFacts[:factsKept]

		sensoryBudget := limit - total(parts)
		if !req.Prepacked && b.cfg.SensoryBudget > 0 && sensoryBudget > b.cfg.SensoryBudget {
			sensoryBudget = b.cfg.SensoryBudget
		}
		sel = selectBySalience(req.SensoryChunks, sensoryBudget)
		parts.sensoryLines = sel.lines

		// Per-line estimates floor, so the rendered prompt can come out slightly over. Shrink sensory
		// first, then facts, until the rendered estimate fits.
		for over := total(parts) - limit; over > 0; over = total(parts) - limit {
			if len(parts.sensoryLines) > 0 {
				sensoryBudget -= over
				sel = selectBySalience(req.SensoryChunks, sensoryBudget)
				parts.sensoryLines = sel.lines
			} else if factsKept > 0 {
				factsKept--
				parts.factLines = allFacts[:factsKept]
			} else {
				break
			}
		}
	}

	userPrompt := renderUser(parts)
	totalTokens := EstimateTokens(systemPrompt) + EstimateTokens(userPrompt)

	return FormattedPrompt{
		SystemPrompt:   systemPrompt,
		UserPrompt:     userPrompt,
		EstimatedTotal: totalTokens,
		BudgetLimit:    window,
		Usage: model.ContextUsage{
			SensoryReceived:       len(req.SensoryChunks),
			SensoryKept:           sel.kept,
			SensoryDropped:        len(req.SensoryChunks) - sel.kept,
			SensoryTruncated:      sel.truncated,
			FactsReceived:         len(req.LongTermContext),
			FactsKept:             factsKept,
			EstimatedPromptTokens: totalTokens,
			PromptWindowTokens:    window,
		},
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

func (b *Budgeter) goalText(objective string) string {
	goalText := strings.TrimSpace(objective)
	if goalText == "" {
		goalText = "Perform step-by-step cognitive analysis and plan the next action."
	}
	return b.truncateText(goalText, b.cfg.GoalBudget)
}

func (b *Budgeter) observationText(currentObs string) string {
	obs := strings.TrimSpace(currentObs)
	if obs == "" {
		return ""
	}
	return b.truncateText(obs, b.cfg.TrajectoryBudget)
}

func renderUser(p promptParts) string {
	var sb strings.Builder
	sb.WriteString("### ACTIVE TASK GOAL\n")
	sb.WriteString(p.goal)
	sb.WriteString("\n\n")

	if len(p.sensoryLines) > 0 {
		sb.WriteString(sensoryHeader)
		for _, line := range p.sensoryLines {
			sb.WriteString(line)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	if len(p.factLines) > 0 {
		sb.WriteString(longTermHeader)
		for _, line := range p.factLines {
			sb.WriteString(line)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	if p.observation != "" {
		sb.WriteString("### CURRENT OBSERVATION\n")
		sb.WriteString(p.observation)
		sb.WriteString("\n\n")
	}

	sb.WriteString(instructionPrompt)
	return sb.String()
}

func formatFacts(facts []string) []string {
	lines := make([]string, len(facts))
	for i, f := range facts {
		lines[i] = fmt.Sprintf("- %s", strings.TrimSpace(f))
	}
	return lines
}

// fitFacts returns how many facts, taken in rank order, fit in budget tokens including the header.
func fitFacts(lines []string, budget int) int {
	used := EstimateTokens(longTermHeader + "\n")
	for i, line := range lines {
		used += EstimateTokens(line + "\n")
		if used > budget {
			return i
		}
	}
	return len(lines)
}

func chunkPrefix(c model.SensoryChunk) string {
	return fmt.Sprintf("- [salience=%.2f] ", c.Salience)
}

func formatChunks(chunks []model.SensoryChunk) []string {
	lines := make([]string, len(chunks))
	for i, c := range chunks {
		lines[i] = chunkPrefix(c) + strings.TrimSpace(c.Text)
	}
	return lines
}

// sensorySelection is the outcome of packing chunks: the rendered lines in original order.
type sensorySelection struct {
	lines     []string
	kept      int
	truncated int
}

// selectBySalience keeps the highest-salience chunks whole, truncates the next one to fill the
// remaining budget, and drops the rest. Ties keep the earlier chunk. The kept chunks are returned
// in their original order.
func selectBySalience(chunks []model.SensoryChunk, budget int) sensorySelection {
	if len(chunks) == 0 {
		return sensorySelection{}
	}
	formatted := formatChunks(chunks)
	order := make([]int, len(chunks))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return chunks[order[a]].Salience > chunks[order[b]].Salience
	})

	remaining := budget - EstimateTokens(sensoryHeader+"\n")
	keep := make([]string, len(chunks))
	var sel sensorySelection
	for _, i := range order {
		cost := EstimateTokens(formatted[i] + "\n")
		if cost <= remaining {
			keep[i] = formatted[i]
			remaining -= cost
			sel.kept++
			continue
		}
		prefix := chunkPrefix(chunks[i])
		textBudget := remaining - EstimateTokens(prefix) - EstimateTokens(truncationMarker+"\n")
		if textBudget > 0 {
			maxChars := int(float64(textBudget) * CharsPerTokenHeuristic)
			runes := []rune(strings.TrimSpace(chunks[i].Text))
			if len(runes) > maxChars {
				runes = runes[:maxChars]
			}
			keep[i] = prefix + string(runes) + truncationMarker
			sel.kept++
			sel.truncated++
		}
		break
	}

	for _, line := range keep {
		if line != "" {
			sel.lines = append(sel.lines, line)
		}
	}
	return sel
}

func (b *Budgeter) truncateText(text string, maxTokens int) string {
	if EstimateTokens(text) <= maxTokens {
		return text
	}
	maxChars := int(float64(maxTokens) * CharsPerTokenHeuristic)
	runes := []rune(text)
	if len(runes) > maxChars {
		return string(runes[:maxChars]) + truncationMarker
	}
	return text
}
