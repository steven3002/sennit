package mcp_test

import (
	"strings"
	"testing"

	"github.com/steven3002/sennit/mcp"
	"github.com/steven3002/sennit/record"
)

// The descriptions are the product surface recall quality rests on, so the
// things they must say are locked rather than left to a future edit's judgement.
func TestRememberDescriptionElicitsWhatRecallDependsOn(t *testing.T) {
	text := strings.ToLower(mcp.RememberTool.Description)
	for _, must := range []string{
		"context",    // the largest measured contributor, and mandatory
		"tags",       // what the filter discriminates on
		"type",       // the other filter axis
		"supersedes", // so an update is not a duplicate
		"conflicts",  // so the agent acts on a near-duplicate
		"neighbours", // the evidence it acts on
		"reject",     // that a missing context is refused, not defaulted
		"specific",   // tag specificity, the measured single-domain failure
		"reuse",      // synonyms split what should be found together
	} {
		if !strings.Contains(text, must) {
			t.Errorf("the remember description never mentions %q", must)
		}
	}
	// Every type has to be named, or it is unreachable in practice.
	for _, kind := range record.Types {
		if !strings.Contains(text, string(kind)) {
			t.Errorf("the remember description never names the %q type", kind)
		}
	}
}

func TestRecallDescriptionSaysFiltersPreferRatherThanExclude(t *testing.T) {
	text := strings.ToLower(mcp.RecallTool.Description)
	for _, must := range []string{
		"tags",
		"types",
		"prefer",  // the property that makes guessing safe
		"guess",   // and the instruction that follows from it
		"history", // superseded records are reachable
		"empty",   // returning nothing is a real answer
	} {
		if !strings.Contains(text, must) {
			t.Errorf("the recall description never mentions %q", must)
		}
	}
	// An agent that thinks a filter excludes writes brittle filters to avoid
	// false positives, which is the behaviour that made hard filtering a cliff.
	if !strings.Contains(text, "do not exclude") && !strings.Contains(text, "not exclude") {
		t.Error("the recall description does not tell the agent that filters never exclude")
	}
}

// A blind study of 60 agent sessions found the shipped single-shot configuration
// answered 0 of 8 vague preference questions, and that the same query text with
// a licence to search more than once answered 5 of 8. The licence is what
// carries that gain, so it is locked here rather than left to a future edit.
func TestRecallDescriptionLicensesMoreThanOneSearch(t *testing.T) {
	text := strings.ToLower(mcp.RecallTool.Description)
	for _, must := range []string{
		"search again", // the instruction itself
		"attribute",    // what the second search is for
		"disagree",     // the other case a second search resolves
		"stop when",    // and that it is bounded, because each search costs context
	} {
		if !strings.Contains(text, must) {
			t.Errorf("the recall description never mentions %q", must)
		}
	}
	// The failure mode the licence exists for: a question in the user's own
	// words matches the record of them asking, not the fact that answers it.
	if !strings.Contains(text, "asked for the same thing before") {
		t.Error("the recall description does not say why one search can miss a durable fact")
	}
}

// A stored statement is a rewrite, and a rewrite loses detail silently. The
// trail back to the source is the only way to recover what was actually said,
// so the description has to ask for it rather than list it as an optional
// extra. Published ablation behind this: arXiv:2601.00821 measures the cost of
// substituting an LLM rewrite for source text at 22 points on LongMemEval-S.
func TestRememberDescriptionAsksForTheTrailBackToSource(t *testing.T) {
	text := strings.ToLower(mcp.RememberTool.Description)
	for _, must := range []string{
		"session", // the conversation the claim came from
		"span",    // and where in it
		"rewrite", // why the trail is needed at all
	} {
		if !strings.Contains(text, must) {
			t.Errorf("the remember description never mentions %q", must)
		}
	}
	// Provenance listed under OPTIONAL is what this change exists to undo.
	opt := strings.Index(text, "optional, and worth supplying")
	if opt >= 0 && strings.Index(text, "session") > opt {
		t.Error("the remember description introduces session only in the optional section")
	}
}

// Every type in the vocabulary needs a gloss, or an agent has to guess what one
// means. A type added without one should fail here rather than in the field.
func TestEveryTypeHasGuidance(t *testing.T) {
	guidance := mcp.TypeGuidance()
	if len(guidance) != len(record.Types) {
		t.Fatalf("%d types have guidance, the vocabulary has %d", len(guidance), len(record.Types))
	}
	for _, kind := range record.Types {
		if strings.TrimSpace(guidance[kind]) == "" {
			t.Errorf("type %q has no guidance", kind)
		}
	}
}

// The distinction that forced `correction` into the vocabulary has to survive in
// the description, because it is the whole reason the type exists: the
// bi-temporal fields express "was true, now outdated" and cannot express "was
// never true".
func TestTheCorrectionDistinctionIsStated(t *testing.T) {
	text := strings.ToLower(mcp.RememberTool.Description)
	if !strings.Contains(text, "never true") {
		t.Error("the remember description does not distinguish a correction from an outdated record")
	}
	if !strings.Contains(mcp.TypeGuidance()[record.TypeCorrection], "never true") {
		t.Error("the correction type's guidance does not state what makes it different")
	}
}

func TestEveryToolIsDescribed(t *testing.T) {
	for _, tool := range mcp.Tools {
		if tool.Name == "" || tool.Title == "" {
			t.Errorf("tool %+v is missing a name or title", tool)
		}
		if len(tool.Description) < 200 {
			t.Errorf("tool %q has a %d-character description; these carry recall quality",
				tool.Name, len(tool.Description))
		}
	}
}
