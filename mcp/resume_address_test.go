package mcp_test

import (
	"fmt"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/steven3002/sennit/mcp"
)

// notRestoredHere is what a resume adds, on a device that has not restored any
// record of its vault, to an answer that found no conversation there.
//
// Every vault these tests open is such a device. It is offline, so nothing it
// holds has reached the network and been catalogued, and nothing has been
// restored to it. A vault just created looks the same until its first flush, so
// what it says has to be true of that vault too.
const notRestoredHere = ", and it has not restored any record of this vault from the network, so this " +
	"does not show that the vault has none. If this vault was just created here, there is nothing to " +
	"restore; otherwise `sennit hydrate` restores its records from the network, conversations included"

// resumeInstead is what a resume of an address or an id that names no
// conversation here says to run instead.
const resumeInstead = "Run /mcp__sennit__resume alone to choose from the recent conversations, or with " +
	"what the conversation was about to search for it"

// memoryRefusal is what a resume says to a memory's id, given the memory's
// address.
func memoryRefusal(memory string) string {
	return "nothing was resumed, because " + idOfMemory(memory) + " is the id of a memory, not of a " +
		"conversation, and a memory cannot be resumed. Ask your agent to read it with `open` on " + memory
}

// idOfMemory is a memory's id alone.
func idOfMemory(uri string) string { return strings.TrimPrefix(uri, mcp.Scheme+"memory/") }

// An address or an id that names no conversation here is refused with what to
// run instead, and on a device that has not restored its vault, with the
// command that restores it.
//
// It was refused with "no record at that address" and the address, which said
// neither what to type instead nor that the conversation could be one this
// device has not restored.
func TestAnAddressOrIdThatNamesNoConversationSaysWhatToRunInstead(t *testing.T) {
	session, _ := serve(t)
	harbour, _, _ := saveHarbourAndRollup(t, session)
	unknown := strings.Repeat("0", 32)
	address := mcp.Scheme + "session/" + unknown
	want := "nothing was resumed, because this device holds no conversation at " + address +
		notRestoredHere + ". " + resumeInstead

	for _, c := range []struct {
		typed string            // what the user typed after the command
		words map[string]string // where the host puts it
	}{
		{"its address", map[string]string{"session": address}},
		{"its id", map[string]string{"session": unknown}},
		// A count after it is read before the conversation is looked for, and
		// changes nothing here.
		{"its id and 10", map[string]string{"session": unknown, "topic": "10"}},
		// However the id is typed, it is looked for, and not searched for.
		{"its id in quotes", map[string]string{"session": `"` + unknown + `"`}},
		{"its address without the scheme", map[string]string{"session": "session/" + unknown}},
	} {
		t.Run(c.typed, func(t *testing.T) {
			if got := refusal(t, session, c.words); got != want {
				t.Errorf("resume %s was refused with\n%q\nwant\n%q", c.typed, got, want)
			}
		})
	}

	// What the refusal says to run does what it says. Alone, the command offers
	// the conversations to choose from: this client cannot show a dialog, so
	// they come as a list, which gives each one's address.
	listed := resumeWith(t, session, nil)
	if text, _ := listed.Messages[0].Content.(*sdk.TextContent); text == nil || !strings.Contains(text.Text, harbour) {
		t.Errorf("resume alone did not offer %s to choose: %q", harbour, listed.Description)
	}
	// With what the conversation was about, it is searched for.
	found := resumeWith(t, session, map[string]string{"session": "harbour", "topic": "survey"})
	if head, _ := found.Messages[1].Content.(*sdk.EmbeddedResource); head == nil || head.Resource.URI != harbour {
		t.Errorf("resume harbour survey resumed %q, want %s", found.Description, harbour)
	}

	// On a device that holds no conversation at all, neither would find one, so
	// the id is answered as a resume that names nothing is, which says what
	// this device has not restored.
	empty := connect(t, mcp.New(openVault(t, t.TempDir())))
	for _, typed := range []string{unknown, address} {
		got, alone := refusal(t, empty, map[string]string{"session": typed}), refusal(t, empty, nil)
		if got != alone {
			t.Errorf("on a device holding no conversation, resume %s was refused with\n%q\nwant what "+
				"resume alone is refused with\n%q", typed, got, alone)
		}
	}
}

