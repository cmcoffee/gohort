// Pulling a JSON object out of a model's reply.
//
// Every judge in the framework asks for JSON and gets JSON wrapped in
// something: a lead-in sentence, a ```json fence, an apology, a trailing
// "Let me know if you'd like me to explain." A parser that feeds the whole
// reply to encoding/json fails on all of them, and a model that answered
// correctly reads as a model that could not be parsed.
//
// This was written twice before it was lifted here — once in the guardrail
// warden, once inline in a judge — and both copies did the same thing with the
// same brace-depth walk. One copy, one set of edge cases.

package textutil

import "strings"

// FirstJSONObject returns the first balanced {...} run in s, or "" when there
// is none.
//
// Brace depth is tracked with string-literal awareness, so a `{` inside a
// quoted value doesn't open a level and an escaped `\"` doesn't close the
// string. That matters more here than it looks: the judges that use this quote
// UNTRUSTED text back in their verdicts, and a naive scan would let a crafted
// quote in a fetched page decide where the object ends.
//
// Returns the first object only. A model that emits two is answering a
// question it wasn't asked, and taking the first is the same rule every caller
// applied on its own.
func FirstJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}
