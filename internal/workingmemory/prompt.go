package workingmemory

import (
	"fmt"
	"strings"

	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/budgeter"
)

// DefaultSystemPrompt is the system prompt for per-chunk processing.
const DefaultSystemPrompt = `You are the working memory of Node 2 in the Sekha Tri-Node Edge Cognitive Cluster.
You consider one item at a time against a task. Every item is labelled, e.g. [c1].
Use only what the labelled items say. Do not add facts, events or values that are not written in them.`

const (
	chunkLabel       = "c1"
	contextLabelFmt  = "x%d"
	relatedLabelFmt  = "r%d"
	promptTruncation = "... [truncated]"
	instruction      = `### INSTRUCTION
Think about the item under consideration [c1] in light of the task. The other items are read-only context.
Format your response strictly as:
Thought: <what [c1] means for the task, using only the items above>
Used: <labels of the items your thought relies on, e.g. c1, r2; or none>`
)

// PromptConfig sets the per-call prompt budget.
type PromptConfig struct {
	// Budget caps the whole prompt (system + user) in estimated tokens. Related items count
	// toward it.
	Budget int
	// TaskBudget caps the task text.
	TaskBudget   int
	SystemPrompt string
}

// Prompt is one stateless per-chunk prompt.
type Prompt struct {
	System string
	User   string
	// Labels maps each label in the prompt to the item it stands for.
	Labels          map[string]string
	EstimatedTokens int
	ContextKept     int
	RelatedKept     int
	RelatedDropped  int
}

type promptLine struct {
	label string
	item  string
	text  string
}

// BuildPrompt builds the prompt for one model call from the call's own snapshot only: the chunk
// being processed, read-only context from its links, and related items from the same memory in the
// temp DB. The chunk always goes in (truncated if it alone overflows the budget); context and then
// related items are added in order while they fit.
func BuildPrompt(cfg PromptConfig, task string, chunk Item, context []Item, related []Item) Prompt {
	system := cfg.SystemPrompt
	if system == "" {
		system = DefaultSystemPrompt
	}
	task = strings.TrimSpace(task)
	if task == "" {
		task = "(no task given)"
	}
	if cfg.TaskBudget > 0 {
		task = truncateTokens(task, cfg.TaskBudget)
	}

	main := promptLine{label: chunkLabel, item: chunk.ItemID, text: describe(chunk, "")}
	var ctxLines, relatedLines []promptLine

	render := func() string {
		var sb strings.Builder
		sb.WriteString("### TASK\n")
		sb.WriteString(task)
		sb.WriteString("\n\n### ITEM UNDER CONSIDERATION\n")
		writeLine(&sb, main)
		if len(ctxLines) > 0 {
			sb.WriteString("\n### CONTEXT (read-only)\n")
			for _, l := range ctxLines {
				writeLine(&sb, l)
			}
		}
		if len(relatedLines) > 0 {
			sb.WriteString("\n### RELATED ITEMS IN THIS MEMORY (read-only)\n")
			for _, l := range relatedLines {
				writeLine(&sb, l)
			}
		}
		sb.WriteString("\n")
		sb.WriteString(instruction)
		return sb.String()
	}
	total := func() int { return budgeter.EstimateTokens(system) + budgeter.EstimateTokens(render()) }

	// Per-part estimates round down, so shrink until the rendered prompt fits.
	full := main.text
	keep := budgeter.EstimateTokens(full)
	for over := total() - cfg.Budget; cfg.Budget > 0 && over > 0 && keep > 1; over = total() - cfg.Budget {
		keep -= over
		if keep < 1 {
			keep = 1
		}
		main.text = truncateTokens(full, keep)
	}

	fits := func() bool { return cfg.Budget <= 0 || total() <= cfg.Budget }

	p := Prompt{Labels: map[string]string{chunkLabel: chunk.ItemID}}
	for _, c := range context {
		l := promptLine{label: fmt.Sprintf(contextLabelFmt, len(ctxLines)+1), item: c.ItemID}
		rel := "previous"
		if c.Seq > chunk.Seq {
			rel = "next"
		}
		l.text = describe(c, rel)
		ctxLines = append(ctxLines, l)
		if !fits() {
			ctxLines = ctxLines[:len(ctxLines)-1]
		}
	}
	for _, r := range related {
		l := promptLine{label: fmt.Sprintf(relatedLabelFmt, len(relatedLines)+1), item: r.ItemID, text: describe(r, "")}
		relatedLines = append(relatedLines, l)
		if !fits() {
			relatedLines = relatedLines[:len(relatedLines)-1]
			p.RelatedDropped++
		}
	}

	for _, lines := range [][]promptLine{ctxLines, relatedLines} {
		for _, l := range lines {
			p.Labels[l.label] = l.item
		}
	}
	p.System = system
	p.User = render()
	p.EstimatedTokens = total()
	p.ContextKept = len(ctxLines)
	p.RelatedKept = len(relatedLines)
	return p
}

func writeLine(sb *strings.Builder, l promptLine) {
	fmt.Fprintf(sb, "[%s] %s\n", l.label, l.text)
}

// describe renders an item's text with its speaker (dialogue) or heading (documents).
func describe(it Item, rel string) string {
	var tags []string
	if rel != "" {
		tags = append(tags, rel)
	}
	if it.Speaker != "" {
		tags = append(tags, it.Speaker)
	} else if it.Heading != "" {
		tags = append(tags, it.Heading)
	}
	text := strings.TrimSpace(it.Text)
	if len(tags) == 0 {
		return text
	}
	return "(" + strings.Join(tags, ", ") + ") " + text
}

func truncateTokens(text string, maxTokens int) string {
	if budgeter.EstimateTokens(text) <= maxTokens {
		return text
	}
	maxChars := int(float64(maxTokens) * budgeter.CharsPerTokenHeuristic)
	runes := []rune(text)
	if len(runes) > maxChars {
		return string(runes[:maxChars]) + promptTruncation
	}
	return text
}

// ParseThought reads the model's "Thought:" and "Used:" lines. Labels are returned without
// brackets; "none" yields no labels. Output without a "Thought:" line is taken whole as the thought.
func ParseThought(raw string) (thought string, used []string) {
	var lines []string
	sawThought := false
	inThought := false
	for _, line := range strings.Split(raw, "\n") {
		t := strings.TrimSpace(line)
		lower := strings.ToLower(t)
		switch {
		case strings.HasPrefix(lower, "thought:"):
			sawThought, inThought = true, true
			if rest := strings.TrimSpace(t[len("thought:"):]); rest != "" {
				lines = append(lines, rest)
			}
		case strings.HasPrefix(lower, "used:"):
			inThought = false
			used = append(used, parseLabels(t[len("used:"):])...)
		case strings.HasPrefix(lower, "action:"), strings.HasPrefix(lower, "complete:"):
			inThought = false
		case t == "":
		case inThought || !sawThought:
			lines = append(lines, t)
		}
	}
	return strings.TrimSpace(strings.Join(lines, " ")), used
}

func parseLabels(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		f = strings.ToLower(strings.Trim(f, "[]()."))
		if f == "" || f == "none" {
			continue
		}
		out = append(out, f)
	}
	return out
}
