package record_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/steven3002/sennit/record"
)

// The portable message schema exists so a conversation written by one agent can
// be replayed by another. Until this file it had only ever met transcripts this
// project wrote, which is a round trip against its own assumptions.
//
// What follows is an adapter from a real agent's stored log into record.Message
// and back, and a census of what survives. It is deliberately test-only: nothing
// in the product reads another agent's log directory, because an agent saving a
// session supplies messages in this schema rather than having its private
// format parsed. The adapter's job is to answer whether the schema is expressive
// enough to be handed a real transcript, not to become a feature.
//
// The transcripts it was measured against are the owner's own conversation data
// and are not in this repository. TranscriptDirEnv points the test at them; with
// the variable unset the same shapes are exercised against a synthetic fixture
// that reproduces every structure found in the real ones.

// TranscriptDirEnv points the round trip at a directory of real agent
// transcripts, as newline-delimited JSON.
const TranscriptDirEnv = "SENNIT_TRANSCRIPTS"

// logRecord is one line of a stored agent log, kept as raw JSON so that a field
// this adapter has never heard of is still there to be carried or counted.
type logRecord map[string]json.RawMessage

// census counts what a conversion did, by structure rather than by content.
type census struct {
	Lines       int
	Messages    int
	NotMessages map[string]int
	Parts       map[string]int
	// Rebuilt counts messages whose conversion back matched the original
	// exactly, and Diverged the ones that did not, by the field that differed.
	Rebuilt  int
	Diverged map[string]int
	// Refused counts messages the schema would not store at all, by reason.
	Refused map[string]int
}

func newCensus() *census {
	return &census{
		NotMessages: map[string]int{}, Parts: map[string]int{},
		Diverged: map[string]int{}, Refused: map[string]int{},
	}
}

func (c *census) report(t *testing.T) {
	t.Helper()
	t.Logf("lines %d · messages %d · other record types %d",
		c.Lines, c.Messages, c.Lines-c.Messages)
	t.Logf("  round-tripped identically: %d of %d", c.Rebuilt, c.Messages)
	for _, m := range []struct {
		label  string
		counts map[string]int
	}{
		{"not a message", c.NotMessages},
		{"content parts", c.Parts},
		{"diverged at", c.Diverged},
		{"refused because", c.Refused},
	} {
		if len(m.counts) == 0 {
			continue
		}
		keys := make([]string, 0, len(m.counts))
		for k := range m.counts {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(a, b int) bool {
			if m.counts[keys[a]] != m.counts[keys[b]] {
				return m.counts[keys[a]] > m.counts[keys[b]]
			}
			return keys[a] < keys[b]
		})
		t.Logf("  %s:", m.label)
		for _, k := range keys {
			t.Logf("    %-32s %d", k, m.counts[k])
		}
	}
}

// TestARealTranscriptRoundTripsThroughTheSchema converts a real agent's log into
// record.Message and back, and requires every message to come back identical.
//
// It is the measurement M17 was opened for. Being lossy against a real agent is
// the failure the ext bag exists to prevent, and it would otherwise be found
// while recording the demo.
func TestARealTranscriptRoundTripsThroughTheSchema(t *testing.T) {
	dir := os.Getenv(TranscriptDirEnv)
	if dir == "" {
		t.Skipf("set %s to a directory of agent transcripts to run this against real data",
			TranscriptDirEnv)
	}
	var files []string
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".jsonl") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	if len(files) == 0 {
		t.Fatalf("no .jsonl transcripts under %s", dir)
	}

	counts := newCensus()
	for _, file := range files {
		roundTripFile(t, file, counts)
	}
	counts.report(t)

	if counts.Messages == 0 {
		t.Fatal("no messages were found in any transcript")
	}
	if len(counts.Refused) > 0 {
		t.Errorf("the schema refused to store %d message(s) of a real transcript", total(counts.Refused))
	}
	if counts.Rebuilt != counts.Messages {
		t.Errorf("%d of %d messages did not come back identical",
			counts.Messages-counts.Rebuilt, counts.Messages)
	}
}

