package appcheck

import (
	"encoding/json"
	"strings"
)

// ScriptReply is the JSON value a script printed as its reply, and what it
// printed besides. The sandbox hands back a script's output and its error
// stream together, so lines the script did not mean as its reply land in the
// same text: a warning Python wrote (a newer interpreter deprecating
// datetime.utcnow), a progress line. Taken whole, those made a working data
// source read as broken JSON and its page fail to load.
//
// The reply is the whole output when that is one JSON value; else the
// shortest tail of whole lines that parses as an object or an array (the
// usual case: a warning is written as it happens, the reply when the script
// ends); else the shortest head of lines that does (a warning written at
// exit). extra is the rest, trimmed, for the log. ok is false when no such
// value is there, and reply is then the output as it was, for the error.
func ScriptReply(out string) (reply, extra string, ok bool) {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return "", "", false
	}
	if json.Valid([]byte(trimmed)) {
		return trimmed, "", true
	}
	lines := strings.Split(trimmed, "\n")
	opens := func(l string) bool {
		l = strings.TrimSpace(l)
		return strings.HasPrefix(l, "{") || strings.HasPrefix(l, "[")
	}
	for i := len(lines) - 1; i > 0; i-- {
		if !opens(lines[i]) {
			continue
		}
		if tail := strings.TrimSpace(strings.Join(lines[i:], "\n")); json.Valid([]byte(tail)) {
			return tail, strings.TrimSpace(strings.Join(lines[:i], "\n")), true
		}
	}
	if opens(lines[0]) {
		for i := 1; i < len(lines); i++ {
			if head := strings.TrimSpace(strings.Join(lines[:i], "\n")); json.Valid([]byte(head)) {
				return head, strings.TrimSpace(strings.Join(lines[i:], "\n")), true
			}
		}
	}
	return trimmed, "", false
}
