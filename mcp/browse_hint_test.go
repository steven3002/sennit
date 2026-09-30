package mcp_test

import (
	"strings"
	"testing"
	"time"

	"github.com/steven3002/sennit/mcp"
)

// A filtered browse that lists nothing names the filters that were set, with
// the values that ran, and tells the model to drop only those.
//
// A hint that says "drop a tag" when only a type was set sends the model after a
// tag it never passed. The pointer to `recall` is given only when tags or types
// were set, because those are the filters recall treats as a preference; its
// scope excludes a class exactly as kinds does here, so it is no remedy for a
// page that kinds alone emptied. A blank tag and a kinds list naming both
// classes excluded nothing, so neither is named.
func TestAFilteredBrowseNamesOnlyTheFiltersThatWereSet(t *testing.T) {
	session, _ := serve(t)
	storeMemory(t, session, mcp.RememberIn{
		Statement: "The west gauge reads a metre high at the spring tide.",
		Context:   "Written so the filters have a record to exclude.",
		Type:      "fact",
		Tags:      []string{"west", "gauge"},
	})

	for name, want := range map[string]struct {
		in     mcp.BrowseIn
		names  string   // how the hint names the filters that ran
		advice string   // what it tells the model to try
		absent []string // what it must not say, because it was not set
	}{
		"a type alone": {
			in:     mcp.BrowseIn{Types: []string{"preference"}},
			names:  `with types ["preference"] set.`,
			advice: "Drop the type, or use `recall` with the same types, which only prefer there",
			absent: []string{"tag", "kinds"},
		},
		"two types": {
			in:     mcp.BrowseIn{Types: []string{"preference", "profile"}},
			names:  `with types ["preference", "profile"] set.`,
			advice: "Drop types, or use `recall` with the same types, which only prefer there",
			absent: []string{"tag", "kinds"},
		},
		"a class alone": {
			in:     mcp.BrowseIn{Kinds: []string{"session"}},
			names:  `with kinds ["session"] set.`,
			advice: "Drop kinds to list every class of record.",
			absent: []string{"tag", "type", "`recall`"},
		},
		"a tag alone": {
			in:     mcp.BrowseIn{Tags: []string{"east"}},
			names:  `with tags ["east"] set.`,
			advice: "Drop the tag, or use `recall` with the same tags, which only prefer there",
			absent: []string{"type", "kinds"},
		},
		"two tags, one the record lacks": {
			in:     mcp.BrowseIn{Tags: []string{"west", "east"}},
			names:  `with tags ["west", "east"] set.`,
			advice: "Drop a tag, or use `recall` with the same tags, which only prefer there",
			absent: []string{"type", "kinds"},
		},
		"a blank tag beside a type": {
			in:     mcp.BrowseIn{Types: []string{"preference"}, Tags: []string{" "}},
			names:  `with types ["preference"] set.`,
			advice: "Drop the type, or use `recall` with the same types, which only prefer there",
			absent: []string{"tag", "kinds"},
		},
		"both classes beside a tag": {
			in:     mcp.BrowseIn{Kinds: []string{"memory", "session"}, Tags: []string{"east"}},
			names:  `with tags ["east"] set.`,
			advice: "Drop the tag, or use `recall` with the same tags, which only prefer there",
			absent: []string{"type", "kinds"},
		},
		"every filter, with a tag as it was typed": {
			in: mcp.BrowseIn{
				Kinds: []string{"session"}, Types: []string{"fact"}, Tags: []string{" West ", "gauge"},
			},
			// The tag is named as the vault compared it, which is the form the
			// vault's own tag list uses.
			names: `with kinds ["session"], types ["fact"] and tags ["west", "gauge"] set.`,
			advice: "Drop kinds, the type or a tag, or use `recall` with the same types and tags, " +
				"which only prefer there and cannot empty a result.",
		},
	} {
		var out mcp.BrowseOut
		result := call(t, session, "browse", want.in, &out)
		if len(out.Rows) != 0 {
			t.Fatalf("%s: listed %+v, want nothing", name, out.Rows)
		}
		if !strings.Contains(out.Hint, "Nothing is listed "+want.names) {
			t.Errorf("%s: the hint does not name the filters as %q: %q", name, want.names, out.Hint)
		}
		if !strings.Contains(out.Hint, "Every filter here EXCLUDES") {
			t.Errorf("%s: the hint does not say the filters exclude: %q", name, out.Hint)
		}
		if !strings.Contains(out.Hint, want.advice) {
			t.Errorf("%s: the hint does not advise %q: %q", name, want.advice, out.Hint)
		}
		for _, wrong := range want.absent {
			if strings.Contains(out.Hint, wrong) {
				t.Errorf("%s: the hint says %q, which was not set: %q", name, wrong, out.Hint)
			}
		}
		if !strings.Contains(resultText(result), out.Hint) {
			t.Errorf("%s: the text a model reads does not carry the hint: %q", name, resultText(result))
		}
	}
}