// TestTheSyntheticTranscriptCarriesEveryShapeFoundInARealOne runs the same
// conversion over a fixture reproducing each structure the real transcripts
// hold, so the regression survives on a machine with no transcripts on it.
func TestTheSyntheticTranscriptCarriesEveryShapeFoundInARealOne(t *testing.T) {
	counts := newCensus()
	roundTripFile(t, filepath.Join("testdata", "transcript.jsonl"), counts)
	counts.report(t)

	// The shapes the fixture exists to carry. Each was found in a real
	// transcript and each broke, or nearly broke, the conversion.
	for part, want := range map[string]int{
		"text":           2,
		"thinking":       1,
		"tool_use":       1,
		"tool_result":    3,
		"image":          1,
		"tool_reference": 1,
		"<string>":       1,
	} {
		if counts.Parts[part] < want {
			t.Errorf("the fixture holds %d %q part(s), want at least %d", counts.Parts[part], part, want)
		}
	}
	if len(counts.Refused) > 0 {
		t.Errorf("the schema refused %d message(s) of the fixture: %v", total(counts.Refused), counts.Refused)
	}
	if counts.Rebuilt != counts.Messages {
		t.Errorf("%d of %d fixture messages did not come back identical",
			counts.Messages-counts.Rebuilt, counts.Messages)
	}
}

func total(counts map[string]int) int {
	var n int
	for _, count := range counts {
		n += count
	}
	return n
}

func roundTripFile(t *testing.T, path string, counts *census) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), 64<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		counts.Lines++

		var stored logRecord
		if err := json.Unmarshal([]byte(line), &stored); err != nil {
			t.Fatalf("%s: parse line %d: %v", path, counts.Lines, err)
		}
		kind := unquote(stored["type"])
		if kind != "user" && kind != "assistant" {
			counts.NotMessages[kind]++
			continue
		}
		counts.Messages++

		message, err := toMessage(stored, counts)
		if err != nil {
			counts.Refused[reason(err)]++
			continue
		}
		if err := message.Validate(); err != nil {
			counts.Refused[reason(err)]++
			continue
		}
		// Through the wire form, not only through the struct: a field that
		// survives in memory and not in JSON is still lost.
		encoded, err := json.Marshal(message)
		if err != nil {
			t.Fatalf("%s: marshal message: %v", path, err)
		}
		var reloaded record.Message
		if err := json.Unmarshal(encoded, &reloaded); err != nil {
			t.Fatalf("%s: reload message: %v", path, err)
		}

		rebuilt, err := fromMessage(&reloaded)
		if err != nil {
			counts.Refused[reason(err)]++
			continue
		}
		if field, ok := firstDifference(stored, rebuilt); !ok {
			counts.Diverged[field]++
			continue
		}
		counts.Rebuilt++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
}

// reason reduces an error to the shape of the failure, so the census counts
// causes rather than instances.
func reason(err error) string {
	text := err.Error()
	if i := strings.Index(text, ": "); i >= 0 && strings.Contains(text[:i], "invalid") {
		text = text[i+2:]
	}
	for _, cut := range []string{" \"", " '"} {
		if i := strings.Index(text, cut); i >= 0 {
			text = text[:i]
		}
	}
	return text
}

// messageFields are the log's own names for the things the schema has a home
// for. Everything else is carried in the ext bag untouched, which is what makes
// the round trip possible at all.
//
// parentUuid is conditional: the schema's Parent is a string, so it can say
// "this message replies to that one" and "this message is a root", but not
// which of null and absent the provider used to spell the second. A null one
// therefore rides in the passthrough bag with everything else the schema does
// not model, rather than being flattened into an empty string.
var messageFields = map[string]bool{"uuid": true, "timestamp": true}

func mapsParent(stored logRecord) bool {
	raw, ok := stored["parentUuid"]
	return ok && string(raw) != "null"
}

// extMessageKey is where the adapter parks the provider's message envelope,
// the fields inside `message` that are not the role or the content.
const extMessageKey = "message"

