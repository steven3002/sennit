package mcp_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/steven3002/sennit/local"
	"github.com/steven3002/sennit/mcp"
	"github.com/steven3002/sennit/record"
	"github.com/steven3002/sennit/store/packer"
)

// Forgetting a memory a second time is answered as forgetting a conversation a
// second time is: it succeeds, says there was nothing at that address and that
// nothing was removed, and changes nothing.
//
// A retry after a dropped response is the ordinary way an agent forgets a record
// twice. A conversation already gone was answered this way, while a memory
// already gone failed with the catalog's own words, "no manifest entry for" its
// id, so the agent was told that the forget it had already made had failed.
func TestForgettingAMemoryTwiceIsAnsweredAsForgettingAConversationTwiceIs(t *testing.T) {
	session, _ := serve(t)

	memory := storeMemory(t, session, mcp.RememberIn{
		Statement: "The harbour gauge at Tidepool reads in centimetres.",
		Context:   "From the Tidepool field guide, written to be forgotten twice.",
		Type:      "fact",
		Tags:      []string{"harbour", "gauge"},
	})
	saved := saveConversation(t, session, mcp.SaveSessionIn{
		Title:    "Tidepool gauge survey",
		Summary:  "Went through which unit each Tidepool gauge reads in.",
		Messages: conversation("survey"),
	})
	for _, uri := range []string{memory, saved.URI} {
		var forgotten mcp.ForgetOut
		call(t, session, "forget", mcp.ForgetIn{URI: uri, Confirm: true}, &forgotten)
		if !forgotten.Removed {
			t.Fatalf("the first forget of %s reported removing nothing", uri)
		}
	}
	before := vaultCounts(t, session)

	memoryAgain := callRaw(t, session, "forget", mcp.ForgetIn{URI: memory, Confirm: true})
	if memoryAgain.IsError {
		t.Fatalf("forgetting the memory again failed: %s", resultText(memoryAgain))
	}
	conversationAgain := callRaw(t, session, "forget", mcp.ForgetIn{URI: saved.URI, Confirm: true})
	if conversationAgain.IsError {
		t.Fatalf("forgetting the conversation again failed: %s", resultText(conversationAgain))
	}
	var memoryOut, conversationOut mcp.ForgetOut
	decodeStructured(t, "forget", memoryAgain, &memoryOut)
	decodeStructured(t, "forget", conversationAgain, &conversationOut)
	if want := (mcp.ForgetOut{URI: memory, Note: "There was nothing at that address; nothing was removed."}); memoryOut != want {
		t.Errorf("forgetting the memory again answered %+v, want %+v", memoryOut, want)
	}
	if memoryOut.Removed != conversationOut.Removed || memoryOut.Note != conversationOut.Note {
		t.Errorf("forgetting the memory again answered %+v, and the conversation %+v", memoryOut, conversationOut)
	}
	// The text a model reads is the same answer too, each about its own address.
	if got, want := resultText(memoryAgain), strings.ReplaceAll(resultText(conversationAgain), saved.URI, memory); got != want {
		t.Errorf("forgetting the memory again reads %q, want %q", got, want)
	}

	after := vaultCounts(t, session)
	if after.Records != before.Records || after.Memories != before.Memories || after.Sessions != before.Sessions ||
		after.Pending != before.Pending || after.Indexed != before.Indexed {
		t.Errorf("forgetting again changed what the vault holds: %+v, then %+v", before, after)
	}
	if result := callRaw(t, session, "open", mcp.OpenIn{URI: memory}); !result.IsError {
		t.Errorf("the forgotten memory opens: %s", resultText(result))
	}
}

// A forget that meets a flush writing the memory says nothing was removed and to
// forget it again, and never that there was nothing there. The memory is
// untouched until the flush lets go, and forgetting it then removes it.
//
// The memory is not gone: the flush has its sealed payload in hand and
// catalogues it when it lands. So this refusal is not one a memory already gone
// is answered with, and it stays as the vault words it.
func TestAMemoryAFlushIsWritingIsRefusedAndNotSaidToBeGone(t *testing.T) {
	home := t.TempDir()
	v := openVault(t, home)
	session := connect(t, mcp.New(v))

	memory := storeMemory(t, session, mcp.RememberIn{
		Statement: "The harbour gauge at Tidepool reads in centimetres.",
		Context:   "From the Tidepool field guide, written while a flush was under way.",
		Type:      "fact",
		Tags:      []string{"harbour", "gauge"},
	})
	// A flush in another process over the same vault claims the queue.
	device, err := local.Open(filepath.Join(home, "vault.db"))
	if err != nil {
		t.Fatalf("open the device store as a second process would: %v", err)
	}
	defer device.Close()
	claimed, err := device.ClaimQueued("a flush under way", packer.DefaultClaimTimeout, 0)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim the queue: %d record(s), %v", len(claimed), err)
	}

	result := callRaw(t, session, "forget", mcp.ForgetIn{URI: memory, Confirm: true})
	if !result.IsError {
		t.Fatalf("a memory a flush is writing was answered as forgotten: %s", resultText(result))
	}
	for _, want := range []string{"nothing was removed", "forget it again once that flush has finished"} {
		if !strings.Contains(resultText(result), want) {
			t.Errorf("the refusal does not say %q: %s", want, resultText(result))
		}
	}
	if strings.Contains(resultText(result), "There was nothing at that address") {
		t.Errorf("the refusal says nothing was there: %s", resultText(result))
	}
	var opened mcp.OpenOut
	call(t, session, "open", mcp.OpenIn{URI: memory}, &opened)
	if !strings.Contains(opened.Content, "reads in centimetres") {
		t.Errorf("the refused forget took the memory: %s", opened.Content)
	}

	// The flush gives up and hands its claim back, as a failed flush does.
	if err := device.ReleaseQueued([]record.ID{claimed[0].ID}); err != nil {
		t.Fatalf("hand the claim back: %v", err)
	}
	var forgotten mcp.ForgetOut
	call(t, session, "forget", mcp.ForgetIn{URI: memory, Confirm: true}, &forgotten)
	if !forgotten.Removed {
		t.Error("forgetting the memory once the flush had let go reported removing nothing")
	}
}
