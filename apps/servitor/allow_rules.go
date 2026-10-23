package servitor

// Always-allow rules: what an approval card's Always saves.
//
// It saved the exact command, so a command naming a file, a date, a count or a
// run's id never came round again and the answer never applied: the
// Permissions list filled with one-offs and every like command still asked.
// Always now saves a RULE worked out from the command (suggestAllowRule), shown
// on the card to edit before it is saved:
//
//	tail -n 200 /var/log/app/2026-10-22.log  ->  tail -n <number> /var/log/app/*.log
//
// A rule is the command's words, each one either exact or a placeholder:
//
//	<number>  one word of digits
//	<text>    any one word without a slash (a path needs a path pattern)
//	a * in a word, matched as a glob that stays inside its folder (no /)
//
// Matching is word by word and cannot be talked into more than the rule says:
// the same number of words; shell operators (; && || | > < & and the rest) only
// match themselves; a placeholder or a * never matches a word carrying $ or a
// backtick, which run commands of their own, or one stepping out of its folder
// with ..; and the word that names a program (the first of each command in a
// pipeline) and the target of a redirect are always exact. An existing exact
// allow is a rule with no placeholders, so it keeps working as written.

import (
	"errors"
	"path"
	"regexp"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// allowWord is one word of a command or a rule.
type allowWord struct {
	text   string
	op     bool // a shell operator: ; && || | & > >> < 2> and the like
	quoted bool // written in quotes in the command
	subst  bool // carries $ or a backtick outside single quotes: it runs something
}

// allowOps are the operators a command line splits on, longest first.
var allowOps = []string{"&&", "||", ">>", "2>&1", "2>>", "2>", "&>", ";", "|", "&", ">", "<", "(", ")"}

// splitAllowWords splits a command line the way a shell reads its words:
// quotes group, a backslash escapes, operators stand alone. It reports false
// for a line it cannot read (an unclosed quote), which then matches nothing
// but itself exactly.
func splitAllowWords(cmd string) ([]allowWord, bool) {
	var out []allowWord
	var cur strings.Builder
	have, quoted, subst := false, false, false
	flush := func() {
		if have {
			out = append(out, allowWord{text: cur.String(), quoted: quoted, subst: subst})
		}
		cur.Reset()
		have, quoted, subst = false, false, false
	}
	for i := 0; i < len(cmd); {
		c := cmd[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			flush()
			i++
		case c == '\\' && i+1 < len(cmd):
			cur.WriteByte(cmd[i+1])
			have = true
			i += 2
		case c == '\'':
			j := strings.IndexByte(cmd[i+1:], '\'')
			if j < 0 {
				return nil, false
			}
			cur.WriteString(cmd[i+1 : i+1+j])
			have, quoted = true, true
			i += j + 2
		case c == '"':
			j := i + 1
			for j < len(cmd) && cmd[j] != '"' {
				if cmd[j] == '\\' && j+1 < len(cmd) {
					j++
				}
				j++
			}
			if j >= len(cmd) {
				return nil, false
			}
			inner := cmd[i+1 : j]
			if strings.ContainsAny(inner, "$`") {
				subst = true
			}
			cur.WriteString(inner)
			have, quoted = true, true
			i = j + 1
		case strings.HasPrefix(cmd[i:], "<number>") || strings.HasPrefix(cmd[i:], "<text>"):
			// A rule's placeholder is one word, not a redirect.
			n := len("<text>")
			if strings.HasPrefix(cmd[i:], "<number>") {
				n = len("<number>")
			}
			cur.WriteString(cmd[i : i+n])
			have = true
			i += n
		case (c == '$' && i+1 < len(cmd) && cmd[i+1] == '(') || c == '`':
			// A substitution is one word that runs something: kept whole, so
			// it can only ever match itself.
			end := substEnd(cmd, i)
			if end < 0 {
				return nil, false
			}
			cur.WriteString(cmd[i:end])
			have, subst = true, true
			i = end
		default:
			if op := opAt(cmd, i); op != "" {
				flush()
				out = append(out, allowWord{text: op, op: true})
				i += len(op)
				continue
			}
			if c == '$' || c == '`' {
				subst = true
			}
			cur.WriteByte(c)
			have = true
			i++
		}
	}
	flush()
	return out, true
}

// substEnd is the index just past the substitution starting at i: $( to its
// matching ), or a backtick to the next one. -1 when it never closes.
func substEnd(s string, i int) int {
	if s[i] == '`' {
		j := strings.IndexByte(s[i+1:], '`')
		if j < 0 {
			return -1
		}
		return i + j + 2
	}
	depth := 0
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return j + 1
			}
		}
	}
	return -1
}

