package mcp

import (
	"fmt"
	"strings"

	"github.com/steven3002/sennit/record"
)

// Scheme is the vault's URI scheme. Memory and sessions are two kinds in one
// address space, not two endpoints, so one scheme addresses all of them and one
// function resolves them.
const Scheme = "sennit://"

// The vault's whole namespace. Everything a caller can reach has an address
// here, and every address is named in Instructions, a resource a host fetches
// and keeps to itself is invisible to the model, so registering one is not the
// same as making it reachable.
const (
	// VaultURI is what the vault holds and what it owes the network.
	VaultURI = Scheme + "vault"
	// GuideURI is how to use this server.
	GuideURI = Scheme + "guide"

	// MemoryTemplate addresses one memory. Memories are unbounded, so they are
	// a template and never an enumerated list: a vault of a hundred thousand
	// records cannot be listed into a model's context, and recall and browse
	// exist to reach them.
	MemoryTemplate = Scheme + "memory/{id}"
	// SessionTemplate addresses one conversation's head, its title, summary,
	// counts and links, without its transcript.
	SessionTemplate = Scheme + "session/{id}"
	// TranscriptTemplate addresses a conversation's messages.
	//
	// It is a separate address from the head because the two differ by three
	// orders of magnitude: a head measured at 1,119 bytes named 809 KiB of
	// transcript. Anything that browses wants the first; only a replay wants
	// the second, and it should have to ask.
	TranscriptTemplate = Scheme + "session/{id}/transcript"
)

// Templates lists the addressable forms, in the order a reader meets them.
var Templates = []string{MemoryTemplate, SessionTemplate, TranscriptTemplate}

// Fixed lists the addresses that are not templates.
var Fixed = []string{VaultURI, GuideURI}

// A Form is what an address points at.
type Form string

const (
	// FormVault and FormGuide are the two fixed resources.
	FormVault Form = "vault"
	FormGuide Form = "guide"
	// FormMemory is one memory record, FormSession one conversation's head, and
	// FormTranscript that conversation's messages.
	FormMemory     Form = "memory"
	FormSession    Form = "session"
	FormTranscript Form = "transcript"
)

// An Address is a parsed vault URI.
type Address struct {
	Form Form
	// URI is the address as it was written, so everything downstream reports
	// back the string the caller used.
	URI string
	// ID names the record, for the forms that address one.
	ID record.ID
}

// Kind reports which class of record an address points at, if any.
func (a Address) Kind() record.Kind {
	switch a.Form {
	case FormMemory:
		return record.KindMemory
	case FormSession, FormTranscript:
		return record.KindSession
	default:
		return ""
	}
}

// URI renders the canonical address of a record.
func URI(kind record.Kind, id record.ID) string {
	return Scheme + string(kind) + "/" + id.String()
}

// TranscriptURI renders the address of a conversation's messages.
func TranscriptURI(id record.ID) string { return URI(record.KindSession, id) + "/transcript" }

// Parse reads a vault URI.
//
// There is exactly one of these, and every path into the namespace goes through
// it, the tool that opens a record and the protocol's own resource reads alike.
// Two entry points into one address space drift, and they drift quietly, because
// each looks correct on its own. Measured once already: an `open` tool answered
// "no record at sennit://guide" for a resource sitting in the server's own
// resource listing, within four hundred lines of the claim that one opener
// resolves everything.
func Parse(uri string) (Address, error) {
	rest, ok := strings.CutPrefix(uri, Scheme)
	if !ok {
		return Address{}, fmt.Errorf("resolve %q: not a %s address", uri, Scheme)
	}
	switch rest {
	case string(FormVault):
		return Address{Form: FormVault, URI: uri}, nil
	case string(FormGuide):
		return Address{Form: FormGuide, URI: uri}, nil
	}

	head, tail, ok := strings.Cut(rest, "/")
	if !ok {
		return Address{}, fmt.Errorf("resolve %q: expected %s<kind>/<id>, %s or %s",
			uri, Scheme, VaultURI, GuideURI)
	}
	idPart, suffix, hasSuffix := strings.Cut(tail, "/")
	id, err := record.ParseID(idPart)
	if err != nil {
		return Address{}, fmt.Errorf("resolve %q: %w", uri, err)
	}

	switch record.Kind(head) {
	case record.KindMemory:
		if hasSuffix {
			return Address{}, fmt.Errorf("resolve %q: a memory has no parts to address separately", uri)
		}
		return Address{Form: FormMemory, URI: uri, ID: id}, nil
	case record.KindSession:
		switch {
		case !hasSuffix:
			return Address{Form: FormSession, URI: uri, ID: id}, nil
		case suffix == string(FormTranscript):
			return Address{Form: FormTranscript, URI: uri, ID: id}, nil
		default:
			return Address{}, fmt.Errorf("resolve %q: a session addresses its head and %s, not %q",
				uri, TranscriptTemplate, suffix)
		}
	default:
		return Address{}, fmt.Errorf("resolve %q: unknown kind %q; this vault addresses %s and %s",
			uri, head, record.KindMemory, record.KindSession)
	}
}
