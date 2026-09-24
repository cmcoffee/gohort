package servitor

// run_pty input is typed into whatever the command opened, and has to be
// judged in THAT language. The old gate read every line as a shell command,
// so "psql mydb" with input "DELETE FROM sessions;" classified the DELETE as
// an unknown shell word, called it harmless, and the write ran unprompted -
// the run_command path gated the same statement given with -c.
//
// Each line is now classified by the session it lands in: SQL for a SQL
// client, redis commands for redis-cli, mongo script for mongosh, shell lines
// for a shell (or su/ssh). Input to anything else - a Python REPL, an editor,
// a pager - cannot be read, so it asks.
//
// The one line that is let through unread is a password: the first line(s),
// when the command is one that prompts for a password, and only when the line
// is a single bare word that is not a program the gate knows. Showing it in a
// confirmation card would put the secret on screen and in the session's event
// buffer. A bare word typed into a shell that did NOT prompt would run as a
// command; the "not a known program" test is what keeps "reboot" or
// "systemctl" from sliding through that way.

import (
	"strings"
)

// pty_line_risk is one input line that needs the gate, with what it found.
type pty_line_risk struct {
	line string
	hits []risk_hit
}

// pty_session describes what a run_pty command opens.
type pty_session struct {
	kind      string // "shell", "sql", "redis", "mongo" or "opaque"
	program   string // the interactive program's name
	passwords int    // how many leading lines may be password answers
}

// pty_input_risks classifies each non-empty input line of a run_pty call and
// returns the ones that are not read-only. Password answers are skipped.
func pty_input_risks(cmd, input, scratch string) []pty_line_risk {
	sess := pty_session_of(cmd)
	var out []pty_line_risk
	passwords := sess.passwords
	for _, raw := range strings.Split(input, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if passwords > 0 {
			passwords--
			if looks_like_password(line) {
				continue
			}
		}
		rc := &risk_ctx{scratch: scratch}
		switch sess.kind {
		case "shell":
			rc.line(line)
		case "sql":
			rc.sql_risk(sess.program, line)
		case "redis":
			rc.redis_command(strings.Fields(line))
		case "mongo":
			if line != "exit" && line != "quit" && !strings.HasPrefix(line, "show ") && !strings.HasPrefix(line, "use ") {
				rc.mongo_risk(line)
			}
		default:
			rc.unverified("input typed into an interactive " + sess.program + " session, which the gate cannot read")
		}
		if len(rc.hits) > 0 {
			out = append(out, pty_line_risk{line: line, hits: rc.hits})
		}
	}
	return out
}

// looks_like_password: one bare word, no shell syntax, and not the name of
// anything the gate knows how to run.
func looks_like_password(line string) bool {
	if strings.ContainsAny(line, " \t;|&<>$`()'\"\\/") {
		return false
	}
	if _, known := risk_rules[line]; known || read_only_cmds[line] || shell_keywords[line] {
		return false
	}
	return true
}

// pty_session_of finds the program whose terminal the input reaches: the last
// simple command on the line, looked through the usual wrappers.
func pty_session_of(cmd string) pty_session {
	p := sh_lex(cmd)
	if p.opaque != "" || len(p.cmds) == 0 {
		return pty_session{kind: "opaque", program: "unknown"}
	}
	words := p.cmds[len(p.cmds)-1].words
	for len(words) > 0 && is_assignment(words[0]) {
		words = words[1:]
	}
	sess := pty_session{}
	for len(words) > 0 {
		name, ok := program_name(words[0].text)
		if !ok || words[0].expanded {
			return pty_session{kind: "opaque", program: words[0].text}
		}
		pa := parse_args(words[1:], "-u", "-g", "-h", "-p", "-C", "-D", "-r", "-t", "-T", "-U", "-n", "-c", "-s")
		switch name {
		case "sudo", "doas":
			if !pa.has("-n", "--non-interactive") {
				sess.passwords++
			}
			if pa.has("-i", "-s", "--login", "--shell") && len(pa.ops) == 0 {
				sess.kind, sess.program = "shell", name
				return sess
			}
			words = skip_wrapper(words[1:])
			continue
		case "env", "nice", "nohup", "stdbuf", "ionice", "time", "timeout", "command", "exec", "setsid", "unbuffer":
			words = skip_wrapper(words[1:])
			if name == "timeout" && len(words) > 0 {
				words = words[1:] // the duration
			}
			for len(words) > 0 && is_assignment(words[0]) {
				words = words[1:]
			}
			continue
		case "su", "login", "runuser":
			if name != "runuser" {
				sess.passwords++
			}
			sess.kind, sess.program = "shell", name
			return sess
		case "ssh":
			sess.passwords++
			sess.kind, sess.program = "shell", name
			return sess
		}
		sess.program = name
		switch {
		case shells[name]:
			sess.kind = "shell"
		case sql_clients[name].sql_opts != nil:
			sess.kind = "sql"
			if pa.has("-W", "--password") || (sql_clients[name].password_op && has_bare_password_flag(words[1:])) {
				sess.passwords++
			}
		case name == "redis-cli":
			sess.kind = "redis"
			if pa.has("--askpass") {
				sess.passwords++
			}
		case name == "mongo" || name == "mongosh":
			sess.kind = "mongo"
			if pa.has("-p", "--password") {
				sess.passwords++
			}
		default:
			sess.kind = "opaque"
		}
		return sess
	}
	return pty_session{kind: "shell", program: "sh"}
}

// skip_wrapper drops a wrapper's own options so the next word is the program.
// Approximate on purpose: it only decides which language input is read in,
// and every language's fallback is to ask.
func skip_wrapper(words []sh_word) []sh_word {
	value := set_of("-u", "-g", "-h", "-p", "-C", "-D", "-r", "-t", "-T", "-U", "-n", "-c", "-k", "-s", "-i", "-o", "-e", "-a")
	for len(words) > 0 {
		t := words[0].text
		if t == "--" {
			return words[1:]
		}
		if !strings.HasPrefix(t, "-") {
			return words
		}
		words = words[1:]
		if value[t] && len(words) > 0 && !strings.HasPrefix(words[0].text, "-") && !looks_like_program(words[0].text) {
			words = words[1:]
		}
	}
	return words
}

// looks_like_program: a word the gate has rules for, so it is the program and
// not an option's value.
func looks_like_program(w string) bool {
	name, ok := program_name(w)
	if !ok {
		return false
	}
	_, known := risk_rules[name]
	return known || read_only_cmds[name]
}

// has_bare_password_flag: mysql's "-p" / "--password" with no value attached
// makes it prompt. "-psecret" does not.
func has_bare_password_flag(args []sh_word) bool {
	for _, a := range args {
		if a.text == "-p" || a.text == "--password" {
			return true
		}
	}
	return false
}
