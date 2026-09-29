package mcp_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/steven3002/sennit/mcp"
)

// resumeWith runs the prompt with its arguments filled the way a host would
// fill them, and returns what it resumed.
func resumeWith(t *testing.T, session *sdk.ClientSession, arguments map[string]string) *sdk.GetPromptResult {
	t.Helper()
	got, err := session.GetPrompt(context.Background(), &sdk.GetPromptParams{
		Name: mcp.ResumePrompt, Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("resume %v: %v", arguments, err)
	}
	return got
}

// refusal runs the prompt with arguments it should refuse, and returns the
// refusal as the user is shown it, which is word for word.
func refusal(t *testing.T, session *sdk.ClientSession, arguments map[string]string) string {
	t.Helper()
	got, err := session.GetPrompt(context.Background(), &sdk.GetPromptParams{
		Name: mcp.ResumePrompt, Arguments: arguments,
	})
	if err == nil {
		t.Fatalf("resume %v resumed %q, want a refusal", arguments, got.Description)
	}
	var wire *jsonrpc.Error
	if !errors.As(err, &wire) {
		t.Fatalf("resume %v failed with %v rather than with a refusal", arguments, err)
	}
	return wire.Message
}

// saveHarbourAndRollup stores two conversations, the second longer than a
// resume replays at most and saved last, so it is the one recent resumes and a
// count up to the ceiling shows in how many of its turns come back. It returns
// both addresses and the second one's length.
func saveHarbourAndRollup(t *testing.T, session *sdk.ClientSession) (harbour, rollup string, turns int) {
	t.Helper()
	turns = mcp.MaxResumeTurns + 5
	saved := saveInOrder(t, session,
		mcp.SaveSessionIn{
			Title:    "Harbour survey",
			Summary:  "Surveyed the harbour approaches and found silting at the east.",
			Messages: conversation("harbour"),
		},
		mcp.SaveSessionIn{
			Title:    "Rollup schedule",
			Summary:  "Settled that the rollup runs hourly at ten past.",
			Messages: datedConversation("rollup", turns, func(int) bool { return false }),
		},
	)
	return saved[0], saved[1], turns
}

// idOf is a conversation's id alone, as `sennit recall --memory-text` shows it.
func idOf(uri string) string { return strings.TrimPrefix(uri, mcp.Scheme+"session/") }

// A number after recent, an address or an id is how many turns to replay.
//
// A host that fills arguments in order puts the word after the first one in
// topic, and after any of the three nothing is searched for, so topic is where
// the count is read from. Read only from turns, /mcp__sennit__resume recent 10
// replayed the default thirty.
func TestANumberAfterRecentAnAddressOrAnIdIsHowManyTurnsToReplay(t *testing.T) {
	session, _ := serve(t)
	_, rollup, n := saveHarbourAndRollup(t, session)
	ceiling := strconv.Itoa(mcp.MaxResumeTurns)

	for _, c := range []struct {
		typed string            // what the user typed after the command
		words map[string]string // where the host puts it
		turns int               // how many turns come back
		how   string            // how the conversation was chosen
	}{
		{"recent 10", map[string]string{"session": "recent", "topic": "10"},
			10, "the most recent conversation"},
		{"its address and 10", map[string]string{"session": rollup, "topic": "10"}, 10, "by address"},
		{"its id and 10", map[string]string{"session": idOf(rollup), "topic": "10"}, 10, "by id"},
		// The ceiling is a count a resume replays.
		{"recent " + ceiling, map[string]string{"session": "recent", "topic": ceiling},
			mcp.MaxResumeTurns, "the most recent conversation"},
		// With nothing after it, the default.
		{"recent", map[string]string{"session": "recent"},
			mcp.ResumeTurns, "the most recent conversation"},
		// A host that fills arguments by name puts the count in turns, and it
		// is read from there as before.
		{"recent, with 10 in turns by name", map[string]string{"session": "recent", "turns": "10"},
			10, "the most recent conversation"},
	} {
		t.Run(c.typed, func(t *testing.T) {
			got := resumeWith(t, session, c.words)
			want := fmt.Sprintf("Resuming %q, %d of %d turn(s), %s", "Rollup schedule", c.turns, n, c.how)
			if got.Description != want {
				t.Errorf("resume %s: %q, want %q", c.typed, got.Description, want)
			}
		})
	}
}

// Any other word after recent, an address or an id is refused, and the refusal
// says what to run instead.
//
// Nothing after those three is searched for, so a word there changes nothing.
// Dropped without a word, /mcp__sennit__resume recent harbour resumed the newest
// conversation, which need not be about harbours, and left the user believing
// the word had been used.
func TestAnyOtherWordAfterRecentAnAddressOrAnIdIsRefusedRatherThanDropped(t *testing.T) {
	session, _ := serve(t)
	harbour, rollup, _ := saveHarbourAndRollup(t, session)
	id := idOf(rollup)

	for _, c := range []struct {
		typed string
		words map[string]string
		want  string
	}{
		{"recent harbour", map[string]string{"session": "recent", "topic": "harbour"},
			`nothing was resumed, because only a number of turns can follow recent, and "harbour" is ` +
				`not one. Run /mcp__sennit__resume recent to resume the most recent conversation, or ` +
				`leave recent out to search for one by what it was about`},
		// The command it names is the one that works, whatever case the
		// reserved word was typed in.
		{"RECENT harbour", map[string]string{"session": "RECENT", "topic": "harbour"},
			`nothing was resumed, because only a number of turns can follow recent, and "harbour" is ` +
				`not one. Run /mcp__sennit__resume recent to resume the most recent conversation, or ` +
				`leave recent out to search for one by what it was about`},
		// The first word that is not a count is the one refused.
		{"recent harbour survey",
			map[string]string{"session": "recent", "topic": "harbour", "turns": "survey"},
			`nothing was resumed, because only a number of turns can follow recent, and "harbour" is ` +
				`not one. Run /mcp__sennit__resume recent to resume the most recent conversation, or ` +
				`leave recent out to search for one by what it was about`},
		{"its address and harbour", map[string]string{"session": rollup, "topic": "harbour"},
			`nothing was resumed, because only a number of turns can follow an address, and "harbour" ` +
				`is not one. Run /mcp__sennit__resume ` + rollup + ` to resume that conversation`},
		{"its id and harbour", map[string]string{"session": id, "topic": "harbour"},
			`nothing was resumed, because only a number of turns can follow an id, and "harbour" is not ` +
				`one. Run /mcp__sennit__resume ` + id + ` to resume that conversation`},
		// A count may follow, and nothing after it.
		{"recent 10 harbour", map[string]string{"session": "recent", "topic": "10", "turns": "harbour"},
			`nothing was resumed, because only a number of turns can follow recent, and "harbour" came ` +
				`after "10". Run /mcp__sennit__resume recent 10 to resume the most recent conversation`},
		{"its address, 10 and 20", map[string]string{"session": rollup, "topic": "10", "turns": "20"},
			`nothing was resumed, because only a number of turns can follow an address, and "20" came ` +
				`after "10". Run /mcp__sennit__resume ` + rollup + ` 10 to resume that conversation`},
		// A host that fills arguments by name can send a whole phrase after
		// recent, and it is refused the same way.
		{"recent, with harbour survey in topic by name",
			map[string]string{"session": "recent", "topic": "harbour survey"},
			`nothing was resumed, because only a number of turns can follow recent, and "harbour ` +
				`survey" is not one. Run /mcp__sennit__resume recent to resume the most recent ` +
				`conversation, or leave recent out to search for one by what it was about`},
		// There a word in turns is refused as before, and the refusal now says
		// how large a count can be.
		{"recent, with survey in turns by name", map[string]string{"session": "recent", "turns": "survey"},
			`nothing was resumed, because turns must be a positive whole number up to ` +
				strconv.Itoa(mcp.MaxResumeTurns) + `, not "survey"`},
	} {
		t.Run(c.typed, func(t *testing.T) {
			if got := refusal(t, session, c.words); got != c.want {
				t.Errorf("resume %s was refused with\n%q\nwant\n%q", c.typed, got, c.want)
			}
		})
	}

	// What the refusal of recent harbour suggests does what it says: without
	// recent, the word is searched for.
	got := resumeWith(t, session, map[string]string{"session": "harbour"})
	if head, _ := got.Messages[1].Content.(*sdk.EmbeddedResource); head == nil || head.Resource.URI != harbour {
		t.Errorf("resume harbour resumed %q, want %s", got.Description, harbour)
	}
}

// A resume replays at most MaxResumeTurns turns, the arguments say so, and a
// larger count is refused rather than replayed or cut down.
//
// Without a ceiling, a year typed as the last word of a topic was read as the
// count and replayed the whole conversation. Where a topic is being searched
// for, the refusal says how to search for the number as part of it instead.
func TestACountAboveTheCeilingIsRefusedRatherThanReplayed(t *testing.T) {
	session, _ := serve(t)
	_, rollup, n := saveHarbourAndRollup(t, session)
	ceiling := strconv.Itoa(mcp.MaxResumeTurns)
	above := strconv.Itoa(mcp.MaxResumeTurns + 1)

	listed, err := session.ListPrompts(context.Background(), nil)
	if err != nil {
		t.Fatalf("list prompts: %v", err)
	}
	described := map[string]string{}
	for _, argument := range listed.Prompts[0].Arguments {
		described[argument.Name] = argument.Description
	}
	for name, says := range map[string]string{
		"session": "sennit://session/{id}, that {id} alone, or recent",
		"topic": "After an address, an id or recent in session, only the number of turns to replay " +
			"can go here",
		"turns": "a positive whole number up to " + ceiling + ".",
	} {
		if !strings.Contains(described[name], says) {
			t.Errorf("the description of %s does not say %q: %q", name, says, described[name])
		}
	}
	if !strings.Contains(described["turns"], "outside that range is refused") {
		t.Errorf("the description of turns does not say what happens past the ceiling: %q", described["turns"])
	}

	// The ceiling itself is replayed.
	got := resumeWith(t, session, map[string]string{"session": "rollup", "topic": "schedule", "turns": ceiling})
	if want := fmt.Sprintf("%d of %d turn(s)", mcp.MaxResumeTurns, n); !strings.Contains(got.Description, want) {
		t.Errorf("resume rollup schedule %s replayed %q, want %s", ceiling, got.Description, want)
	}

	named := func(number string) string {
		return `nothing was resumed, because "` + number + `" was read as the number of turns to ` +
			`replay, which must be a positive whole number up to ` + ceiling + `, or left out for the ` +
			`default of ` + strconv.Itoa(mcp.ResumeTurns)
	}
	searched := func(number string) string {
		return named(number) + `. To search for "` + number + `" as part of the topic instead, put it ` +
			`among the topic's other words rather than after them`
	}
	for _, c := range []struct {
		typed string
		words map[string]string
		want  string
	}{
		{"rollup schedule 2024",
			map[string]string{"session": "rollup", "topic": "schedule", "turns": "2024"}, searched("2024")},
		{"rollup schedule " + above,
			map[string]string{"session": "rollup", "topic": "schedule", "turns": above}, searched(above)},
		// Too large for an int is too large to replay, and is still a number.
		{"rollup schedule 99999999999999999999",
			map[string]string{"session": "rollup", "topic": "schedule", "turns": "99999999999999999999"},
			searched("99999999999999999999")},
		// A topic given by name is searched for all the same.
		{"rollup schedule, with 2024 in turns by name",
			map[string]string{"topic": "rollup schedule", "turns": "2024"}, searched("2024")},
		// After recent, an address or an id there is no topic to search.
		{"recent " + above, map[string]string{"session": "recent", "topic": above}, named(above)},
		{"its address and 2024", map[string]string{"session": rollup, "topic": "2024"}, named("2024")},
		{"its id and 2024", map[string]string{"session": idOf(rollup), "topic": "2024"}, named("2024")},
		// Nor with nothing named, where the list would otherwise be offered and
		// the count kept for whichever conversation the user chose.
		{"2024 in turns by name, and nothing else", map[string]string{"turns": "2024"}, named("2024")},
	} {
		t.Run(c.typed, func(t *testing.T) {
			if got := refusal(t, session, c.words); got != c.want {
				t.Errorf("resume %s was refused with\n%q\nwant\n%q", c.typed, got, c.want)
			}
		})
	}

	// What the refusal suggests does what it says: earlier among the words,
	// the year is searched for, and the default number of turns comes back.
	got = resumeWith(t, session, map[string]string{"session": "rollup", "topic": "2024", "turns": "schedule"})
	if want := fmt.Sprintf(`Resuming "Rollup schedule", %d of %d turn(s), the closest match for %q,`,
		mcp.ResumeTurns, n, "rollup 2024 schedule"); !strings.HasPrefix(got.Description, want) {
		t.Errorf("resume rollup 2024 schedule: %q, want it to start %q", got.Description, want)
	}
}

// Zero or a negative number where a count goes is refused, not searched for.
//
// It is a whole number in the count's place, so it is read as a count, and it
// is none a resume can replay. Taken as a word of the topic instead, a count
// the user got wrong, a 0 for all of them or a -5 for the last five, was
// searched for, and the default number of turns replayed without a word.
func TestZeroOrANegativeCountIsRefusedRatherThanSearchedFor(t *testing.T) {
	session, _ := serve(t)
	saveHarbourAndRollup(t, session)
	ceiling := strconv.Itoa(mcp.MaxResumeTurns)

	for _, number := range []string{"0", "-5", "-0"} {
		named := `nothing was resumed, because "` + number + `" was read as the number of turns to ` +
			`replay, which must be a positive whole number up to ` + ceiling + `, or left out for the ` +
			`default of ` + strconv.Itoa(mcp.ResumeTurns)
		searched := named + `. To search for "` + number + `" as part of the topic instead, put it ` +
			`among the topic's other words rather than after them`
		for _, c := range []struct {
			typed string
			words map[string]string
			want  string
		}{
			{"rollup schedule " + number,
				map[string]string{"session": "rollup", "topic": "schedule", "turns": number}, searched},
			{"recent " + number, map[string]string{"session": "recent", "topic": number}, named},
			{number + " in turns by name, and nothing else", map[string]string{"turns": number}, named},
		} {
			t.Run(c.typed, func(t *testing.T) {
				if got := refusal(t, session, c.words); got != c.want {
					t.Errorf("resume %s was refused with\n%q\nwant\n%q", c.typed, got, c.want)
				}
			})
		}
	}
}

// A conversation's id typed alone resumes that conversation, as its address
// does, rather than being searched for as a word of a topic.
//
// It is what `sennit recall --memory-text` shows beside each result. Searched
// for as a topic, it resumed whichever conversation lay nearest to a string of
// hex digits, which need not be the one it names.
func TestAnIdAloneResumesTheConversationItNames(t *testing.T) {
	session, _ := serve(t)
	harbour, _, _ := saveHarbourAndRollup(t, session)
	id := idOf(harbour)

	// The older of the two, so the newest is not what comes back by accident,
	// and in either case, since hex digits are read in both.
	for _, typed := range []string{id, strings.ToUpper(id), " " + id + " "} {
		got := resumeWith(t, session, map[string]string{"session": typed})
		if want := `Resuming "Harbour survey", 4 of 4 turn(s), by id`; got.Description != want {
			t.Errorf("resume %s: %q, want %q", typed, got.Description, want)
		}
		if head, _ := got.Messages[1].Content.(*sdk.EmbeddedResource); head == nil || head.Resource.URI != harbour {
			t.Errorf("resume %s embedded a head other than %s", typed, harbour)
		}
		framing, _ := got.Messages[0].Content.(*sdk.TextContent)
		if framing == nil || !strings.Contains(framing.Text, "How it was chosen: by id.\n") {
			t.Errorf("resume %s does not tell the model it was chosen by id", typed)
		}
	}

	// An id that names no conversation here, a memory's among them, is refused
	// as its address would be, rather than searched for.
	memory := strings.TrimPrefix(storeMemory(t, session, mcp.RememberIn{
		Statement: "The harbour survey found silting at the eastern approach.",
		Context:   "Recorded from the harbour survey conversation.",
		Type:      "fact",
		Tags:      []string{"harbour", "survey"},
	}), mcp.Scheme+"memory/")
	for _, unknown := range []string{memory, strings.Repeat("0", 32)} {
		want := "no record at that address: " + mcp.Scheme + "session/" + unknown
		if got := refusal(t, session, map[string]string{"session": unknown}); got != want {
			t.Errorf("resume %s was refused with %q, want %q", unknown, got, want)
		}
	}
}
