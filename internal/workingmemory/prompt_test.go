package workingmemory

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Duara-Cortex/sekha-working-scratchpad/internal/budgeter"
)

func TestBuildPromptContainsOnlyItsOwnSnapshot(t *testing.T) {
	chunk := Item{ItemID: "i2", Text: "the backup finished", Speaker: "assistant", Seq: 2}
	ctx := []Item{
		{ItemID: "i1", Text: "did the backup run?", Speaker: "user", Seq: 1},
		{ItemID: "i3", Text: "thanks", Speaker: "user", Seq: 3},
	}
	rec := []Item{{ItemID: "i9", Text: "Backups run nightly", Speaker: "cortex"}}
	p := BuildPrompt(PromptConfig{Budget: 1024}, "When did the backup run?", chunk, ctx, rec)

	for _, want := range []string{
		"### TASK\nWhen did the backup run?",
		"[c1] (assistant) the backup finished",
		"[x1] (previous, user) did the backup run?",
		"[x2] (next, user) thanks",
		"[r1] (cortex) Backups run nightly",
		"Used:",
	} {
		if !strings.Contains(p.User, want) {
			t.Fatalf("prompt missing %q:\n%s", want, p.User)
		}
	}
	want := map[string]string{"c1": "i2", "x1": "i1", "x2": "i3", "r1": "i9"}
	if !reflect.DeepEqual(p.Labels, want) {
		t.Fatalf("labels = %v", p.Labels)
	}
	if p.System != DefaultSystemPrompt {
		t.Fatal("default system prompt not used")
	}
}

func TestBuildPromptRelatedItemsCountTowardBudget(t *testing.T) {
	chunk := Item{ItemID: "i1", Text: "short chunk"}
	var rec []Item
	for i := 0; i < 20; i++ {
		rec = append(rec, Item{ItemID: "r", Text: strings.Repeat("related item words ", 15)})
	}
	base := BuildPrompt(PromptConfig{}, "task", chunk, nil, nil)
	budget := base.EstimatedTokens + 200

	p := BuildPrompt(PromptConfig{Budget: budget}, "task", chunk, nil, rec)
	if p.EstimatedTokens > budget {
		t.Fatalf("prompt %d tokens over budget %d", p.EstimatedTokens, budget)
	}
	if p.RelatedKept == 0 || p.RelatedDropped == 0 || p.RelatedKept+p.RelatedDropped != 20 {
		t.Fatalf("recall kept %d dropped %d", p.RelatedKept, p.RelatedDropped)
	}
	if got := budgeter.EstimateTokens(p.System) + budgeter.EstimateTokens(p.User); got != p.EstimatedTokens {
		t.Fatalf("estimate %d does not match the rendered prompt %d", p.EstimatedTokens, got)
	}
}

func TestBuildPromptTruncatesOversizedChunk(t *testing.T) {
	chunk := Item{ItemID: "i1", Text: strings.Repeat("very long chunk text ", 400)}
	p := BuildPrompt(PromptConfig{Budget: 400}, "task", chunk, []Item{{ItemID: "i0", Text: "ctx", Seq: -1}}, nil)
	if p.EstimatedTokens > 400 {
		t.Fatalf("prompt %d tokens over budget 400", p.EstimatedTokens)
	}
	if !strings.Contains(p.User, promptTruncation) {
		t.Fatal("oversized chunk was not truncated")
	}
}

func TestParseThought(t *testing.T) {
	cases := []struct {
		raw     string
		thought string
		used    []string
	}{
		{"Thought: the backup ran at 02:00\nUsed: c1, r2", "the backup ran at 02:00", []string{"c1", "r2"}},
		{"Thought: a\nsecond line\nUsed: [c1] [x1]", "a second line", []string{"c1", "x1"}},
		{"Thought: nothing new\nUsed: none", "nothing new", nil},
		{"no labels at all", "no labels at all", nil},
		{"Thought: a\nAction: ignored\nComplete: true", "a", nil},
	}
	for _, c := range cases {
		thought, used := ParseThought(c.raw)
		if thought != c.thought || !reflect.DeepEqual(used, c.used) {
			t.Errorf("ParseThought(%q) = %q, %v; want %q, %v", c.raw, thought, used, c.thought, c.used)
		}
	}
}
