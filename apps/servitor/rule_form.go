package servitor

// The rule form: an Always rule set out as choices rather than typed in the
// rule's own syntax. A program, the subcommand that may follow it (one word,
// or one of a few), whether it may run with sudo, and what may follow: nothing,
// anything (flags and arguments), or only the flags listed, each taking a
// value or not. One rule then covers
// every combination of the flags it allows, where the syntax alone needed a
// rule per combination.
//
// The form is stored as an ordinary rule string (composeRule), so nothing
// stored changes and the gate reads it as it reads every rule; a rule that
// fits the form reads back into it (parseRuleForm), and one that does not
// (a pipeline saved from an Always card) is edited as text.

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ruleForm is one rule as the form sets it out.
type ruleForm struct {
	Program string `json:"program"`
	// Subcommands is the word that may follow the program: one of these.
	// Empty for none.
	Subcommands []string `json:"subcommands,omitempty"`
	// Sudo lets the command run under sudo (and sudo -n), and without it.
	Sudo bool `json:"sudo,omitempty"`
	// Flags is "none", "any" (anything after the program: flags and
	// arguments), or "only" (just the flags in Allowed).
	Flags   string     `json:"flags"`
	Allowed []ruleFlag `json:"allowed,omitempty"`
}

// ruleFlag is one flag a rule allows: its name, and whether it takes a value
// ("" none, "any", "number"), after = or as the next word.
type ruleFlag struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

// programWord is a program as the form takes it: a name or a path, written
// out, with nothing in it a shell would read as more than one word or as a
// pattern.
var programWord = regexp.MustCompile(`^[A-Za-z0-9_./+@-]+$`)

// subcommandWord is one word that may follow the program.
var subcommandWord = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:+@-]*$`)

// composeRule writes the form as a rule string, checked as a rule added by
// hand is (validNewRule).
func composeRule(f ruleForm) (string, error) {
	prog := strings.TrimSpace(f.Program)
	if prog == "" {
		return "", errors.New("name the program the rule allows")
	}
	if !programWord.MatchString(prog) || hasDotDot(prog) || isAllowWrapper(prog) {
		return "", fmt.Errorf("%q is not a program the form takes: write its name or path, with no spaces, patterns or sudo (tick Run with sudo for that)", prog)
	}
	var words []string
	if f.Sudo {
		words = append(words, "sudo")
	}
	words = append(words, prog)
	var subs []string
	for _, w := range f.Subcommands {
		if w = strings.TrimSpace(w); w == "" {
			continue
		}
		if !subcommandWord.MatchString(w) {
			return "", fmt.Errorf("%q is not a subcommand the form takes: one word, letters, digits and . _ : + @ -", w)
		}
		subs = append(subs, w)
	}
	switch len(subs) {
	case 0:
	case 1:
		words = append(words, subs[0])
	default:
		words = append(words, "<word:"+strings.Join(subs, ",")+">")
	}
	switch f.Flags {
	case "", "none":
	case "any":
		words = append(words, argsPlaceholder)
	case "only":
		if len(f.Allowed) == 0 {
			return "", errors.New("list the flags the rule allows, or choose any flags or none")
		}
		seen := map[string]bool{}
		var spec []string
		for _, fl := range f.Allowed {
			name := strings.TrimSpace(fl.Name)
			if !flagName.MatchString(name) {
				return "", fmt.Errorf("%q is not a flag: one or two dashes and a name, with no value (choose whether it takes one)", name)
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			switch fl.Value {
			case "":
				spec = append(spec, name)
			case "any":
				spec = append(spec, name+"=*")
			case "number":
				spec = append(spec, name+"=#")
			default:
				return "", fmt.Errorf("a flag's value is none, any or number, not %q", fl.Value)
			}
		}
		words = append(words, "<flags:"+strings.Join(spec, ",")+">")
	default:
		return "", fmt.Errorf("flags are none, any or only, not %q", f.Flags)
	}
	rule := strings.Join(words, " ")
	if err := validNewRule(rule); err != nil {
		return "", err
	}
	return rule, nil
}

// parseRuleForm reads a rule back into the form, when it is one the form
// could have written: [sudo] program [subcommand] [flags]. ok false for any
// other rule, which is then edited as text.
func parseRuleForm(rule string) (ruleForm, bool) {
	ws, ok := splitAllowWords(strings.TrimSpace(rule))
	if !ok || len(ws) == 0 {
		return ruleForm{}, false
	}
	for _, w := range ws {
		if w.op || w.quoted || w.subst {
			return ruleForm{}, false
		}
	}
	f := ruleForm{Flags: "none"}
	i := 0
	if ws[0].text == "sudo" {
		f.Sudo = true
		i++
	}
	if i >= len(ws) || !programWord.MatchString(ws[i].text) || isAllowWrapper(ws[i].text) {
		return ruleForm{}, false
	}
	f.Program = ws[i].text
	i++
	if i < len(ws) && !isFlagsPlaceholder(ws[i]) {
		switch {
		case isWordChoice(ws[i]):
			f.Subcommands = wordChoices(ws[i])
		case subcommandWord.MatchString(ws[i].text):
			f.Subcommands = []string{ws[i].text}
		default:
			return ruleForm{}, false
		}
		i++
	}
	if i < len(ws) {
		spec, ok := flagsSpec(ws[i])
		if !ok || i != len(ws)-1 {
			return ruleForm{}, false
		}
		if spec == nil {
			f.Flags = "any"
		} else {
			f.Flags = "only"
			for _, e := range strings.Split(strings.TrimSuffix(strings.TrimPrefix(ws[i].text, "<flags:"), ">"), ",") {
				name, value, _ := strings.Cut(strings.TrimSpace(e), "=")
				fl := ruleFlag{Name: name}
				switch value {
				case "*":
					fl.Value = "any"
				case "#":
					fl.Value = "number"
				}
				f.Allowed = append(f.Allowed, fl)
			}
		}
	}
	return f, true
}

// describeRule says what a rule allows in words, for Permissions and for the
// investigator: the form's choices when it fits the form, the rule as written
// otherwise.
func describeRule(rule string) string {
	f, ok := parseRuleForm(rule)
	if !ok {
		return "`" + rule + "`"
	}
	var b strings.Builder
	b.WriteString("`" + f.Program)
	if len(f.Subcommands) == 1 {
		b.WriteString(" " + f.Subcommands[0])
	}
	b.WriteString("`")
	if len(f.Subcommands) > 1 {
		b.WriteString(" followed by " + strings.Join(f.Subcommands, " or "))
	}
	switch f.Flags {
	case "any":
		b.WriteString(", with anything after it")
	case "only":
		var names []string
		for _, fl := range f.Allowed {
			n := fl.Name
			switch fl.Value {
			case "any":
				n += " <value>"
			case "number":
				n += " <number>"
			}
			names = append(names, n)
		}
		b.WriteString(", with only these flags, in any order: " + strings.Join(names, ", "))
	default:
		b.WriteString(", with no flags")
	}
	if f.Sudo {
		b.WriteString(" (with or without sudo)")
	}
	return b.String()
}

// describeNear is describeRule for a near-miss rule, "" for none.
func describeNear(rule string) string {
	if strings.TrimSpace(rule) == "" {
		return ""
	}
	return describeRule(rule)
}
