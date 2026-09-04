package record

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MessageSchema names the message format a session's chunks are written in.
//
// It is stored on the head so that a reader meeting a session written by a
// later build can tell whether it understands the transcript before it starts
// replaying one. No ratified standard for a stored conversation exists, so the
// format is ours and has to say which version of itself it is.
const MessageSchema = "sennit.session/1"

// A Role is who produced a message.
//
// Tool is carried explicitly even though not every vendor has it: the two live
// conventions are a literal tool role and a tool result inside a user turn, and
// a format that keeps the distinction can render either, while one that picks
// has already lost the information the other needs.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	RoleTool      Role = "tool"
)

// Roles lists the vocabulary in its canonical order.
var Roles = []Role{RoleUser, RoleAssistant, RoleSystem, RoleTool}

// Valid reports whether r is in the vocabulary.
func (r Role) Valid() bool {
	for _, known := range Roles {
		if r == known {
			return true
		}
	}
	return false
}

func (r Role) String() string { return string(r) }

// A PartType discriminates one piece of a message's content.
type PartType string

const (
	// PartText is prose.
	PartText PartType = "text"
	// PartReasoning is a thinking block. The text may be empty and the part
	// still matters, because its position in the sequence is information.
	PartReasoning PartType = "reasoning"
	// PartToolCall is an invocation, correlated to its result by CallID.
	PartToolCall PartType = "toolCall"
	// PartToolResult is what an invocation returned.
	PartToolResult PartType = "toolResult"
	// PartFile is a binary attachment, held by reference rather than inline.
	PartFile PartType = "file"
	// PartResourceLink points at another record in the vault.
	PartResourceLink PartType = "resourceLink"
)

// PartTypes lists the vocabulary in its canonical order.
//
// It is what this build knows how to render, and deliberately not what it will
// accept. A stored transcript may carry a part type from a provider this build
// has never met or from a later build of the vault itself, and refusing it would
// cost the whole conversation rather than one part of it.
var PartTypes = []PartType{PartText, PartReasoning, PartToolCall, PartToolResult, PartFile, PartResourceLink}

// Valid reports whether t is in this build's vocabulary.
//
// A part outside it is still storable and still round-trips; this is what a
// renderer asks before deciding whether it understands the part well enough to
// display it as something other than an opaque block.
func (t PartType) Valid() bool {
	for _, known := range PartTypes {
		if t == known {
			return true
		}
	}
	return false
}

func (t PartType) String() string { return string(t) }

// An Ext is a provider's own fields, carried through untouched.
//
// Every vendor has such a bag, and a format without one is lossy by
// construction: the fields it cannot name are the fields it drops. Values are
// kept as raw JSON rather than decoded, so a round trip returns exactly the
// bytes it was given and this build never has to understand a field a later
// provider invents.
type Ext map[string]json.RawMessage

// A Message is one turn, in the shape five independent vendors converged on.
//
// Content is an ordered list of typed parts and never a flat string. Every
// vendor agrees on that much, and flattening is the documented lossiness of the
// cross-agent converters that already exist: a string cannot hold a tool call,
// a thinking block or an image, so a session that survives one round trip
// degrades into a chat log on the next.
type Message struct {
	// ID is stable and unique within the session. It is what a preserved tail,
	// a fork point and a memory's provenance span all point at.
	ID   string `json:"id"`
	Role Role   `json:"role"`
	// Parts is the content, in order.
	Parts []Part `json:"parts"`
	// Created is when the turn happened, for display. Order comes from the
	// sequence and from Parent, never from the clock.
	Created Time `json:"created,omitzero"`
	// Parent is the message this one replies to, forming a DAG rather than a
	// list. Four independent implementations carry lineage or a rewind
	// primitive; retrofitting one costs a rewrite of every stored session,
	// and adding it up front costs one field.
	Parent string `json:"parent,omitempty"`
	// Meta is what the runtime knew about the turn.
	Meta MessageMeta `json:"meta,omitzero"`
	// Ext is the provider's own fields.
	Ext Ext `json:"ext,omitempty"`
}