// A memory's id is refused as a memory's, and the refusal says how to read it
// instead, however the id is given: on its own, in a conversation's address,
// or in the memory's own address.
//
// A memory cannot be resumed, because it is one statement and not a
// conversation. Its id was refused as one that names nothing, "no record at
// that address", while this device held the memory, and its address as one
// that "wants a session address", which sent the user to the first refusal.
func TestAMemorysIdIsRefusedAsAMemorysAndSaysHowToReadIt(t *testing.T) {
	session, _ := serve(t)
	saveHarbourAndRollup(t, session)
	memory := storeMemory(t, session, mcp.RememberIn{
		Statement: "The harbour survey found silting at the eastern approach.",
		Context:   "Recorded from the harbour survey conversation.",
		Type:      "fact",
		Tags:      []string{"harbour", "survey"},
	})
	id := idOfMemory(memory)
	want := memoryRefusal(memory)

	for _, c := range []struct {
		typed string
		words map[string]string
	}{
		{"its id", map[string]string{"session": id}},
		{"its id in capitals", map[string]string{"session": strings.ToUpper(id)}},
		{"its id in quotes", map[string]string{"session": `"` + id + `"`}},
		{"a conversation's address with its id", map[string]string{"session": mcp.Scheme + "session/" + id}},
		{"that address without the scheme", map[string]string{"session": "session/" + id}},
		{"its own address", map[string]string{"session": memory}},
		{"its own address without the scheme", map[string]string{"session": "memory/" + id}},
		// Refused before any word after it is read, as a malformed address is.
		{"its own address and a word", map[string]string{"session": memory, "topic": "harbour"}},
	} {
		t.Run(c.typed, func(t *testing.T) {
			if got := refusal(t, session, c.words); got != want {
				t.Errorf("resume %s was refused with\n%q\nwant\n%q", c.typed, got, want)
			}
		})
	}

	// What the refusal says to do reads the memory.
	var opened mcp.OpenOut
	call(t, session, "open", mcp.OpenIn{URI: memory}, &opened)
	if !strings.Contains(opened.Content, "found silting at the eastern approach") {
		t.Errorf("open on %s did not read the memory: %q", memory, opened.Content)
	}

	// A memory's id is the memory's on a device that holds no conversation too,
	// rather than one more id that names nothing there.
	only := connect(t, mcp.New(openVault(t, t.TempDir())))
	alone := storeMemory(t, only, mcp.RememberIn{
		Statement: "The rollup runs hourly at ten past.",
		Context:   "Settled while planning the rollup schedule.",
		Type:      "fact",
		Tags:      []string{"rollup", "schedule"},
	})
	if got, want := refusal(t, only, map[string]string{"session": idOfMemory(alone)}), memoryRefusal(alone); got != want {
		t.Errorf("on a device holding no conversation, a memory's id was refused with\n%q\nwant\n%q", got, want)
	}
}

// An address or an id in quotes, and an address typed without sennit://,
// resume the conversation they name, as the address does.
//
// Claude Code passes quotes on as part of the words, measured in 2.1.283, so
// an address put in quotes arrives with them. An address copied without its
// scheme arrives as session/ and the id. Both were searched for as a topic,
// which resumes whichever conversation lies nearest to the string, and that
// need not be the one it names.
func TestAQuotedAddressOrIdAndAnAddressWithoutItsSchemeResumeTheirConversation(t *testing.T) {
	session, _ := serve(t)
	harbour, _, _ := saveHarbourAndRollup(t, session)
	id := idOf(harbour)

	// The older of the two, so the newest is not what comes back by accident.
	for _, c := range []struct {
		typed string
		words map[string]string
		how   string // how the conversation was chosen
		turns int    // how many of its four turns come back
	}{
		{"its address in double quotes", map[string]string{"session": `"` + harbour + `"`}, "by address", 4},
		{"its address in single quotes", map[string]string{"session": "'" + harbour + "'"}, "by address", 4},
		{"its address in backticks", map[string]string{"session": "`" + harbour + "`"}, "by address", 4},
		{"its address in curly quotes", map[string]string{"session": "“" + harbour + "”"}, "by address", 4},
		{"its id in double quotes", map[string]string{"session": `"` + id + `"`}, "by id", 4},
		{"its id in single quotes", map[string]string{"session": "'" + id + "'"}, "by id", 4},
		{"its address without the scheme", map[string]string{"session": "session/" + id}, "by address", 4},
		{"its address without the scheme, in quotes", map[string]string{"session": `"session/` + id + `"`},
			"by address", 4},
		{"its address without the scheme, the id in capitals",
			map[string]string{"session": "session/" + strings.ToUpper(id)}, "by address", 4},
		// A count may follow either, as it may follow the address.
		{"its address in quotes and 2", map[string]string{"session": `"` + harbour + `"`, "topic": "2"},
			"by address", 2},
		{"its address without the scheme and 2", map[string]string{"session": "session/" + id, "topic": "2"},
			"by address", 2},
	} {
		t.Run(c.typed, func(t *testing.T) {
			got := resumeWith(t, session, c.words)
			if want := fmt.Sprintf(`Resuming "Harbour survey", %d of 4 turn(s), %s`, c.turns, c.how); got.Description != want {
				t.Errorf("resume %s: %q, want %q", c.typed, got.Description, want)
			}
			if head, _ := got.Messages[1].Content.(*sdk.EmbeddedResource); head == nil || head.Resource.URI != harbour {
				t.Errorf("resume %s embedded a head other than %s", c.typed, harbour)
			}
		})
	}

	// Any other word after either is refused as one after the address is, and
	// the command the refusal names is the address as typed, without quotes.
	for _, c := range []struct {
		typed, again string
		words        map[string]string
	}{
		{"its address in quotes and harbour", harbour,
			map[string]string{"session": `"` + harbour + `"`, "topic": "harbour"}},
		{"its address without the scheme and harbour", "session/" + id,
			map[string]string{"session": "session/" + id, "topic": "harbour"}},
	} {
		t.Run(c.typed, func(t *testing.T) {
			want := `nothing was resumed, because only a number of turns can follow an address, and ` +
				`"harbour" is not one. Run /mcp__sennit__resume ` + c.again + ` to resume that conversation`
			if got := refusal(t, session, c.words); got != want {
				t.Errorf("resume %s was refused with\n%q\nwant\n%q", c.typed, got, want)
			}
		})
	}
}

