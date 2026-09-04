package orchestrate

import (
	"encoding/json"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// The judges ask a model to quote sentences verbatim inside a JSON object.
// Quoted prose carries its own quotation marks, and a worker-tier model in JSON
// mode reliably copies them in unescaped:
//
//	{"verdict":"ASSERTED","claim":"it runs "in **descending order**" first","basis":"…"}
//
// That is invalid JSON, so a strict decode fails and the judge had to say "no
// opinion" — on a verdict it had in hand, in the exact case (a quoted claim)
// the judge exists for. The fields are all flat strings under known keys, so
// the object can be recovered by cutting at the key markers instead of at the
// quotes: whatever lies between `"claim":"` and the next `"<key>":` is the
// claim, unescaped quotes and all.

// salvageJudgeJSON recovers a flat object of string fields from text that a
// strict decode rejected. keys are the fields the judge asked for. ok is false
// when the text does not carry a "verdict" marker at all — prose in place of
// JSON stays no opinion, exactly as before; this only rescues a JSON object
// whose strings were not escaped.
func salvageJudgeJSON(raw string, keys []string) (map[string]string, bool) {
	raw = StripCodeFence(strings.TrimSpace(raw))
	if start := strings.Index(raw, "{"); start >= 0 {
		raw = raw[start:]
	}
	if end := strings.LastIndex(raw, "}"); end >= 0 {
		raw = raw[:end]
	}
	type marker struct {
		key        string
		start, val int // marker start, first byte of the value
	}
	var marks []marker
	for _, k := range keys {
		at := strings.Index(raw, `"`+k+`"`)
		if at < 0 {
			continue
		}
		rest := raw[at+len(k)+2:]
		trimmed := strings.TrimLeft(rest, " \t\r\n")
		if !strings.HasPrefix(trimmed, ":") {
			continue
		}
		trimmed = strings.TrimLeft(trimmed[1:], " \t\r\n")
		if !strings.HasPrefix(trimmed, `"`) {
			continue
		}
		marks = append(marks, marker{key: k, start: at, val: len(raw) - len(trimmed) + 1})
	}
	sort.Slice(marks, func(i, j int) bool { return marks[i].start < marks[j].start })
	out := map[string]string{}
	for i, m := range marks {
		end := len(raw)
		if i+1 < len(marks) {
			end = marks[i+1].start
		}
		v := strings.TrimRight(raw[m.val:end], " \t\r\n")
		v = strings.TrimSuffix(v, ",")
		v = strings.TrimRight(v, " \t\r\n")
		v = strings.TrimSuffix(v, `"`)
		out[m.key] = unescapeJudgeString(v)
	}
	if _, ok := out["verdict"]; !ok {
		return nil, false
	}
	return out, true
}

// unescapeJudgeString turns a JSON string body back into text. A body that
// decodes as JSON is decoded; one that does not (the unescaped case) keeps
// its bytes with only the escapes a model does still write undone.
func unescapeJudgeString(body string) string {
	var s string
	if json.Unmarshal([]byte(`"`+body+`"`), &s) == nil {
		return s
	}
	r := strings.NewReplacer(`\"`, `"`, `\n`, "\n", `\t`, "\t", `\\`, `\`)
	return r.Replace(body)
}