// MessageMeta is the per-turn detail a replay does not need but a reader wants.
type MessageMeta struct {
	Model      string `json:"model,omitempty"`
	StopReason string `json:"stopReason,omitempty"`
	Usage      Usage  `json:"usage,omitzero"`
	Agent      string `json:"agent,omitempty"`
	// Skill attributes the turn to a skill, mirroring what a real transcript
	// already records.
	Skill string `json:"skill,omitempty"`
	// MemoryRefs are the memories that were in context for this turn. A
	// transcript replayed without them reproduces the words and not the
	// conditions.
	MemoryRefs []ID `json:"memoryRefs,omitempty"`
}

// Usage is what a turn cost.
type Usage struct {
	InputTokens      int `json:"inputTokens,omitempty"`
	OutputTokens     int `json:"outputTokens,omitempty"`
	CacheReadTokens  int `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens int `json:"cacheWriteTokens,omitempty"`
	ReasoningTokens  int `json:"reasoningTokens,omitempty"`
}

// A Part is one piece of a message's content.
//
// It is one struct with a discriminator rather than an interface, because the
// stored form has to survive a build that has never heard of a part type: a
// tolerant struct keeps the unknown type and its fields, where a decoder
// dispatching on the discriminator has to fail or drop it.
//
// The correlation id is on both the call and the result and is named the same
// on each. Vendors spell it four different ways, tool_use_id, call_id,
// toolCallId, gen_ai.tool.call.id, so no spelling is portable and the concept
// is: an explicit id linking a call to its result is the only genuinely
// universal part of tool semantics, and it is what a projection into any of
// those formats is built from.
type Part struct {
	Type PartType `json:"type"`

	// Text carries PartText and PartReasoning.
	Text string `json:"text,omitempty"`
	// Signature is the opaque attestation some providers attach to a
	// reasoning block, which has to survive the round trip for the block to
	// be replayable.
	Signature string `json:"signature,omitempty"`

	// CallID correlates PartToolCall with PartToolResult.
	CallID string `json:"callId,omitempty"`
	// Name and Input describe an invocation.
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// Content is a result's own parts, so a tool that returned an image is
	// stored as one.
	Content []Part `json:"content,omitempty"`
	// IsError marks a result the tool reported as a failure.
	IsError bool `json:"isError,omitempty"`

	// MediaType, Ref, Filename, Bytes and SHA256 describe an attachment.
	//
	// Ref points at bytes held elsewhere. Base64 inline is the largest single
	// cause of transcript bloat in every implementation that has been looked
	// at, and a session record that inlines it stops being small enough to be
	// worth having.
	MediaType string `json:"mediaType,omitempty"`
	Ref       string `json:"ref,omitempty"`
	Filename  string `json:"filename,omitempty"`
	Bytes     int64  `json:"bytes,omitempty"`
	SHA256    string `json:"sha256,omitempty"`

	// URI points at another record, for a resource link.
	URI string `json:"uri,omitempty"`

	// Ext is the provider's own fields for this part.
	Ext Ext `json:"ext,omitempty"`
}

// Limits on a stored message. They are generous, and they exist so that a
// malformed transcript is refused before it is sealed rather than after.
const (
	MaxMessageIDBytes  = 256
	MaxPartsPerMessage = 1024
)

// Validate reports whether a message is well formed enough to store.
func (m *Message) Validate() error {
	switch {
	case strings.TrimSpace(m.ID) == "":
		return fmt.Errorf("%w: message has no id", ErrInvalid)
	case len(m.ID) > MaxMessageIDBytes:
		return fmt.Errorf("%w: message id is %d bytes, limit %d", ErrInvalid, len(m.ID), MaxMessageIDBytes)
	case !m.Role.Valid():
		return fmt.Errorf("%w: message %s has role %q, want one of %s",
			ErrInvalid, m.ID, m.Role, strings.Join(roleNames(), ", "))
	case len(m.Parts) == 0:
		return fmt.Errorf("%w: message %s has no parts", ErrInvalid, m.ID)
	case len(m.Parts) > MaxPartsPerMessage:
		return fmt.Errorf("%w: message %s has %d parts, limit %d",
			ErrInvalid, m.ID, len(m.Parts), MaxPartsPerMessage)
	}
	for i := range m.Parts {
		if err := m.Parts[i].validate(m.ID); err != nil {
			return err
		}
	}
	return nil
}

func (p *Part) validate(messageID string) error {
	// The vocabulary is open, and this is the one place that has to say so.
	//
	// Measured against a real agent's stored log: a tool result may contain a
	// part type this build has never heard of, and a closed vocabulary refuses
	// the whole message rather than the part, which is the opposite of what a
	// portable record is for. Nothing above this is asked to understand an
	// unrecognised part; it is stored with its fields intact and rendered as
	// opaque. A type is required because a part with none cannot be interpreted
	// by anything, including a later build that does know the vocabulary.
	if strings.TrimSpace(string(p.Type)) == "" {
		return fmt.Errorf("%w: message %s carries a part with no type; the discriminator is what "+
			"makes a stored part interpretable at all", ErrInvalid, messageID)
	}
	// The correlation id is the one field whose absence is unrecoverable. A
	// call without it cannot be matched to its result by any later reader, and
	// no amount of ordering heuristics restores the link once the transcript
	// has been stored.
	if p.Type == PartToolCall || p.Type == PartToolResult {
		if strings.TrimSpace(p.CallID) == "" {
			return fmt.Errorf("%w: message %s has a %s part with no callId, which is the only thing "+
				"that correlates a call with its result", ErrInvalid, messageID, p.Type)
		}
	}
	for i := range p.Content {
		if err := p.Content[i].validate(messageID); err != nil {
			return err
		}
	}
	return nil
}

func roleNames() []string {
	out := make([]string, len(Roles))
	for i, r := range Roles {
		out[i] = string(r)
	}
	return out
}

// A ToolExchange is one invocation paired with what it returned.
type ToolExchange struct {
	CallID string
	Name   string
	// CallMessage and ResultMessage are the messages the two halves were found
	// in. An empty ResultMessage means the call has no result in the range
	// examined, which is ordinary at the end of a transcript and a defect in
	// the middle of one.
	CallMessage   string
	ResultMessage string
	Call          Part
	Result        Part
	// Matched reports whether a result was found for the call.
	Matched bool
}

// ToolExchanges pairs every tool call in a message sequence with its result.
//
// It walks parts recursively, because a result may itself contain parts, and it
// correlates on the id alone rather than on adjacency: an agent may interleave
// several calls before any of them returns, and position is not a link.
func ToolExchanges(messages []Message) []ToolExchange {
	var order []string
	byCall := make(map[string]*ToolExchange)

	var walk func(messageID string, parts []Part)
	walk = func(messageID string, parts []Part) {
		for _, part := range parts {
			switch part.Type {
			case PartToolCall:
				if _, seen := byCall[part.CallID]; !seen {
					byCall[part.CallID] = &ToolExchange{
						CallID: part.CallID, Name: part.Name,
						CallMessage: messageID, Call: part,
					}
					order = append(order, part.CallID)
				}
			case PartToolResult:
				exchange, seen := byCall[part.CallID]
				if !seen {
					// A result whose call is outside the range examined is
					// still worth reporting: it is how a caller notices that a
					// slice of a transcript has cut an exchange in half.
					exchange = &ToolExchange{CallID: part.CallID}
					byCall[part.CallID] = exchange
					order = append(order, part.CallID)
				}
				exchange.ResultMessage, exchange.Result, exchange.Matched = messageID, part, true
			}
			if len(part.Content) > 0 {
				walk(messageID, part.Content)
			}
		}
	}
	for _, message := range messages {
		walk(message.ID, message.Parts)
	}

	out := make([]ToolExchange, 0, len(order))
	for _, callID := range order {
		out = append(out, *byCall[callID])
	}
	return out
}