func toMessage(stored logRecord, counts *census) (*record.Message, error) {
	var envelope map[string]json.RawMessage
	if raw, ok := stored[extMessageKey]; ok {
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, fmt.Errorf("message envelope: %w", err)
		}
	}

	message := &record.Message{
		ID:   unquote(stored["uuid"]),
		Role: record.Role(unquote(envelope["role"])),
		Ext:  record.Ext{},
	}
	if mapsParent(stored) {
		message.Parent = unquote(stored["parentUuid"])
	}
	if raw, ok := stored["timestamp"]; ok {
		if err := message.Created.UnmarshalJSON(raw); err != nil {
			return nil, fmt.Errorf("timestamp: %w", err)
		}
	}

	parts, err := toParts(envelope["content"], counts)
	if err != nil {
		return nil, err
	}
	message.Parts = parts

	// Everything the schema has no field for rides in the passthrough bag, at
	// the level it was found. The envelope keeps its own namespace so that a
	// record-level key and a message-level key of the same name cannot collide.
	for key, value := range stored {
		if messageFields[key] || key == extMessageKey {
			continue
		}
		if key == "parentUuid" && mapsParent(stored) {
			continue
		}
		message.Ext[key] = value
	}
	kept := record.Ext{}
	for key, value := range envelope {
		if key == "role" || key == "content" {
			continue
		}
		kept[key] = value
	}
	if len(kept) > 0 {
		encoded, err := json.Marshal(kept)
		if err != nil {
			return nil, fmt.Errorf("message envelope: %w", err)
		}
		message.Ext[extMessageKey] = encoded
	}
	return message, nil
}

func fromMessage(message *record.Message) (logRecord, error) {
	out := logRecord{}
	for key, value := range message.Ext {
		if key == extMessageKey {
			continue
		}
		out[key] = value
	}
	out["uuid"] = quote(message.ID)
	if message.Parent != "" {
		out["parentUuid"] = quote(message.Parent)
	}
	if !message.Created.IsZero() {
		encoded, err := message.Created.MarshalJSON()
		if err != nil {
			return nil, err
		}
		out["timestamp"] = encoded
	}

	envelope := map[string]json.RawMessage{}
	if raw, ok := message.Ext[extMessageKey]; ok {
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, fmt.Errorf("message envelope: %w", err)
		}
	}
	envelope["role"] = quote(string(message.Role))
	content, err := fromParts(message.Parts)
	if err != nil {
		return nil, err
	}
	envelope["content"] = content

	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	out[extMessageKey] = encoded
	return out, nil
}

// extPartKey is where a part's own unmapped provider fields are parked.
const extPartKey = "$fields"

// extRawKey marks content that the provider stored as a bare string rather than
// as a list of parts.
//
// It is a shape rather than a field, and it is the one thing a naive adapter
// gets wrong in both directions: the schema's content is always an ordered list
// of typed parts, deliberately, because a flat string cannot hold a tool call or
// a thinking block. Recording that the provider wrote a string is what lets the
// conversion be reversed without the schema giving up the property that made it
// portable.
const extRawKey = "$string"

func toParts(raw json.RawMessage, counts *census) ([]record.Part, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("message has no content")
	}
	if raw[0] == '"' {
		counts.Parts["<string>"]++
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, err
		}
		return []record.Part{{
			Type: record.PartText,
			Text: text,
			Ext:  record.Ext{extRawKey: json.RawMessage("true")},
		}}, nil
	}

	var stored []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, fmt.Errorf("message content: %w", err)
	}
	out := make([]record.Part, 0, len(stored))
	for _, part := range stored {
		converted, err := toPart(part, counts)
		if err != nil {
			return nil, err
		}
		out = append(out, converted)
	}
	return out, nil
}

// partMapping is what each provider part type becomes, and which of its fields
// the schema has a named home for. Everything unnamed is carried in the part's
// own ext bag.
var partMapping = map[string]struct {
	as     record.PartType
	mapped map[string]bool
}{
	"text":        {record.PartText, map[string]bool{"type": true, "text": true}},
	"thinking":    {record.PartReasoning, map[string]bool{"type": true, "thinking": true, "signature": true}},
	"tool_use":    {record.PartToolCall, map[string]bool{"type": true, "id": true, "name": true, "input": true}},
	"tool_result": {record.PartToolResult, map[string]bool{"type": true, "tool_use_id": true, "is_error": true, "content": true}},
	"image":       {record.PartFile, map[string]bool{"type": true}},
}