// An address typed without its scheme is the address, so one that does not
// name a conversation is refused as the address would be, rather than searched
// for as a topic.
func TestAnAddressWithoutItsSchemeIsRefusedAsTheAddressWouldBe(t *testing.T) {
	session, _ := serve(t)
	harbour, _, _ := saveHarbourAndRollup(t, session)
	id := idOf(harbour)

	for _, word := range []string{
		"session/" + id + "/transcript",
		"session/" + id + "/turns",
		// The class in capitals is still meant as an address, as the scheme in
		// capitals is.
		"Session/" + id,
	} {
		t.Run(word, func(t *testing.T) {
			// With a word after it, so a refusal is not a search that happened
			// to fail.
			got := refusal(t, session, map[string]string{"session": word, "topic": "harbour"})
			want := refusal(t, session, map[string]string{"session": mcp.Scheme + word, "topic": "harbour"})
			if got != want {
				t.Errorf("resume %s was refused with\n%q\nwant what its address is refused with\n%q", word, got, want)
			}
		})
	}
}

// A topic that only looks a little like an address is still searched for.
//
// An address is read without its scheme only when what is left is one: a class
// of record an address names, a slash and a whole id. A topic can hold the word
// session, a slash, a run of hex digits short of an id, or quotes, and each of
// those is a word of the topic. Quotes are taken off only to read an address or
// an id, so a topic in quotes is searched for with them, as it was.
func TestATopicThatLooksALittleLikeAnAddressIsStillSearchedFor(t *testing.T) {
	session, _ := serve(t)
	harbour, _, _ := saveHarbourAndRollup(t, session)
	id := idOf(harbour)

	for _, c := range []struct {
		typed    string            // what the user typed after the command
		words    map[string]string // where the host puts it
		searched string            // the words that should be searched for
	}{
		{"session harbour", map[string]string{"session": "session", "topic": "harbour"}, "session harbour"},
		{"session/harbour", map[string]string{"session": "session/harbour"}, "session/harbour"},
		{"harbour/survey", map[string]string{"session": "harbour/survey"}, "harbour/survey"},
		{"memory/leak", map[string]string{"session": "memory/leak"}, "memory/leak"},
		{"session/ and a digit short of an id", map[string]string{"session": "session/" + id[:31]},
			"session/" + id[:31]},
		{"a phrase in quotes, by name", map[string]string{"session": `"harbour survey"`}, `"harbour survey"`},
		// Quoting the reserved word searches for it instead.
		{`"recent"`, map[string]string{"session": `"recent"`}, `"recent"`},
	} {
		t.Run(c.typed, func(t *testing.T) {
			got := resumeWith(t, session, c.words)
			if want := fmt.Sprintf("the closest match for %q,", c.searched); !strings.Contains(got.Description, want) {
				t.Errorf("resume %s was chosen as %q, want %s", c.typed, got.Description, want)
			}
		})
	}
}
