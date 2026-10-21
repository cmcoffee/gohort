package assist

// The reply contract for a form, and its parser, together: the instruction
// the model gets and the code that reads its answer.

import (
	"encoding/json"
	"sort"
	"strings"
)

// FieldsFence is the marker the model wraps proposed form values in. Not a
// bare ``` or ```json, which an answer may hold as an example.
const FieldsFence = "```fields"

// FieldsReplyContract is how to answer beside a form.
const FieldsReplyContract = "Answer in a few short sentences: what you found and what you changed, or the one question you need answered first. WHEN you are proposing values for the form, follow the answer with a fenced block tagged `fields` holding a JSON object of ONLY the fields to change, by their names above, each with its complete new value:\n\n" +
	FieldsFence + "\n{\"field_name\": \"the new value\"}\n```\n\n" +
	"Rules:\n" +
	"- Only the fields listed above, by their names. A field you leave out keeps what it holds.\n" +
	"- A list field takes the whole list, as an array of objects with its columns' names.\n" +
	"- A toggle takes true or false, a number a number, a choice one of its options.\n" +
	"- What you propose fills the form; nothing is saved until the person saves it. Where you could check it with a tool before proposing it, check it.\n" +
	"- Answering a question, or asking one, needs no block.\n"

// SplitFieldsReply separates the model's answer from the values it
// proposed, keeping only fields the form has. note, when set, says what of
// the proposal could not be used, for the person to see: a block that was
// not JSON, or fields the form does not have.
//
// A ```json block is read the same way when every key in it is one of the
// form's fields: a model that forgot the tag still meant the proposal.
func SplitFieldsReply(raw string, allowed map[string]bool) (reply string, fields map[string]any, note string) {
	s := strings.TrimSpace(raw)
	open := strings.Index(s, FieldsFence)
	fence := FieldsFence
	if open < 0 {
		open = strings.Index(s, "```json")
		fence = "```json"
		if open < 0 {
			return s, nil, ""
		}
	}
	reply = strings.TrimSpace(s[:open])
	rest := s[open+len(fence):]
	tail := ""
	if end := strings.Index(rest, "```"); end >= 0 {
		rest, tail = rest[:end], strings.TrimSpace(rest[end+3:])
	}
	if tail != "" {
		reply = strings.TrimSpace(reply + "\n\n" + tail)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(rest)), &got); err != nil {
		if fence != FieldsFence {
			return s, nil, "" // an example, not a proposal
		}
		return reply, nil, "(It proposed values, but they could not be read, so the form is unchanged.)"
	}
	var unknown []string
	fields = map[string]any{}
	for k, v := range got {
		if allowed[k] {
			fields[k] = v
		} else {
			unknown = append(unknown, k)
		}
	}
	if fence != FieldsFence && len(unknown) > 0 {
		return s, nil, ""
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		note = "(Left out what the form has no field for: " + strings.Join(unknown, ", ") + ".)"
	}
	if len(fields) == 0 {
		fields = nil
	}
	return reply, fields, note
}