func toPart(stored map[string]json.RawMessage, counts *census) (record.Part, error) {
	kind := unquote(stored["type"])
	counts.Parts[kind]++

	mapping, known := partMapping[kind]
	// consumed starts as the mapping's own answer and is narrowed by the cases
	// below, so a field the schema models only conditionally, one whose absent
	// and default spellings the schema cannot tell apart, falls back to the
	// passthrough bag instead of being silently normalised.
	consumed := make(map[string]bool, len(mapping.mapped))
	for key := range mapping.mapped {
		consumed[key] = true
	}
	part := record.Part{Type: mapping.as, Ext: record.Ext{}}
	if !known {
		// A part type this build has never heard of. It is stored under its own
		// name with its fields intact rather than refused: the alternative is
		// that one unrecognised part costs the whole conversation, and a
		// vocabulary frozen in this build is not a portable record.
		part.Type = record.PartType(kind)
	}

	switch kind {
	case "text":
		part.Text = unquote(stored["text"])
	case "thinking":
		part.Text, part.Signature = unquote(stored["thinking"]), unquote(stored["signature"])
	case "tool_use":
		part.CallID, part.Name, part.Input = unquote(stored["id"]), unquote(stored["name"]), stored["input"]
	case "tool_result":
		part.CallID = unquote(stored["tool_use_id"])
		// IsError is a bool, so it holds the fact and not the provider's
		// spelling of its absence: written out again, an explicit false and an
		// omitted key are the same key. The fact is what the schema models, and
		// the spelling rides in the passthrough bag with everything else it does
		// not, which is why only a true value is treated as mapped.
		if raw, ok := stored["is_error"]; !ok || string(raw) != "true" {
			delete(consumed, "is_error")
		} else {
			part.IsError = true
		}
		content, err := toResultContent(stored["content"], &part, counts)
		if err != nil {
			return record.Part{}, err
		}
		part.Content = content
	case "image":
		var source struct {
			MediaType string `json:"media_type"`
		}
		if raw, ok := stored["source"]; ok {
			if err := json.Unmarshal(raw, &source); err != nil {
				return record.Part{}, err
			}
		}
		part.MediaType = source.MediaType
	}

	kept := record.Ext{}
	for key, value := range stored {
		if consumed[key] || (!known && key == "type") {
			continue
		}
		kept[key] = value
	}
	if len(kept) > 0 {
		encoded, err := json.Marshal(kept)
		if err != nil {
			return record.Part{}, err
		}
		part.Ext[extPartKey] = encoded
	}
	if len(part.Ext) == 0 {
		part.Ext = nil
	}
	return part, nil
}

// toResultContent converts what a tool returned.
//
// A provider may write it as a bare string or as a list of parts, and the
// overwhelming majority of real ones are strings. The schema holds parts, so a
// string becomes one text part, marked so the conversion back can tell the two
// apart.
func toResultContent(raw json.RawMessage, part *record.Part, counts *census) ([]record.Part, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, err
		}
		if part.Ext == nil {
			part.Ext = record.Ext{}
		}
		part.Ext[extRawKey] = json.RawMessage("true")
		return []record.Part{{Type: record.PartText, Text: text}}, nil
	}
	var stored []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, fmt.Errorf("tool result content: %w", err)
	}
	out := make([]record.Part, 0, len(stored))
	for _, inner := range stored {
		converted, err := toPart(inner, counts)
		if err != nil {
			return nil, err
		}
		out = append(out, converted)
	}
	return out, nil
}

func fromParts(parts []record.Part) (json.RawMessage, error) {
	if len(parts) == 1 && parts[0].Ext[extRawKey] != nil && parts[0].Type == record.PartText {
		return json.Marshal(parts[0].Text)
	}
	out := make([]map[string]json.RawMessage, 0, len(parts))
	for i := range parts {
		converted, err := fromPart(&parts[i])
		if err != nil {
			return nil, err
		}
		out = append(out, converted)
	}
	return json.Marshal(out)
}

// providerNames maps the schema's part types back to the provider's spelling.
var providerNames = map[record.PartType]string{
	record.PartText:       "text",
	record.PartReasoning:  "thinking",
	record.PartToolCall:   "tool_use",
	record.PartToolResult: "tool_result",
	record.PartFile:       "image",
}

