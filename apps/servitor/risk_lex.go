package servitor

// A small shell lexer for the risk gate.
//
// The gate used to split a line on ";", "&&", "||" and "|" with strings.Split
// and read the first field of each piece as the program. Anything that did not
// fit that picture - a substitution, a heredoc, a quoted operator, an option
// value in front of the real command ("sudo -u root rm") - read as a harmless
// unknown and ran without asking. This lexer exists so the classifier can see
// the simple commands a line really runs, and so that when it CANNOT see them
// it says so, and the gate asks instead of guessing.
//
// It is not a shell. It understands enough to find every simple command, its
// redirections, and the bodies of $(...), `...`, <(...) and >(...), which run
// as commands too. Whatever it does not understand sets opaque, and an opaque
// line is never read-only.

import "strings"

// sh_word is one shell word after quote removal.
type sh_word struct {
	text string // the word with quotes removed; expansions are left as written
	raw  string // the source text, for display and assignment detection
	// expanded: part of the word is only decided at run time ($var, $(...),
	// `...`, $'...'). Fine as an argument to a read-only program; never fine
	// as the program name or as a path the gate is asked to vouch for.
	expanded bool
	// glob: an unquoted * ? [ { or a leading ~, which the shell rewrites.
	glob bool
}

// sh_redirect is one redirection on a simple command.
type sh_redirect struct {
	op     string // ">", ">>", ">|", "&>", "&>>", "<>", "<", "<<", "<<-", "<<<", ">&", "<&"
	target sh_word
}

// sh_command is one simple command: its words (assignments included) and its
// redirections, plus what feeds its stdin.
type sh_command struct {
	words     []sh_word
	redirects []sh_redirect
	piped_in  bool   // stdin is the previous command's output
	stdin     string // heredoc / herestring body, when there is one
	has_stdin bool   // a heredoc, herestring or "< file" feeds stdin
}

// sh_parse is what the lexer made of one line.
type sh_parse struct {
	cmds   []sh_command
	nested []string // bodies of $(...), `...`, <(...), >(...): they run too
	opaque string   // why the line could not be read; "" when it could
}

type pending_heredoc struct {
	delim  string
	strip  bool // <<- strips leading tabs from the body and the delimiter line
	quoted bool // a quoted delimiter turns off expansion inside the body
	owner  int  // index in out.cmds of the command the body feeds
}

type sh_lexer struct {
	s         string
	i         int
	out       sh_parse
	cur       sh_command
	word      strings.Builder
	w         sh_word
	in_word   bool
	raw_start int
	op        string // redirect operator waiting for its target word
	heredocs  []pending_heredoc
}

// sh_lex splits a shell line into simple commands. It never fails: what it
// cannot read is reported through opaque, and the gate treats that as a
// command it could not verify.
func sh_lex(s string) sh_parse {
	l := &sh_lexer{s: s}
	l.run()
	return l.out
}

func (l *sh_lexer) fail(why string) {
	if l.out.opaque == "" {
		l.out.opaque = why
	}
	l.i = len(l.s)
}

