// What a call would actually DO, for the card that asks whether to allow it.
//
// The card used to say the tool's name, the raw argument blob and "this tool
// is set to ask before every call". That names the tool and shows the JSON,
// and neither answers the question somebody is being asked: would this be a
// problem? A name does not say whether the tool reaches outside the
// deployment, and a JSON object buries the one argument that matters - the
// host it would dial, the path it would write, the person it would message -
// among the ones that do not.
//
// So the card gets three things it did not have: what the tool is FOR, what it
// can reach, and the arguments that decide the answer, named and first.

package orchestrate

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// reachWords turns a tool's capabilities into what they mean for the reader.
//
// The capability names are the framework's vocabulary, not a person's: CapRead
// and CapExecute say nothing to somebody deciding in the moment whether to
// allow one call.
func reachWords(caps []Capability) []string {
	var out []string
	for _, c := range caps {
		switch c {
		case CapNetwork:
			out = append(out, "reaches outside this deployment")
		case CapWrite:
			out = append(out, "writes files or records")
		case CapExecute:
			out = append(out, "runs commands")
		case CapRead:
			out = append(out, "reads local data")
		}
	}
	return out
}

// decisiveArgs are the argument names that usually decide whether a call is
// fine, in the order they are worth reading.
//
// A call is judged by WHERE it goes and WHAT it carries, and those live under
// a handful of names across every tool anybody writes. Listing them first is
// the whole difference between a card somebody can answer and one they have to
// decode.
var decisiveArgs = []string{
	"url", "endpoint", "host", "domain", "path", "file", "dir",
	"command", "cmd", "script", "query", "to", "recipient", "channel",
	"amount", "body", "message",
}

// confirmCallSummary composes the card's detail: what the tool does, what it
// can reach, and its arguments with the decisive ones first.
//
// Falls back to the raw arguments rather than showing nothing. A summary that
// silently drops something it could not parse would be worse than the blob it
// replaced: the reader would approve a call believing they had seen it.
func confirmCallSummary(sess *ToolSession, name, args string) string {
	var b strings.Builder
	desc, caps := toolFacts(sess, name)
	if desc != "" {
		b.WriteString(desc)
		b.WriteString("\n")
	}
	if words := reachWords(caps); len(words) > 0 {
		b.WriteString("This tool " + joinWords(words) + ".\n")
	}
	if lines := namedArgs(args); len(lines) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(strings.Join(lines, "\n"))
	} else if strings.TrimSpace(args) != "" && strings.TrimSpace(args) != "{}" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(strings.TrimSpace(args))
	}
	return strings.TrimSpace(b.String())
}

// toolFacts finds a tool's one-line description and its capabilities, from
// whichever of the two places it lives in: the user's own records, or the
// framework registry.
func toolFacts(sess *ToolSession, name string) (string, []Capability) {
	if tt := toolRecordFor(sess, name); tt != nil {
		return firstLine(tt.Description), nil
	}
	for _, ct := range RegisteredChatTools() {
		if ct.Name() == name {
			// Through the def builder, which is where a ChatTool's
			// capabilities are read: the interface itself does not expose
			// them, and every other caller asks this way.
			d := ChatToolToAgentToolDefWithSession(ct, sess)
			return firstLine(d.Tool.Description), d.Tool.Caps
		}
	}
	return "", nil
}

// namedArgs renders the call's arguments one per line, decisive ones first.
//
// Values are truncated but never summarised: the point is to show what would
// be sent, and a paraphrase of an argument is not the argument.
func namedArgs(args string) []string {
	var raw map[string]any
	if json.Unmarshal([]byte(args), &raw) != nil || len(raw) == 0 {
		return nil
	}
	rank := map[string]int{}
	for i, k := range decisiveArgs {
		rank[k] = i
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		ri, oki := rank[strings.ToLower(keys[i])]
		rj, okj := rank[strings.ToLower(keys[j])]
		if oki != okj {
			return oki // a decisive argument sorts ahead of one that is not
		}
		if oki && okj && ri != rj {
			return ri < rj
		}
		return keys[i] < keys[j]
	})
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+": "+clipArgValue(raw[k]))
	}
	return out
}

// clipArgValue renders one value, bounded. A card nobody can read past is the
// same as no card.
func clipArgValue(v any) string {
	s := ""
	switch t := v.(type) {
	case string:
		s = t
	case nil:
		s = "(none)"
	default:
		if b, err := json.Marshal(t); err == nil {
			s = string(b)
		} else {
			s = fmt.Sprint(t)
		}
	}
	s = strings.TrimSpace(s)
	const max = 300
	if len(s) > max {
		return s[:max] + "… (" + fmt.Sprintf("%d", len(s)) + " chars)"
	}
	return s
}