func opAt(s string, i int) string {
	for _, op := range allowOps {
		if strings.HasPrefix(s[i:], op) {
			return op
		}
	}
	return ""
}

// heads marks the words that must stay exact: the program word of each command
// (the first, and the first after each operator that starts a new one) and the
// target of a redirect.
func heads(ws []allowWord) []bool {
	fixed := make([]bool, len(ws))
	start := true
	for i, w := range ws {
		switch {
		case w.op:
			fixed[i] = true
			start = w.text != ">" && w.text != ">>" && w.text != "<" && w.text != "2>" && w.text != "2>>" && w.text != "&>"
			if !start && i+1 < len(ws) {
				fixed[i+1] = true // the file a redirect writes or reads
			}
		case start:
			fixed[i] = true
			start = false
		}
	}
	return fixed
}

var (
	allDigits = regexp.MustCompile(`^[0-9]+$`)
	// A word that changes from one run to the next: a date, a time, a uuid, a
	// long hex id, a number with a unit.
	volatileWord = regexp.MustCompile(`(?i)^(\d{4}-\d{2}-\d{2}([t _]\d{2}:\d{2}(:\d{2})?)?|\d{2}:\d{2}(:\d{2})?|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|[0-9a-f]{12,}|\d+[kmgtsh]b?)$`)
	// A run of digits, a uuid or a date inside a longer word: worker-3,
	// app-2026-10-22, servitor-<uuid>.
	volatilePart = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|\d{4}-\d{2}-\d{2}|\d+`)
)

// suggestAllowRule works a rule out of a command: the program, its flags and
// its plain words exact; a number, a date or an id made a placeholder; a file
// kept to its folder and extension with its name a wildcard; quoted text any
// text. A command it cannot read is suggested as written.
func suggestAllowRule(cmd string) string {
	ws, ok := splitAllowWords(cmd)
	if !ok || len(ws) == 0 {
		return strings.TrimSpace(cmd)
	}
	fixed := heads(ws)
	out := make([]string, len(ws))
	for i, w := range ws {
		switch {
		case w.op || fixed[i] || w.subst:
			out[i] = ruleWord(w.text)
		case allDigits.MatchString(w.text):
			out[i] = "<number>"
		case w.quoted && !strings.Contains(w.text, "/"):
			out[i] = "<text>"
		case strings.Contains(w.text, "/") && strings.ContainsAny(w.text, " \t"):
			out[i] = ruleWord(w.text) // a path with spaces stays as written
		case strings.Contains(w.text, "/"):
			out[i] = ruleWord(generalizePath(w.text))
		case strings.HasPrefix(w.text, "-"):
			if k, v, has := strings.Cut(w.text, "="); has && volatileWord.MatchString(v) {
				out[i] = ruleWord(k + "=*")
			} else {
				out[i] = ruleWord(w.text)
			}
		case volatileWord.MatchString(w.text):
			out[i] = "*"
		default:
			out[i] = ruleWord(w.text)
		}
	}
	return strings.Join(out, " ")
}

// generalizePath keeps a path's folders, made general where a folder's name
// carries a number, a date or an id, and makes its file name a wildcard that
// keeps the extension.
func generalizePath(p string) string {
	dir, file := path.Split(p)
	var segs []string
	for _, s := range strings.Split(strings.TrimSuffix(dir, "/"), "/") {
		if s != "" && s != "." && s != ".." && volatilePart.MatchString(s) {
			s = volatilePart.ReplaceAllString(s, "*")
		}
		segs = append(segs, s)
	}
	dir = strings.Join(segs, "/")
	if dir != "" || strings.HasPrefix(p, "/") {
		dir += "/"
	}
	if file == "" {
		return dir
	}
	if ext := path.Ext(file); ext != "" && ext != file {
		return dir + "*" + ext
	}
	return dir + "*"
}

// ruleWord writes a word into a rule, quoted when it needs to be read back as
// one word.
func ruleWord(s string) string {
	if s == "" {
		return "''"
	}
	if strings.ContainsAny(s, " \t'\"\\") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return s
}

// matchAllowRule reports whether cmd is a command rule allows.
func matchAllowRule(rule, cmd string) bool {
	if strings.TrimSpace(rule) == strings.TrimSpace(cmd) {
		return true
	}
	rw, ok := splitAllowWords(rule)
	if !ok {
		return false
	}
	cw, ok := splitAllowWords(cmd)
	if !ok || len(rw) != len(cw) || len(cw) == 0 {
		return false
	}
	fixed := heads(cw)
	for i := range rw {
		r, c := rw[i], cw[i]
		if r.op || c.op {
			if !(r.op && c.op && r.text == c.text) {
				return false
			}
			continue
		}
		if r.text == c.text && r.subst == c.subst {
			continue
		}
		// Anything but an exact word is a placeholder or a glob, and neither
		// may stand for a program, a redirect target, or a word that runs
		// something or leaves its folder.
		if fixed[i] || c.subst || hasDotDot(c.text) {
			return false
		}
		switch {
		case !r.quoted && r.text == "<number>":
			if !allDigits.MatchString(c.text) {
				return false
			}
		case !r.quoted && r.text == "<text>":
			// Any word, but not a path: <text> stands for what a command
			// searches for or names, and a path reaching anywhere on the
			// system is what a path pattern is for.
			if strings.Contains(c.text, "/") {
				return false
			}
		case !r.quoted && strings.Contains(r.text, "*"):
			if m, err := path.Match(r.text, c.text); err != nil || !m {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func hasDotDot(s string) bool {
	for _, seg := range strings.Split(s, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// validAllowRule checks a rule someone edited before it is saved: it has to
// still allow the command it was made from, which also keeps its program and
// its shape, and it may not leave every word of a command open.
func validAllowRule(rule, cmd string) error {
	rule = strings.TrimSpace(rule)
	if rule == "" {
		return errors.New("the rule is empty")
	}
	if !matchAllowRule(rule, cmd) {
		return errors.New("that rule does not cover the command it was made from: keep its program and its words, and use <number>, <text> or * only where the command varies")
	}
	return nil
}

// validEditedRule checks a saved rule edited in Permissions. There is no
// command to hold it to, so it keeps the old rule's shape: the same number of
// words, the same operators, and the same program and redirect targets, with
// only the other words free to narrow or widen.
func validEditedRule(old, rule string) error {
	ow, ok := splitAllowWords(old)
	if !ok {
		return errors.New("the saved rule could not be read")
	}
	nw, ok := splitAllowWords(strings.TrimSpace(rule))
	if !ok || len(nw) == 0 {
		return errors.New("the rule could not be read: check its quotes")
	}
	if len(nw) != len(ow) {
		return errors.New("an edit keeps the rule's words: change what they say, not how many there are")
	}
	fixed := heads(ow)
	for i := range ow {
		if ow[i].op != nw[i].op || (ow[i].op && ow[i].text != nw[i].text) {
			return errors.New("an edit keeps the rule's operators (; | && and the like) where they are")
		}
		if fixed[i] && ow[i].text != nw[i].text {
			return errors.New("an edit keeps the program each command runs, and any file a redirect writes, as they are")
		}
	}
	return nil
}

// commandAlwaysAllowed reports whether cmd runs on applianceID without asking, by an
// Always answer saved in udb.
func commandAlwaysAllowed(udb Database, applianceID, cmd string) bool {
	if udb == nil {
		return false
	}
	get := func(k string) bool {
		var on bool
		return udb.Get(alwaysAllowTable, k, &on) && on
	}
	return allowedByRule(get, udb.Keys(alwaysAllowTable), applianceID, cmd)
}

// allowedByRule reports whether cmd is allowed on appliance by an Always
// answer: one saved for exactly this command, or a rule that matches it.
func allowedByRule(get func(key string) bool, keys []string, applianceID, cmd string) bool {
	if get(alwaysAllowKey(applianceID, cmd)) {
		return true
	}
	prefix := applianceID + "\x00"
	for _, k := range keys {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		if rule := strings.TrimPrefix(k, prefix); matchAllowRule(rule, cmd) && get(k) {
			return true
		}
	}
	return false
}