func (l *sh_lexer) run() {
	s := l.s
	for l.i < len(s) {
		c := s[l.i]
		switch {
		case c == '\\':
			if l.i+1 < len(s) && s[l.i+1] == '\n' {
				l.i += 2 // line continuation
				continue
			}
			l.start_word()
			if l.i+1 < len(s) {
				l.word.WriteByte(s[l.i+1])
				l.i += 2
			} else {
				l.i++
			}
		case c == '\'':
			l.start_word()
			j := strings.IndexByte(s[l.i+1:], '\'')
			if j < 0 {
				l.fail("has an unterminated quote")
				return
			}
			l.word.WriteString(s[l.i+1 : l.i+1+j])
			l.i += j + 2
		case c == '"':
			l.start_word()
			l.double_quoted()
		case c == '`':
			l.start_word()
			l.backtick()
		case c == '$':
			l.start_word()
			l.dollar()
		case c == ' ' || c == '\t' || c == '\r':
			l.end_word()
			l.i++
		case c == '\n':
			l.end_command(false)
			l.i++
			l.read_heredocs()
		case c == '#' && !l.in_word:
			for l.i < len(s) && s[l.i] != '\n' {
				l.i++
			}
		case c == ';':
			l.end_command(false)
			l.i++
			for l.i < len(s) && (s[l.i] == ';' || s[l.i] == '&') {
				l.i++ // ";;" and ";&" end a case arm
			}
		case c == '&':
			switch {
			case l.i+1 < len(s) && s[l.i+1] == '&':
				l.end_command(false)
				l.i += 2
			case l.i+1 < len(s) && s[l.i+1] == '>':
				l.end_word()
				op := "&>"
				l.i += 2
				if l.i < len(s) && s[l.i] == '>' {
					op = "&>>"
					l.i++
				}
				l.set_op(op)
			default:
				l.end_command(false) // background: the next command runs as well
				l.i++
			}
		case c == '|':
			if l.i+1 < len(s) && s[l.i+1] == '|' {
				l.end_command(false)
				l.i += 2
				continue
			}
			if l.i+1 < len(s) && s[l.i+1] == '&' {
				l.i++
			}
			l.end_command(true)
			l.i++
		case c == '(':
			if l.in_word {
				l.fail("uses ( inside a word (a function definition or an array)")
				return
			}
			if len(l.cur.words) > 0 {
				l.fail("has a ( where a command argument was expected")
				return
			}
			// A subshell or group: its contents are ordinary commands.
			l.end_command(false)
			l.i++
		case c == ')':
			l.end_command(false)
			l.i++
		case c == '<' || c == '>':
			l.redirect()
		default:
			l.start_word()
			switch c {
			case '*', '?', '[', '{':
				l.w.glob = true
			case '~':
				if l.word.Len() == 0 {
					l.w.glob = true
				}
			}
			l.word.WriteByte(c)
			l.i++
		}
	}
	l.end_command(false)
	if l.op != "" {
		l.fail("has a redirect with no target")
	}
}

func (l *sh_lexer) start_word() {
	if !l.in_word {
		l.in_word = true
		l.raw_start = l.i
	}
}

// end_word finishes the word being built: it becomes the target of a pending
// redirect, or the next word of the command.
func (l *sh_lexer) end_word() {
	if !l.in_word {
		return
	}
	w := l.w
	w.text = l.word.String()
	end := l.i
	if end > len(l.s) {
		end = len(l.s)
	}
	w.raw = l.s[l.raw_start:end]
	l.word.Reset()
	l.w = sh_word{}
	l.in_word = false
	if l.op == "" {
		l.cur.words = append(l.cur.words, w)
		return
	}
	op := l.op
	l.op = ""
	l.cur.redirects = append(l.cur.redirects, sh_redirect{op: op, target: w})
	switch op {
	case "<<", "<<-":
		l.heredocs = append(l.heredocs, pending_heredoc{
			delim:  w.text,
			strip:  op == "<<-",
			quoted: strings.ContainsAny(w.raw, "'\"\\"),
			owner:  len(l.out.cmds),
		})
		l.cur.has_stdin = true
	case "<<<":
		l.cur.stdin += w.text + "\n"
		l.cur.has_stdin = true
	case "<":
		l.cur.has_stdin = true
	}
}

// end_command closes the current simple command. piped marks the NEXT one as
// reading this one's output.
func (l *sh_lexer) end_command(piped bool) {
	l.end_word()
	if l.op != "" {
		l.fail("has a redirect with no target")
		return
	}
	if len(l.cur.words) > 0 || len(l.cur.redirects) > 0 {
		l.out.cmds = append(l.out.cmds, l.cur)
	}
	l.cur = sh_command{piped_in: piped}
}