func fromPart(part *record.Part) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	if raw, ok := part.Ext[extPartKey]; ok {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, err
		}
	}
	name, known := providerNames[part.Type]
	if !known {
		name = string(part.Type)
	}
	out["type"] = quote(name)

	switch part.Type {
	case record.PartText:
		out["text"] = quote(part.Text)
	case record.PartReasoning:
		out["thinking"], out["signature"] = quote(part.Text), quote(part.Signature)
	case record.PartToolCall:
		out["id"], out["name"], out["input"] = quote(part.CallID), quote(part.Name), part.Input
	case record.PartToolResult:
		out["tool_use_id"] = quote(part.CallID)
		if part.IsError {
			out["is_error"] = json.RawMessage("true")
		}
		if part.Content != nil {
			if part.Ext[extRawKey] != nil {
				out["content"] = quote(part.Content[0].Text)
				break
			}
			content, err := fromParts(part.Content)
			if err != nil {
				return nil, err
			}
			out["content"] = content
		}
	}
	return out, nil
}

// firstDifference compares two log records field by field and names the first
// one that differs, so the census counts which field was lost rather than only
// that something was.
func firstDifference(want, got logRecord) (string, bool) {
	keys := map[string]bool{}
	for key := range want {
		keys[key] = true
	}
	for key := range got {
		keys[key] = true
	}
	names := make([]string, 0, len(keys))
	for key := range keys {
		names = append(names, key)
	}
	sort.Strings(names)

	for _, key := range names {
		if equalJSON(want[key], got[key]) {
			continue
		}
		if key == extMessageKey {
			if inner, ok := firstEnvelopeDifference(want[key], got[key]); !ok {
				return "message." + inner, false
			}
			continue
		}
		return key, false
	}
	return "", true
}

func firstEnvelopeDifference(want, got json.RawMessage) (string, bool) {
	var a, b map[string]json.RawMessage
	if err := json.Unmarshal(want, &a); err != nil {
		return "<unparseable>", false
	}
	if err := json.Unmarshal(got, &b); err != nil {
		return "<unparseable>", false
	}
	keys := map[string]bool{}
	for key := range a {
		keys[key] = true
	}
	for key := range b {
		keys[key] = true
	}
	names := make([]string, 0, len(keys))
	for key := range keys {
		names = append(names, key)
	}
	sort.Strings(names)
	for _, key := range names {
		if !equalJSON(a[key], b[key]) {
			if key == "content" {
				return "content" + contentDifference(a[key], b[key]), false
			}
			return key, false
		}
	}
	return "", true
}

// contentDifference names the part and the field a conversion changed, so a
// census reports which structure was lost rather than that the content differs.
func contentDifference(want, got json.RawMessage) string {
	var a, b []map[string]json.RawMessage
	if err := json.Unmarshal(want, &a); err != nil {
		return "<not a list>"
	}
	if err := json.Unmarshal(got, &b); err != nil {
		return "<not a list>"
	}
	if len(a) != len(b) {
		return fmt.Sprintf("[%d parts vs %d]", len(a), len(b))
	}
	for i := range a {
		keys := map[string]bool{}
		for key := range a[i] {
			keys[key] = true
		}
		for key := range b[i] {
			keys[key] = true
		}
		names := make([]string, 0, len(keys))
		for key := range keys {
			names = append(names, key)
		}
		sort.Strings(names)
		for _, key := range names {
			if equalJSON(a[i][key], b[i][key]) {
				continue
			}
			switch {
			case len(a[i][key]) == 0:
				return fmt.Sprintf("[%s].%s added", unquote(a[i]["type"]), key)
			case len(b[i][key]) == 0:
				return fmt.Sprintf("[%s].%s dropped", unquote(a[i]["type"]), key)
			default:
				return fmt.Sprintf("[%s].%s changed", unquote(a[i]["type"]), key)
			}
		}
	}
	return "<equal part by part>"
}

// equalJSON compares two raw values by their decoded form, so key order and
// insignificant whitespace do not count as a difference.
func equalJSON(a, b json.RawMessage) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	var left, right any
	if err := json.Unmarshal(a, &left); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &right); err != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}

func unquote(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var out string
	if err := json.Unmarshal(raw, &out); err != nil {
		return ""
	}
	return out
}

func quote(value string) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`""`)
	}
	return encoded
}
