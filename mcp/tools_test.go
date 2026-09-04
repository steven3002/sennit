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