func (l *sh_lexer) set_op(op string) {
	if l.op != "" {
		l.fail("has two redirects in a row")
		return
	}
	l.op = op
}

// redirect reads a < or > operator. A bare descriptor number written against
// it ("2>") belongs to the operator, not to the command.
func (l *sh_lexer) redirect() {
	s := l.s
	c := s[l.i]
	if l.in_word && isAllDigits(l.word.String()) && !strings.ContainsAny(l.s[l.raw_start:l.i], "'\"\\$") {
		l.word.Reset()
		l.w = sh_word{}
		l.in_word = false
	} else {
		l.end_word()
	}
	next := func(k int) byte {
		if l.i+k < len(s) {
			return s[l.i+k]
		}
		return 0
	}
	// <(...) and >(...) are process substitutions: an ARGUMENT naming a pipe
	// to a command that runs alongside, not a redirection.
	if next(1) == '(' {
		j := find_close(s, l.i+1)
		if j < 0 {
			l.fail("has an unterminated process substitution")
			return
		}
		l.out.nested = append(l.out.nested, s[l.i+2:j])
		l.start_word()
		l.w.expanded = true
		l.word.WriteString("/dev/fd/63")
		l.i = j + 1
		return
	}
	var op string
	if c == '>' {
		switch next(1) {
		case '>':
			op = ">>"
		case '|':
			op = ">|"
		case '&':
			op = ">&"
		default:
			op = ">"
		}
	} else {
		switch {
		case next(1) == '<' && next(2) == '<':
			op = "<<<"
		case next(1) == '<' && next(2) == '-':
			op = "<<-"
		case next(1) == '<':
			op = "<<"
		case next(1) == '>':
			op = "<>"
		case next(1) == '&':
			op = "<&"
		default:
			op = "<"
		}
	}
	l.i += len(op)
	l.set_op(op)
}

// double_quoted reads a "..." section into the current word. $ and backticks
// still expand inside double quotes, which is the case the old tokenizer
// missed: "$(rm -rf x)" is a command, quoted or not.
func (l *sh_lexer) double_quoted() {
	s := l.s
	l.i++
	for l.i < len(s) {
		c := s[l.i]
		switch c {
		case '"':
			l.i++
			return
		case '\\':
			if l.i+1 < len(s) {
				switch s[l.i+1] {
				case '$', '`', '"', '\\':
					l.word.WriteByte(s[l.i+1])
					l.i += 2
					continue
				case '\n':
					l.i += 2
					continue
				}
			}
			l.word.WriteByte(c)
			l.i++
		case '$':
			l.dollar()
			if l.out.opaque != "" {
				return
			}
		case '`':
			l.backtick()
			if l.out.opaque != "" {
				return
			}
		default:
			l.word.WriteByte(c)
			l.i++
		}
	}
	l.fail("has an unterminated quote")
}

// dollar reads one $ expansion into the current word.
func (l *sh_lexer) dollar() {
	s := l.s
	l.w.expanded = true
	next := byte(0)
	if l.i+1 < len(s) {
		next = s[l.i+1]
	}
	switch next {
	case '(':
		j := find_close(s, l.i+1)
		if j < 0 {
			l.fail("has an unterminated $(")
			return
		}
		body := s[l.i+2 : j]
		if strings.HasPrefix(body, "(") && strings.HasSuffix(body, ")") {
			// $(( arithmetic )). Only a command substitution hidden inside
			// it would run anything, and that is not worth reading through.
			if strings.Contains(body, "$(") || strings.Contains(body, "`") {
				l.fail("hides a command inside arithmetic")
				return
			}
		} else {
			l.out.nested = append(l.out.nested, body)
		}
		l.word.WriteString(s[l.i : j+1])
		l.i = j + 1
	case '{':
		j := strings.IndexByte(s[l.i:], '}')
		if j < 0 {
			l.fail("has an unterminated ${")
			return
		}
		body := s[l.i : l.i+j+1]
		if strings.Contains(body, "$(") || strings.Contains(body, "`") {
			l.fail("hides a command inside ${...}")
			return
		}
		l.word.WriteString(body)
		l.i += j + 1
	case '\'':
		// $'...' decodes escapes, so the text is not what it looks like.
		k := l.i + 2
		for k < len(s) && s[k] != '\'' {
			if s[k] == '\\' {
				k++
			}
			k++
		}
		if k >= len(s) {
			l.fail("has an unterminated quote")
			return
		}
		l.word.WriteString(s[l.i+2 : k])
		l.i = k + 1
	case '"':
		l.i++
		l.double_quoted()
	default:
		l.word.WriteByte('$')
		l.i++
	}
}

