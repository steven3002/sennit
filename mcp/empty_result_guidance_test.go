package mcp_test

import (
	"strings"
	"testing"

	"github.com/steven3002/sennit/mcp"
)

// What an agent is told an empty result means depends on whether this device
// has restored the vault, and both places that tell it say so.
//
// A search reads only what this device holds. Every agent that connects reads
// the instructions, and every agent that lists the tools reads recall's
// description, and both said that an empty result means the vault does not
// hold the record. On a device that has not restored the vault that is not
// true, and an agent that believes it tells the user something is not stored
// when it is. Each now says when an empty result means that, and what to do
// when the hint says this device has not restored all of the vault.
func TestTheGuidanceSaysWhatAnEmptyResultMeansOnEachKindOfDevice(t *testing.T) {
	for name, text := range map[string]string{
		"the instructions":              strings.Join(strings.Fields(mcp.Instructions), " "),
		"the recall tool's description": strings.Join(strings.Fields(mcp.RecallTool.Description), " "),
	} {
		for _, must := range []string{
			"On a device that has restored the vault",
			"has not restored all of",
			"the hint says what is missing",
			"sennit hydrate",
		} {
			if !strings.Contains(text, must) {
				t.Errorf("%s: %q is not said", name, must)
			}
		}
		for _, stale := range []string{
			"An empty result is a real answer and means the vault does not hold it.",
			"An empty result is a real answer. It means the vault does not hold this.",
		} {
			if strings.Contains(text, stale) {
				t.Errorf("%s: %q is still said of every device", name, stale)
			}
		}
	}
}