// A filtered page past a cursor with nothing the filters select beyond it says
// the listing ends there. It does not blame the filters, which selected what
// the pages before it listed, and it does not send the model to
// includeSuperseded when what lies past the cursor is replaced records the
// filters exclude.
//
// Records do lie past the cursor here, one current and one replaced, and neither
// carries the tag. That is why the hint names the filter: "nothing lies past
// this cursor" would be false, and a check for history that dropped the filter
// would find the replaced one and point at a page that still lists nothing.
func TestAFilteredPageAfterACursorSaysTheListingEndsThere(t *testing.T) {
	session, _ := serve(t)

	gauge := storeMemory(t, session, mcp.RememberIn{
		Statement: "The tide gauge at the west pier reads a metre high at spring tide.",
		Context:   "Written first and never tagged harbour, so it lies past the cursor unselected.",
		Type:      "fact",
		Tags:      []string{"tide", "gauge"},
	})
	time.Sleep(2 * time.Millisecond)
	table := storeMemory(t, session, mcp.RememberIn{
		Statement: "The tide table is published on the first of each month.",
		Context:   "Written second and replaced later, so a replaced record the filter excludes lies past the cursor.",
		Type:      "fact",
		Tags:      []string{"tide", "table"},
	})
	time.Sleep(2 * time.Millisecond)
	survey := storeMemory(t, session, mcp.RememberIn{
		Statement: "The harbour survey found silting at the east approach.",
		Context:   "The second record carrying harbour, forgotten before the next page is asked for.",
		Type:      "fact",
		Tags:      []string{"harbour", "survey"},
	})
	time.Sleep(2 * time.Millisecond)
	dredging := storeMemory(t, session, mcp.RememberIn{
		Statement: "The east approach needs dredging before the autumn tides.",
		Context:   "The newest record carrying harbour, so a filtered page of one holds it.",
		Type:      "insight",
		Tags:      []string{"harbour", "dredging"},
	})

	harbour := []string{"harbour"}
	var first mcp.BrowseOut
	call(t, session, "browse", mcp.BrowseIn{Tags: harbour, Limit: 1}, &first)
	if len(first.Rows) != 1 || first.Rows[0].URI != dredging || first.NextCursor == "" {
		t.Fatalf("a filtered limit of 1 over two records returned %+v and cursor %q", first.Rows, first.NextCursor)
	}

	// The last record the filter selects past the cursor is forgotten, and the
	// record past it the filter excludes is replaced by one written after the
	// cursor's row.
	var forgotten mcp.ForgetOut
	call(t, session, "forget", mcp.ForgetIn{URI: survey, Confirm: true}, &forgotten)
	time.Sleep(2 * time.Millisecond)
	storeMemory(t, session, mcp.RememberIn{
		Statement:  "The tide table is published on the last working day of each month.",
		Context:    "The harbour office moved publication so the table is out before the month starts.",
		Type:       "fact",
		Tags:       []string{"tide", "table"},
		Supersedes: table,
	})

	for _, includeSuperseded := range []bool{false, true} {
		var past mcp.BrowseOut
		result := call(t, session, "browse", mcp.BrowseIn{
			Tags: harbour, Cursor: first.NextCursor, IncludeSuperseded: includeSuperseded,
		}, &past)
		if len(past.Rows) != 0 {
			t.Fatalf("includeSuperseded %t: the filtered page past the cursor listed %+v", includeSuperseded, past.Rows)
		}
		for _, must := range []string{
			`Nothing past this cursor matches tags ["harbour"], so the listing ends here.`,
			"`browse` without a cursor",
		} {
			if !strings.Contains(past.Hint, must) {
				t.Errorf("includeSuperseded %t: the hint does not say %q: %q", includeSuperseded, must, past.Hint)
			}
		}
		// The filters are not why this page is empty, and nothing they select
		// past the cursor was replaced.
		for _, wrong := range []string{"EXCLUDES", "Drop", "drop", "`recall`", "includeSuperseded", "holds nothing"} {
			if strings.Contains(past.Hint, wrong) {
				t.Errorf("includeSuperseded %t: the hint says %q: %q", includeSuperseded, wrong, past.Hint)
			}
		}
		if !strings.Contains(resultText(result), past.Hint) {
			t.Errorf("includeSuperseded %t: the text a model reads does not carry the hint: %q",
				includeSuperseded, resultText(result))
		}
	}

	// Past the same cursor without the filter, the records the hint had to
	// account for are there: the gauge reading, and the replaced table with
	// history included.
	var unfiltered mcp.BrowseOut
	call(t, session, "browse", mcp.BrowseIn{Cursor: first.NextCursor, IncludeSuperseded: true}, &unfiltered)
	if len(unfiltered.Rows) != 2 || unfiltered.Rows[0].URI != table || !unfiltered.Rows[0].Superseded ||
		unfiltered.Rows[1].URI != gauge {
		t.Fatalf("past the cursor without the filter listed %+v, want the replaced table and the gauge", unfiltered.Rows)
	}

	// Following the hint lists what the filter selects from the newest.
	var again mcp.BrowseOut
	call(t, session, "browse", mcp.BrowseIn{Tags: harbour}, &again)
	if len(again.Rows) != 1 || again.Rows[0].URI != dredging {
		t.Errorf("a filtered listing from the start returned %+v, want %s", again.Rows, dredging)
	}
}