// backtick reads a `...` command substitution.
func (l *sh_lexer) backtick() {
	s := l.s
	var body strings.Builder
	k := l.i + 1
	for k < len(s) && s[k] != '`' {
		if s[k] == '\\' && k+1 < len(s) {
			k++
		}
		body.WriteByte(s[k])
		k++
	}
	if k >= len(s) {
		l.fail("has an unterminated backtick")
		return
	}
	l.out.nested = append(l.out.nested, body.String())
	l.w.expanded = true
	l.word.WriteString(s[l.i : k+1])
	l.i = k + 1
}

// read_heredocs consumes the bodies of the heredocs opened on the line that
// just ended. A heredoc body is data, not commands, unless its delimiter was
// unquoted and the body carries a substitution, which the shell would run.
func (l *sh_lexer) read_heredocs() {
	for _, h := range l.heredocs {
		var body strings.Builder
		for l.i < len(l.s) {
			end := strings.IndexByte(l.s[l.i:], '\n')
			var line string
			if end < 0 {
				line = l.s[l.i:]
				l.i = len(l.s)
			} else {
				line = l.s[l.i : l.i+end]
				l.i += end + 1
			}
			cmp := line
			if h.strip {
				cmp = strings.TrimLeft(cmp, "\t")
			}
			if cmp == h.delim {
				break
			}
			body.WriteString(line)
			body.WriteByte('\n')
		}
		text := body.String()
		if !h.quoted && (strings.Contains(text, "$(") || strings.Contains(text, "`")) {
			l.fail("runs commands inside a heredoc body")
			return
		}
		if h.owner < len(l.out.cmds) {
			l.out.cmds[h.owner].stdin += text
		}
	}
	l.heredocs = nil
}

// find_close returns the index of the ) matching the ( at s[open], respecting
// quotes and nesting, or -1.
func find_close(s string, open int) int {
	depth := 0
	for k := open; k < len(s); k++ {
		switch s[k] {
		case '\\':
			k++
		case '\'':
			j := strings.IndexByte(s[k+1:], '\'')
			if j < 0 {
				return -1
			}
			k += j + 1
		case '"':
			k++
			for k < len(s) && s[k] != '"' {
				if s[k] == '\\' {
					k++
				}
				k++
			}
			if k >= len(s) {
				return -1
			}
		case '`':
			j := strings.IndexByte(s[k+1:], '`')
			if j < 0 {
				return -1
			}
			k += j + 1
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return k
			}
		}
	}
	return -1
}

// isAllDigits reports whether s is a non-empty run of ASCII digits - used to
// recognize the file-descriptor prefix in "2>".
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// is_assignment reports whether w is a NAME=value prefix. Judged on the raw
// text, so a quoted "A=b" is the command it looks like, not an assignment.
func is_assignment(w sh_word) bool {
	eq := strings.IndexByte(w.raw, '=')
	if eq <= 0 {
		return false
	}
	name := strings.TrimSuffix(w.raw[:eq], "+")
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// assignment_name returns NAME from a NAME=value word.
func assignment_name(w sh_word) string {
	eq := strings.IndexByte(w.raw, '=')
	if eq <= 0 {
		return ""
	}
	return strings.TrimSuffix(w.raw[:eq], "+")
}
