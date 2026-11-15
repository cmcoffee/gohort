// Package displayname is the one rule for the name an account goes by.
//
// An account's username is its key (core/accountname) and is often an email
// address, which is not what anyone calls a person. The display name is what
// they are called: chosen by them, asked for once, and shown beside the
// username, so two people who pick the same name, or someone who names
// themselves after somebody else, can still be told apart. It is a label and
// nothing reads it as a key.
//
// A leaf package rather than a file in core for the reason core/accountname
// is: core sits at its export ceiling.
package displayname

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxLen is the longest name, in characters.
const MaxLen = 64

// Clean is name as stored: trimmed, and every run of space (line breaks
// included, so a name cannot pass for two lines of whatever shows it) made
// one space. Refused when empty, too long, or holding a control character.
func Clean(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return "", errors.New("a name is required")
	}
	if utf8.RuneCountInString(name) > MaxLen {
		return "", errors.New("a name is at most 64 characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", errors.New("a name is plain text, on one line")
		}
	}
	return name, nil
}

// Shown is how an account reads to someone else: its name with the username
// beside it, or the username alone when it has no name or the name is the
// username.
func Shown(name, username string) string {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, username) {
		return username
	}
	return name + " (" + username + ")"
}