// A filtered page past a cursor where everything the filters select has been
// replaced says so, names the filters, and points at includeSuperseded with the
// same cursor and filters. Following it lists the replaced record.
func TestAFilteredPageOfOnlyReplacedRecordsPointsAtIncludeSuperseded(t *testing.T) {
	session, _ := serve(t)

	schedule := storeMemory(t, session, mcp.RememberIn{
		Statement: "The harbour dredger works the east approach on Mondays.",
		Context:   "Written first, so it is the oldest record carrying harbour and lies past the first page.",
		Type:      "fact",
		Tags:      []string{"harbour", "dredging"},
	})
	time.Sleep(2 * time.Millisecond)
	survey := storeMemory(t, session, mcp.RememberIn{
		Statement: "The harbour survey found silting at the east approach.",
		Context:   "Written second, so a filtered page of one holds it and nothing else.",
		Type:      "fact",
		Tags:      []string{"harbour", "survey"},
	})

	harbour := []string{"harbour"}
	var first mcp.BrowseOut
	call(t, session, "browse", mcp.BrowseIn{Tags: harbour, Limit: 1}, &first)
	if len(first.Rows) != 1 || first.Rows[0].URI != survey || first.NextCursor == "" {
		t.Fatalf("a filtered limit of 1 over two records returned %+v and cursor %q", first.Rows, first.NextCursor)
	}

	// The dredging schedule changes while the listing is being paged, so the one
	// record the filter selects past the cursor is now a replaced one.
	time.Sleep(2 * time.Millisecond)
	storeMemory(t, session, mcp.RememberIn{
		Statement:  "The harbour dredger works the east approach on Mondays and Thursdays.",
		Context:    "The silting found by the survey needed a second day of dredging each week.",
		Type:       "fact",
		Tags:       []string{"harbour", "dredging"},
		Supersedes: schedule,
	})

	var past mcp.BrowseOut
	result := call(t, session, "browse", mcp.BrowseIn{Tags: harbour, Cursor: first.NextCursor}, &past)
	if len(past.Rows) != 0 {
		t.Fatalf("the filtered page past the cursor listed %+v, want nothing current", past.Rows)
	}
	for _, must := range []string{
		`Every record past this cursor that matches tags ["harbour"] has been replaced`,
		"the same cursor and filters with includeSuperseded set",
	} {
		if !strings.Contains(past.Hint, must) {
			t.Errorf("the hint does not say %q: %q", must, past.Hint)
		}
	}
	for _, wrong := range []string{"EXCLUDES", "Drop", "drop", "`recall`", "ends here", "holds nothing"} {
		if strings.Contains(past.Hint, wrong) {
			t.Errorf("a filtered page of replaced records says %q: %q", wrong, past.Hint)
		}
	}
	if !strings.Contains(resultText(result), past.Hint) {
		t.Errorf("the text a model reads does not carry the hint: %q", resultText(result))
	}

	var history mcp.BrowseOut
	call(t, session, "browse", mcp.BrowseIn{Tags: harbour, Cursor: first.NextCursor, IncludeSuperseded: true}, &history)
	if len(history.Rows) != 1 || history.Rows[0].URI != schedule || !history.Rows[0].Superseded {
		t.Errorf("the same cursor and filter with history listed %+v, want the replaced schedule", history.Rows)
	}
}

// The browse description's advice to try recall is for a browse that set the
// filters recall treats as a preference. An empty browse with no filter set has
// another cause, and the description says the hint names it rather than sending
// the model to recall for a filter it never set.
func TestBrowseDescriptionGivesRecallAdviceOnlyWhenFiltersWereSet(t *testing.T) {
	text := strings.Join(strings.Fields(mcp.BrowseTool.Description), " ")
	if strings.Contains(text, "If a browse comes back empty, try recall") {
		t.Error("the browse description still sends every empty browse to recall")
	}

	var advice string
	for _, sentence := range strings.SplitAfter(text, ". ") {
		if strings.Contains(sentence, "try recall") {
			advice = sentence
		}
	}
	if advice == "" {
		t.Fatal("the browse description no longer tells the agent to try recall after a filtered browse")
	}
	for _, must := range []string{"tags", "types"} {
		if !strings.Contains(advice, must) {
			t.Errorf("the recall advice does not say it applies when %s were set: %q", must, advice)
		}
	}
	if !strings.Contains(text, "no filter set") || !strings.Contains(text, "hint") {
		t.Error("the browse description does not say an unfiltered empty page carries its own reason")
	}
}
