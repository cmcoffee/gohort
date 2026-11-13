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
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// allowWord is one word of a command or a rule.
type allowWord struct {
	text   string
	raw    string // the word as written in the line, quotes and all
	op     bool   // a shell operator: ; && || | & > >> < 2> and the like
	quoted bool   // written in quotes in the command
	subst  bool   // carries $ or a backtick outside single quotes: it runs something
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
	start, i := 0, 0
	flush := func() {
		if have {
			out = append(out, allowWord{text: cur.String(), raw: cmd[start:i], quoted: quoted, subst: subst})
		}
		cur.Reset()
		have, quoted, subst = false, false, false
		start = i
	}
	for i < len(cmd) {
		c := cmd[i]
		if !have {
			start = i
		}
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			flush()
			i++
		case c == '\\' && i+1 < len(cmd) && cmd[i+1] == '\n':
			// A line continued onto the next: the shell drops both, so it
			// is no word at all (it was read as a word holding a newline,
			// which a rule written back out could never match).
			i += 2
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
		case placeholderAt(cmd[i:]) > 0:
			// A rule's placeholder is one word, not a redirect.
			n := placeholderAt(cmd[i:])
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
// (the first, and the first after each operator that starts a new one), the
// target of a redirect, and, under a wrapper, the program it runs.
//
// A wrapper (sudo, env, nice, timeout and the like, or a leading VAR=value)
// runs the program named after its own options, so that program is what the
// line runs: kept exact as the first word is. Held to the first word alone,
// Always on "sudo /opt/tool.py --help" suggested "sudo /opt/*.py --help",
// which runs any script in /opt as root. Under a wrapper every word stays
// exact through its options and their values, the program and any words
// after it, and ends at the first option that follows a word that is not one
// (the program's own flags); a wrapper inside a wrapper ("sudo nice -n 5
// tool") starts over. Holding too many words exact can only narrow a rule.
func heads(ws []allowWord) []bool {
	fixed := make([]bool, len(ws))
	start := true
	wrapped, sawWord := false, false
	for i, w := range ws {
		switch {
		case w.op:
			fixed[i] = true
			start = w.text != ">" && w.text != ">>" && w.text != "<" && w.text != "2>" && w.text != "2>>" && w.text != "&>"
			wrapped = false
			if !start && i+1 < len(ws) {
				fixed[i+1] = true // the file a redirect writes or reads
			}
		case start:
			fixed[i] = true
			start = false
			wrapped, sawWord = isAllowWrapper(w.text), false
		case wrapped:
			// <flags> in a rule stands for the program's own flags.
			flag := strings.HasPrefix(w.text, "-") || isFlagsPlaceholder(w)
			if flag && sawWord {
				wrapped = false
				continue
			}
			fixed[i] = true
			if !flag {
				sawWord = !isAllowWrapper(w.text)
			}
		}
	}
	return fixed
}

// allowWrappers run the program named after their own options.
var allowWrappers = map[string]bool{"sudo": true, "doas": true, "env": true, "nice": true, "nohup": true,
	"timeout": true, "command": true, "time": true, "stdbuf": true, "ionice": true}

// envAssignment is a leading VAR=value, which sets the program's environment
// and is followed by the program itself.
var envAssignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// isAllowWrapper reports whether word runs another program after it: a
// wrapper by name, wherever it is installed, or an environment assignment.
func isAllowWrapper(word string) bool {
	return allowWrappers[path.Base(word)] || envAssignment.MatchString(word)
}

// placeholderAt is the length of the rule placeholder s begins with, or 0:
// <number>, <text> and <flags> are each one word, not redirects.
func placeholderAt(s string) int {
	for _, p := range []string{"<number>", "<text>", flagsPlaceholder, argsPlaceholder} {
		if strings.HasPrefix(s, p) {
			return len(p)
		}
	}
	// <flags:--a,--b=*> and <word:x,y>: a list, written by the rule form,
	// read to its closing > as one word.
	if strings.HasPrefix(s, "<flags:") || strings.HasPrefix(s, "<word:") {
		end := strings.IndexByte(s, '>')
		if end > 0 && strings.Count(s[:end], "<") == 1 && !strings.ContainsAny(s[:end], " \t'\"$`") {
			return end + 1
		}
	}
	return 0
}

// isFlagsPlaceholder reports a rule word that stands for the command's flags:
// <flags> for any, <flags:...> for only those it lists.
func isFlagsPlaceholder(w allowWord) bool {
	return !w.op && !w.quoted && (w.text == flagsPlaceholder || w.text == argsPlaceholder || (strings.HasPrefix(w.text, "<flags:") && strings.HasSuffix(w.text, ">")))
}

// flagsSpec is what <flags:...> allows, by flag name: "" a flag alone, "*"
// one taking any value, "#" one taking a number. nil is <flags>, any flag.
func flagsSpec(w allowWord) (map[string]string, bool) {
	if w.text == flagsPlaceholder || w.text == argsPlaceholder {
		return nil, true
	}
	body := strings.TrimSuffix(strings.TrimPrefix(w.text, "<flags:"), ">")
	spec := map[string]string{}
	for _, e := range strings.Split(body, ",") {
		e = strings.TrimSpace(e)
		name, value, _ := strings.Cut(e, "=")
		if !flagName.MatchString(name) || (value != "" && value != "*" && value != "#") {
			return nil, false
		}
		spec[name] = value
	}
	return spec, len(spec) > 0
}

// flagName is a flag a rule may list: one or two dashes and a name.
var flagName = regexp.MustCompile(`^--?[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// flagsCover reports whether the command words from i on are flags the rule
// allows: any flag for <flags>, only the listed ones for <flags:...>. A listed
// flag taking a value has it after = or as the next word; the next word only
// for a flag the rule says takes one, so a stray argument cannot pass as a
// value. Never an operator, a word that runs something, one leaving its
// folder, or a word that must stay exact.
func flagsCover(spec map[string]string, cw []allowWord, fixed []bool, i int) bool {
	bad := func(j int) bool {
		c := cw[j]
		return c.op || c.subst || fixed[j] || flagLeavesFolder(c.text)
	}
	// Anything after it (<args>, and <flags>, which a person choosing "any
	// flags" read the same way): every further word, flags and arguments
	// alike. Never an operator, a word that runs something or one leaving its
	// folder; the program is matched exactly before this, so the words that
	// stay exact under a wrapper are already behind it.
	if spec == nil {
		for ; i < len(cw); i++ {
			if c := cw[i]; c.op || c.subst || flagLeavesFolder(c.text) {
				return false
			}
		}
		return true
	}
	for ; i < len(cw); i++ {
		if bad(i) || !strings.HasPrefix(cw[i].text, "-") {
			return false
		}
		name, value, hasValue := strings.Cut(cw[i].text, "=")
		want, listed := spec[name]
		if !listed {
			return false
		}
		switch {
		case want == "" && hasValue:
			return false
		case want == "":
		case hasValue:
			if !flagValueOK(want, value) {
				return false
			}
		default:
			// The value as the next word.
			if i+1 >= len(cw) || bad(i+1) || strings.HasPrefix(cw[i+1].text, "-") || !flagValueOK(want, cw[i+1].text) {
				return false
			}
			i++
		}
	}
	return true
}

// flagValueOK reports whether v is a value a listed flag takes: any for "*"
// (one word, not a path that leaves its folder), digits for "#".
func flagValueOK(want, v string) bool {
	if want == "#" {
		return allDigits.MatchString(v)
	}
	return v != "" && !hasDotDot(v)
}

// isWordChoice reports a rule word <word:a,b> that stands for one of the exact
// words it lists.
func isWordChoice(w allowWord) bool {
	return !w.op && !w.quoted && strings.HasPrefix(w.text, "<word:") && strings.HasSuffix(w.text, ">")
}

// wordChoices is the words <word:a,b> lists.
func wordChoices(w allowWord) []string {
	var out []string
	for _, x := range strings.Split(strings.TrimSuffix(strings.TrimPrefix(w.text, "<word:"), ">"), ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// argsPlaceholder, as a rule's last word, stands for anything after it: any
// number of further words, flags and arguments alike, none included. "sudo
// /opt/tool.py <args>" covers the program with whatever it is given, and
// nothing that is not part of that one command (no operator, no $ or
// backtick, no path leaving its folder).
//
// flagsPlaceholder is the same thing under the name it had first. It meant
// only words beginning with -, and "Any flags" chosen in the form was read as
// anything after the program: "--go status" was refused for its "status". A
// rule is what the person adding it meant, so the two read alike.
const (
	argsPlaceholder  = "<args>"
	flagsPlaceholder = "<flags>"
)

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
		general := ""
		switch {
		case w.op || fixed[i] || w.subst:
		case allDigits.MatchString(w.text):
			general = "<number>"
		case w.quoted && !strings.Contains(w.text, "/"):
			general = "<text>"
		case strings.Contains(w.text, "/") && strings.ContainsAny(w.text, " \t"):
			// a path with spaces stays as written
		case strings.Contains(w.text, "/"):
			general = ruleWord(generalizePath(w.text))
		case strings.HasPrefix(w.text, "-"):
			if k, v, has := strings.Cut(w.text, "="); has && volatileWord.MatchString(v) {
				general = ruleWord(k + "=*")
			}
		case volatileWord.MatchString(w.text):
			general = "*"
		}
		// A general form is kept only when it still allows the word it was
		// made from (a folder named [2024] turned [*], a character class,
		// and stopped matching itself). Otherwise the word stays as the
		// line wrote it, quotes and all, so it reads back as the same word:
		// rewritten in single quotes, a $(...) became plain text and no
		// longer matched the command.
		if general != "" {
			if gw, ok := splitAllowWords(general); ok && len(gw) == 1 && wordCovers(gw[0], w, fixed[i]) {
				out[i] = general
				continue
			}
		}
		out[i] = exactRuleWord(w)
	}
	rule := strings.Join(out, " ")
	// The suggestion always covers its own command: whatever else is wrong
	// with it, Always on an unedited card never answers that the rule does
	// not cover the command.
	if !matchAllowRule(rule, cmd) {
		return strings.TrimSpace(cmd)
	}
	return rule
}

// exactRuleWord is w written into a rule to match only itself: as the line
// wrote it, so quoting and substitution read back the same.
func exactRuleWord(w allowWord) string {
	if w.raw != "" {
		return w.raw
	}
	return ruleWord(w.text)
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
	if !ok || len(cw) == 0 {
		return false
	}
	// A wrapper's harmless options (sudo -n) are set aside on both sides:
	// an agent adds -n so sudo fails instead of waiting for a password, and
	// a rule written without it should still cover the command.
	rw, cw = withoutHarmlessWrapperOpts(rw), withoutHarmlessWrapperOpts(cw)
	if matchWords(rw, cw) {
		return true
	}
	// A rule run with plain sudo (or doas) also covers the same command run
	// without it, which is the same thing with less privilege. Not a rule
	// with sudo options of its own: "sudo -u svc x" is not "x" as you.
	if len(rw) > 1 && !rw[0].op && allowWrappers[path.Base(rw[0].text)] && (path.Base(rw[0].text) == "sudo" || path.Base(rw[0].text) == "doas") &&
		!rw[1].op && !strings.HasPrefix(rw[1].text, "-") && !cw[0].op && !isAllowWrapper(cw[0].text) {
		return matchWords(rw[1:], cw)
	}
	return false
}

// matchWords is matchAllowRule over words already split.
func matchWords(rw, cw []allowWord) bool {
	if len(cw) == 0 {
		return false
	}
	fixed := heads(cw)
	// Flags last: the words before them match one for one, and every word of
	// the command after them is a flag the rule allows (flagsCover).
	if n := len(rw); n > 0 && isFlagsPlaceholder(rw[n-1]) {
		spec, ok := flagsSpec(rw[n-1])
		if !ok || len(cw) < n-1 {
			return false
		}
		for i := 0; i < n-1; i++ {
			if !wordCovers(rw[i], cw[i], fixed[i]) {
				return false
			}
		}
		return flagsCover(spec, cw, fixed, n-1)
	}
	if len(rw) != len(cw) {
		return false
	}
	for i := range rw {
		if !wordCovers(rw[i], cw[i], fixed[i]) {
			return false
		}
	}
	return true
}

// wordCovers reports whether rule word r allows command word c, where fixed
// says c must be matched exactly (a program, or a redirect's target).
func wordCovers(r, c allowWord, fixed bool) bool {
	if r.op || c.op {
		if !(r.op && c.op && r.text == c.text) {
			return false
		}
		return true
	}
	if r.text == c.text && r.subst == c.subst {
		return true
	}
	// One of a list of exact words: the same as a rule per word, so it may
	// stand where an exact word must (a subcommand under sudo).
	if isWordChoice(r) {
		if c.subst || c.quoted {
			return false
		}
		for _, w := range wordChoices(r) {
			if w == c.text {
				return true
			}
		}
		return false
	}
	// Anything but an exact word is a placeholder or a glob, and neither
	// may stand for a program, a redirect target, or a word that runs
	// something or leaves its folder.
	if fixed || c.subst || hasDotDot(c.text) {
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
	return true
}

// flagLeavesFolder reports whether a flag steps out of its folder with ..,
// in its value (--path=../x) as well as in itself.
func flagLeavesFolder(flag string) bool {
	if _, v, ok := strings.Cut(flag, "="); ok && hasDotDot(v) {
		return true
	}
	return hasDotDot(flag)
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
	if ws, ok := splitAllowWords(rule); ok {
		if err := flagsLastOnly(ws); err != nil {
			return err
		}
		if err := flagsNameTheProgram(ws); err != nil {
			return err
		}
	}
	return nil
}

// harmlessWrapperOpts are a wrapper's options that change nothing about what
// runs or as whom: sudo's -n only makes it fail rather than ask for a
// password. -u, -E, -s and the rest change who runs what, and stay part of
// the command.
var harmlessWrapperOpts = map[string]map[string]bool{
	"sudo": {"-n": true, "--non-interactive": true},
	"doas": {"-n": true},
}

// withoutHarmlessWrapperOpts is ws with a leading wrapper's harmless options
// left out, among its own options only (before the program it runs).
func withoutHarmlessWrapperOpts(ws []allowWord) []allowWord {
	if len(ws) == 0 || ws[0].op {
		return ws
	}
	name := path.Base(ws[0].text)
	harmless := harmlessWrapperOpts[name]
	if harmless == nil {
		return ws
	}
	values := wrapperValueOpts[name]
	out := []allowWord{ws[0]}
	i := 1
	for i < len(ws) && !ws[i].op && strings.HasPrefix(ws[i].text, "-") {
		w := ws[i]
		if !w.quoted && harmless[w.text] {
			i++
			continue
		}
		out = append(out, w)
		if values[w.text] && i+1 < len(ws) {
			out = append(out, ws[i+1])
			i++
		}
		i++
	}
	return append(out, ws[i:]...)
}

// rulesFor is the Always rules saved for an appliance, sorted.
func rulesFor(udb Database, applianceID string) []string {
	if udb == nil || applianceID == "" {
		return nil
	}
	prefix := applianceID + "\x00"
	var out []string
	for _, k := range udb.Keys(alwaysAllowTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		var on bool
		if udb.Get(alwaysAllowTable, k, &on) && on {
			out = append(out, strings.TrimPrefix(k, prefix))
		}
	}
	sort.Strings(out)
	return out
}

// programOf is the program a line's first command runs: under a wrapper, the
// one it wraps; "" when it names none.
func programOf(ws []allowWord) string {
	if len(ws) == 0 || ws[0].op {
		return ""
	}
	if isAllowWrapper(ws[0].text) {
		if i := wrappedProgram(ws); i >= 0 {
			return ws[i].text
		}
		return ""
	}
	return ws[0].text
}

// nearMissRule is a rule for this appliance that names the program a refused
// command runs, anywhere in it, when no rule covers the command: the command
// was meant for the rule and missed its shape ("sudo python3 /opt/tool.py",
// "cd /opt && sudo /opt/tool.py" against "sudo /opt/tool.py <flags>"). "" when
// none does.
func nearMissRule(udb Database, applianceID, cmd string) string {
	cw, ok := splitAllowWords(cmd)
	if !ok {
		return ""
	}
	for _, rule := range rulesFor(udb, applianceID) {
		rw, ok := splitAllowWords(rule)
		if !ok {
			continue
		}
		prog := programOf(rw)
		if prog == "" {
			continue
		}
		for _, w := range cw {
			if !w.op && w.text == prog {
				return rule
			}
		}
	}
	return ""
}

// allowedCommandsCap bounds how many rules the investigator is told about.
const allowedCommandsCap = 40

// allowedCommandsBlock is what the investigator is told runs without asking
// on this system: its Always rules, and the kinds of command the acting agent
// (or the owner) has allowed. Told nothing, an agent wrote the command in a
// shape of its own ("sudo -n", an interpreter in front), was refused, and
// reported Servitor as locked down. Empty when nothing is allowed.
func allowedCommandsBlock(udb Database, agentID, applianceID string) string {
	rules := rulesFor(udb, applianceID)
	set, _ := ResolveCommandGrant(udb, agentID, applianceID)
	var cats []string
	for _, c := range []RiskCategory{RiskDataMutate, RiskFileDelete, RiskNetEgress, RiskSysControl, RiskPkgInstall, RiskUnverified} {
		if set[c] {
			cats = append(cats, riskCategoryWords[c])
		}
	}
	if len(rules) == 0 && len(cats) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## Commands that run without asking here\n\n")
	if len(rules) > 0 {
		b.WriteString("The owner allowed these on this system, and a command in one of these shapes runs without asking. " +
			"Write it in exactly that shape: the program as named, with nothing in front of it the rule does not have (no interpreter, no cd, no sudo option but -n), and only the words and flags the rule allows. " +
			"In a rule written out, <number> is any number, <text> any one word that is not a path, and * any characters within one word. " +
			"Other commands are judged as usual.\n")
		for i, r := range rules {
			if i == allowedCommandsCap {
				fmt.Fprintf(&b, "- and %d more\n", len(rules)-i)
				break
			}
			b.WriteString("- " + describeRule(r) + "\n")
		}
	}
	if len(cats) > 0 {
		b.WriteString("These kinds of command also run without asking here: " + strings.Join(cats, ", ") + ".\n")
	}
	return b.String()
}

// riskCategoryWords names each category as the Permissions page does.
var riskCategoryWords = map[RiskCategory]string{
	RiskDataMutate: "database writes", RiskFileDelete: "file changes", RiskNetEgress: "outbound calls",
	RiskSysControl: "system control", RiskPkgInstall: "software installs", RiskUnverified: "unrecognized commands",
}

// flagsLastOnly is an error when a rule has <flags> anywhere but as its last
// word, where it would stand for nothing the matcher reads.
func flagsLastOnly(ws []allowWord) error {
	for i, w := range ws {
		if isFlagsPlaceholder(w) && i != len(ws)-1 {
			return errors.New("<flags> can only be a rule's last word: it stands for whatever flags come after the rest")
		}
	}
	return nil
}

// validNewRule checks a rule typed into Permissions with no command behind
// it. It is one command, no operators; every word that must stay exact (the
// program, and the program under sudo or another wrapper) is written out, not
// a placeholder or a pattern; it has no $ or backtick; and <flags> comes only
// last.
func validNewRule(rule string) error {
	ws, ok := splitAllowWords(strings.TrimSpace(rule))
	if !ok || len(ws) == 0 {
		return errors.New("the rule could not be read: check its quotes")
	}
	if err := flagsLastOnly(ws); err != nil {
		return err
	}
	fixed := heads(ws)
	for i, w := range ws {
		if w.op {
			return errors.New("a rule added here is one command: no ; | && > or other operators (answer Always on a card for a longer line)")
		}
		if w.subst {
			return errors.New("a rule cannot contain $ or a backtick: they run commands of their own")
		}
		if fixed[i] && !w.quoted && !isWordChoice(w) && (w.text == "<number>" || w.text == "<text>" || isFlagsPlaceholder(w) || strings.ContainsAny(w.text, "*?[")) {
			return errors.New("write the program out in full, and the program it runs under sudo or another wrapper: \"" + w.text + "\" cannot stand for one")
		}
	}
	return flagsNameTheProgram(ws)
}

// wrapperValueOpts are the options of each wrapper that take the next word as
// their value ("sudo -u root"), so that word is not the program it runs.
var wrapperValueOpts = map[string]map[string]bool{
	"sudo":    {"-u": true, "-g": true, "-C": true, "-h": true, "-p": true, "-r": true, "-t": true, "-T": true, "-U": true, "-D": true, "-R": true},
	"doas":    {"-u": true, "-C": true},
	"nice":    {"-n": true},
	"ionice":  {"-c": true, "-n": true, "-p": true, "-P": true, "-u": true},
	"stdbuf":  {"-i": true, "-o": true, "-e": true},
	"timeout": {"-s": true, "-k": true},
	"env":     {"-u": true, "-C": true, "-S": true},
}

// wrappedProgram is the index of the program a line starting with a wrapper
// runs: past each wrapper's options and their values (and timeout's
// duration), through nested wrappers. -1 when it names none, as in "sudo -u
// root" with nothing after.
func wrappedProgram(ws []allowWord) int {
	i := 0
	for i < len(ws) && !ws[i].op && isAllowWrapper(ws[i].text) {
		name := path.Base(ws[i].text)
		values := wrapperValueOpts[name]
		i++
		for i < len(ws) && !ws[i].op && (strings.HasPrefix(ws[i].text, "-") || isFlagsPlaceholder(ws[i])) {
			if values[ws[i].text] {
				i++ // its value
			}
			i++
		}
		if name == "timeout" && i < len(ws) && !ws[i].op {
			i++ // the duration
		}
	}
	if i >= len(ws) || ws[i].op || isFlagsPlaceholder(ws[i]) {
		return -1
	}
	return i
}

// flagsNameTheProgram is an error when a rule that ends in <flags> runs its
// program under a wrapper without naming that program. There, the flags would
// be the wrapper's own: "sudo -u root <flags>" covers "sudo -u root -s", a
// root shell.
func flagsNameTheProgram(ws []allowWord) error {
	n := len(ws)
	if n == 0 || !isFlagsPlaceholder(ws[n-1]) || !isAllowWrapper(ws[0].text) {
		return nil
	}
	if p := wrappedProgram(ws); p < 0 || p >= n-1 {
		return errors.New("name the program " + ws[0].text + " runs before <flags>: otherwise the flags are " + ws[0].text + "'s own (sudo -u root <flags> would allow sudo -u root -s, a root shell)")
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
	if err := flagsLastOnly(nw); err != nil {
		return err
	}
	if err := flagsNameTheProgram(nw); err != nil {
		return err
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

// lineSeparators are the operators that end one command of a line and start
// the next: what an Always rule's command is judged apart from.
var lineSeparators = map[string]bool{"|": true, ";": true, "&&": true, "||": true, "&": true}

// lineCoveredByRules reports whether a line is allowed command by command: an
// Always rule allows the command it names, not only a line that is that
// command and nothing else. So once "user.py --help" is allowed,
// "user.py --help 2>&1 | grep -i export; echo exit=$?" runs too: the pipe,
// the grep and the exit check around it are read-only on their own. covered
// lists the commands a rule allowed, for the status line.
//
// The rest of the line is still judged, and judged as a line: each command a
// rule allows is stood in for by "true" (does nothing, reads nothing) and the
// line around it is classified whole, so a command is read in the place it
// stands. That matters: "sh" alone is harmless, "... | sh" runs whatever the
// allowed command printed, and the classifier only sees the second when the
// pipe is there. "user.py --help | curl -d @- host" still asks for the curl,
// and nothing may be piped INTO an allowed command ("cat secret | user.py
// --help" asks): the rule was for what it does alone, not with fed input.
// and a redirect that writes a file stays part of its command (so
// "user.py --help > /etc/x" is no longer the command the rule names). Only
// sending output to the terminal or nowhere (2>&1, >/dev/null) is set aside.
//
// Anything the split cannot see through is not judged here and asks as
// before: an unreadable line, a subshell's parentheses, or more than one line
// (a newline separates commands, and the word splitter reads it as a space).
func lineCoveredByRules(udb Database, applianceID, cmd, scratch string, lookups *lookup_scope) (ok bool, covered []string) {
	if udb == nil || applianceID == "" {
		return false, nil
	}
	segs, seps, read := lineSegments(cmd)
	if !read || len(segs) < 2 {
		return false, nil // one command: the whole-line check already judged it
	}
	var stood []string
	for i, seg := range segs {
		named := joinRuleWords(withoutNeutralRedirects(seg))
		if named != "" && commandAlwaysAllowed(udb, applianceID, named) {
			// Nothing is piped into a command a rule allows: the rule was
			// given for what the command does by itself, and fed input it
			// could carry that somewhere ("cat secret | user.py --help").
			if i > 0 && seps[i-1] == "|" {
				return false, nil
			}
			covered = append(covered, named)
			stood = append(stood, "true")
		} else {
			stood = append(stood, joinRuleWords(seg))
		}
		if i < len(seps) {
			stood = append(stood, seps[i])
		}
	}
	if len(covered) == 0 || len(assess_command(strings.Join(stood, " "), scratch, lookups)) != 0 {
		return false, nil
	}
	return true, covered
}

// lineSegments splits a shell line into its commands and the operators
// between them. ok false for a line the split cannot see through: unreadable,
// a subshell's parentheses, more than one line, or an empty command.
func lineSegments(cmd string) (segs [][]allowWord, seps []string, ok bool) {
	line := strings.ReplaceAll(cmd, "\\\n", " ")
	if strings.ContainsAny(line, "\n\r") {
		return nil, nil, false
	}
	ws, read := splitAllowWords(line)
	if !read || len(ws) == 0 {
		return nil, nil, false
	}
	var cur []allowWord
	for _, w := range ws {
		switch {
		case w.op && (w.text == "(" || w.text == ")"):
			return nil, nil, false
		case w.op && lineSeparators[w.text]:
			if len(cur) == 0 {
				return nil, nil, false
			}
			segs = append(segs, cur)
			seps = append(seps, w.text)
			cur = nil
		default:
			cur = append(cur, w)
		}
	}
	if len(cur) == 0 {
		return nil, nil, false
	}
	return append(segs, cur), seps, true
}

// coveredPart is, for a line a rule covers only part of, the commands the
// rules cover and the rest of the line: what an agent is told was the
// problem, so it can run the command without the part that needed approval
// ("the rule covers /opt/tool.py --go status; the rest, | curl ..., is an
// outbound call"). Both "" when no part, or every part, is covered.
func coveredPart(udb Database, applianceID, cmd string) (covered, rest string) {
	if udb == nil || applianceID == "" {
		return "", ""
	}
	segs, seps, ok := lineSegments(cmd)
	if !ok || len(segs) < 2 {
		return "", ""
	}
	var have, left []string
	for i, seg := range segs {
		named := joinRuleWords(withoutNeutralRedirects(seg))
		if named != "" && commandAlwaysAllowed(udb, applianceID, named) {
			have = append(have, named)
			continue
		}
		part := joinRuleWords(seg)
		if i > 0 {
			part = seps[i-1] + " " + part
		}
		left = append(left, part)
	}
	if len(have) == 0 || len(left) == 0 {
		return "", ""
	}
	return strings.Join(have, "; "), strings.Join(left, " ")
}

// withoutNeutralRedirects drops the redirects that only send a command's
// output to the terminal or nowhere: 2>&1, and >, 2>, &> or >> to /dev/null.
func withoutNeutralRedirects(ws []allowWord) []allowWord {
	var out []allowWord
	for i := 0; i < len(ws); i++ {
		w := ws[i]
		if w.op && w.text == "2>&1" {
			continue
		}
		if w.op && (w.text == ">" || w.text == "2>" || w.text == "&>" || w.text == ">>" || w.text == "2>>") &&
			i+1 < len(ws) && !ws[i+1].op && ws[i+1].text == "/dev/null" {
			i++
			continue
		}
		out = append(out, w)
	}
	return out
}

// joinRuleWords writes words back as one command, each as the line wrote it.
func joinRuleWords(ws []allowWord) string {
	parts := make([]string, len(ws))
	for i, w := range ws {
		parts[i] = exactRuleWord(w)
	}
	return strings.Join(parts, " ")
}

// alwaysCoveredNote says why a line ran without asking: the commands an
// Always rule allowed, the rest read-only.
func alwaysCoveredNote(covered []string) string {
	return "Always allowed: " + strings.Join(covered, ", ") + "; the rest only reads"
}
