package core

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// --- invented-identifier gate ------------------------------------------------
//
// A model that references a record by an id nobody issued gets the service's
// 404, and a 404 is indistinguishable from "that record was deleted" or "this
// endpoint is broken" — so the model explains the failure instead of fixing
// it, and the invented id survives into the next call and the report. The
// recall tool learned this first (an id nobody issued is a fabrication, not a
// formatting mistake); this is the same rule for every tool, checked from the
// one place that sees both the arguments and everything the session was given.

// uuidPattern matches the canonical 8-4-4-4-12 hex form. Deliberately narrow:
// an opaque slug or a numeric id cannot be told apart from a value the model
// legitimately composed, and refusing one of those would block real work.
var uuidPattern = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)

// referenceParam reports whether an argument NAMES an existing record rather
// than describing a new one: id, post_id, parentId, thread_id, uuid, ref.
func referenceParam(name string) bool {
	n := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(strings.TrimSpace(name)))
	switch n {
	case "id", "uuid", "guid", "ref", "reference":
		return true
	}
	return strings.HasSuffix(n, "id") || strings.HasSuffix(n, "uuid") || strings.HasSuffix(n, "ref")
}

// creationCall reports a call that BRINGS A RECORD INTO BEING, where an id the
// session has never seen is the point rather than a mistake. Matched on the
// verb the name starts with, which is the only signal available here.
func creationCall(tool string, args map[string]any) bool {
	t := strings.ToLower(strings.TrimSpace(tool))
	if a, ok := args["action"].(string); ok && strings.TrimSpace(a) != "" {
		t = strings.ToLower(strings.TrimSpace(a))
	}
	if i := strings.LastIndex(t, "/"); i >= 0 {
		t = t[i+1:]
	}
	for _, verb := range []string{"create", "new", "add", "insert", "register", "upsert", "save", "put", "set", "start", "open", "make", "import", "generate", "schedule"} {
		if strings.HasPrefix(t, verb) {
			return true
		}
	}
	return false
}

// collectKnownIDs gathers every UUID the conversation has been GIVEN: the
// system prompt, the user's own words, and tool results. Assistant prose is
// excluded on purpose — see the gate's note above.
func collectKnownIDs(systemPrompt string, history []Message) map[string]bool {
	out := map[string]bool{}
	add := func(text string) {
		for _, id := range uuidPattern.FindAllString(text, -1) {
			out[strings.ToLower(id)] = true
		}
	}
	add(systemPrompt)
	for _, m := range history {
		if m.Role == "user" || m.Role == "system" {
			add(m.Content)
		}
		for _, r := range m.ToolResults {
			add(r.Content)
		}
	}
	return out
}

// idProvenanceRefusal returns the text to hand back instead of running a call
// whose reference argument names an id this session never saw, or "" to let
// the call proceed.
//
// The closing paragraph says the tool itself still works, for the same reason
// the user-denial message three branches up says a denial denies the OPERATION
// and not one route to it. Observed live on a support agent: the gate refused a
// fabricated doc_id, and the agent spent the next twenty minutes telling the
// user that knowledge_search was not in its tool set, then repeating it every
// turn once the claim was in its own history. A refusal delivered as a tool
// error, opening "was NOT called", reads as absence unless it says otherwise.
func idProvenanceRefusal(tool string, args map[string]any, known map[string]bool) string {
	if creationCall(tool, args) {
		return ""
	}
	for _, name := range sortedArgNames(args) {
		if !referenceParam(name) {
			continue
		}
		v, _ := args[name].(string)
		id := strings.ToLower(strings.TrimSpace(v))
		if !uuidPattern.MatchString(id) || known[id] {
			continue
		}
		return fmt.Sprintf(
			"STOP — '%s' was NOT called. Its %s is %q, an identifier nothing in this conversation ever produced: it is not in any tool result, and the user did not give it to you. You composed it.%s\n\n"+
				"An id you did not receive will not start working on a retry, and the service's 404 for one reads exactly like a deleted record or a broken endpoint — do not report it as either. "+
				"Call the tool that LISTS or SEARCHES the records you want, copy the id from its result character-for-character, and use that. If you cannot find the record, say so plainly.\n\n"+
				"'%s' itself is available and working, and so is your access to it. What was refused is this one argument, nothing else. Do not tell the user the tool is missing, unavailable, or absent from your tool set, and do not reach for another route to the same record.",
			tool, name, v, nearestKnownIDNote(id, known), tool)
	}
	return ""
}

// nearestKnownIDNote names a real id the invented one was likely assembled
// from — the observed failure spliced the front of one id onto the tail of
// another, and being shown the pair is what makes that visible.
func nearestKnownIDNote(id string, known map[string]bool) string {
	best, bestLen := "", 0
	for k := range known {
		n := 0
		for n < len(k) && n < len(id) && k[n] == id[n] {
			n++
		}
		if n > bestLen || (n == bestLen && k < best) {
			best, bestLen = k, n
		}
	}
	if bestLen < 8 {
		return ""
	}
	return fmt.Sprintf(" The closest id this conversation actually produced is %q — check whether you meant that one, and whether you joined pieces of two different ids together.", best)
}

// sortedArgNames keeps a refusal deterministic when a call carries more than
// one reference argument.
func sortedArgNames(args map[string]any) []string {
	names := make([]string, 0, len(args))
	for k := range args {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
