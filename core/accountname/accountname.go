// Package accountname is the one rule for an account's name.
//
// A name is a key nearly everywhere: the account record, every per-user store,
// ownership on shared things, the folders on disk. Two rules follow, and every
// way an account is made applies both:
//
//   - one person, one name: a name is stored lowercased, and a new name that
//     matches an existing one when lowercased is refused, so "Alice@example.test"
//     cannot sit beside "alice@example.test" and pass for her;
//   - nothing that reads as structure: only letters, digits and @ . _ + -,
//     since stores key records as name + ":" + something, and a name holding
//     ":" listed as the account whose name came before it.
//
// Existing names are not rewritten: they key too much to move. Audit says
// which of them the rule would refuse, for an administrator to look at, and
// login matches one typed in another case when only one account answers.
//
// A leaf package rather than a file in core for the reason core/notices is:
// core sits at its export ceiling.
package accountname

import (
	"fmt"
	"sort"
	"strings"
)

// MaxLen is the longest name: an email address's limit.
const MaxLen = 254

// Canonical is raw as an account name: trimmed and lowercased. It is refused
// when empty, longer than MaxLen, or holding anything but ASCII letters,
// digits and @ . _ + -.
func Canonical(raw string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	if name == "" {
		return "", fmt.Errorf("a name is required")
	}
	if len(name) > MaxLen {
		return "", fmt.Errorf("a name is at most %d characters", MaxLen)
	}
	for _, r := range name {
		if !allowed(r) {
			return "", fmt.Errorf("a name may hold only letters, digits and @ . _ + - (not %q)", r)
		}
	}
	return name, nil
}

func allowed(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		return true
	}
	return strings.ContainsRune("@._+-", r)
}

// Clash is the existing name that name matches when both are lowercased, if
// there is one.
func Clash(existing []string, name string) (string, bool) {
	for _, e := range existing {
		if strings.EqualFold(e, name) {
			return e, true
		}
	}
	return "", false
}

// Matches are the existing names typed matches when both are lowercased.
func Matches(existing []string, typed string) []string {
	typed = strings.TrimSpace(typed)
	var out []string
	for _, e := range existing {
		if strings.EqualFold(e, typed) {
			out = append(out, e)
		}
	}
	sort.Strings(out)
	return out
}

// Audit is one line per existing name the rule would refuse today, or that
// matches another when both are lowercased. Empty when every name is fine.
func Audit(names []string) []string {
	var out []string
	folded := map[string][]string{}
	for _, n := range names {
		folded[strings.ToLower(n)] = append(folded[strings.ToLower(n)], n)
		if _, err := Canonical(n); err != nil {
			out = append(out, fmt.Sprintf("%q: %v", n, err))
		}
	}
	for _, group := range folded {
		if len(group) > 1 {
			sort.Strings(group)
			out = append(out, fmt.Sprintf("%s are one name in different case: each still signs in only as typed", strings.Join(quoted(group), " and ")))
		}
	}
	sort.Strings(out)
	return out
}

func quoted(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = fmt.Sprintf("%q", n)
	}
	return out
}
