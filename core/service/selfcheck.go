package service

import (
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// SelfCheck is what is wrong with connection c as it is saved, each in words
// for its row, or nothing. It reads what is kept and changes nothing.
//
// Whatever c's use, a spec that no longer reads, a saved value its setting
// no longer takes, and a secret kept but unreadable are wrong. While a page
// has it in use, so is what would refuse its next call: a required setting
// or secret missing, a spec still naming a vault credential, and a secret
// given for an address its calls no longer go to. A connection gohort
// provides and nothing uses yet has no key, and that is not wrong.
//
// Without it, a bad value or a lost key surfaced only as some tool's call
// failing, on whichever turn reached it first.
func SelfCheck(db Database, c Connector) []string {
	if c.Kind != Kind {
		return nil
	}
	s, err := specOf(c)
	if err != nil {
		return []string{"its spec no longer reads: " + err.Error() + ". Fix or Revert it"}
	}
	declared := map[string]Setting{}
	for _, st := range s.settings() {
		declared[st.Key] = st
	}
	values := ConnectionConfig(db, c)
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		st, ok := declared[k]
		if !ok || st.Field.IsSecret() {
			continue // a value its spec no longer declares is never sent
		}
		if err := checkValue(st.Field, values[k]); err != nil {
			out = append(out, fmt.Sprintf("its %s setting is wrong: %v. Set it again under Settings", st.Field.Title(), err))
		}
	}
	for _, name := range c.UnreadableSecrets(db) {
		title := name
		if st, ok := declared[name]; ok {
			title = st.Field.Title()
		}
		out = append(out, fmt.Sprintf("its %s is kept but cannot be read: give it again under Settings", title))
	}
	if !c.Approved {
		return out
	}
	if cred := strings.TrimSpace(s.Provider.Auth.Credential); cred != "" {
		return append(out, "it is in use and sends its key with the vault credential "+cred+", which connections no longer do: add it again from its template")
	}
	settled, err := s.settle(values)
	if err != nil {
		return append(out, "it is in use, and "+err.Error())
	}
	f, ok := settled.secretField()
	if !ok {
		return out
	}
	kept, has := GetConnectionSecret(db, c, f.Name)
	switch {
	case !has && f.Required && !strings.Contains(strings.Join(out, "\n"), "is kept but cannot be read"):
		out = append(out, "it is in use with no "+f.Title()+": give it under Settings or on the page that uses it")
	case has && !sameAddresses(kept.Addresses, settled.secretAddresses(f)):
		out = append(out, fmt.Sprintf("its %s was given for %s, and its calls now go to %s, so they are sent without it: give it again under Settings",
			f.Title(), strings.Join(kept.Addresses, ", "), strings.Join(settled.secretAddresses(f), ", ")))
	}
	return out
}

// LogSelfCheck warns of what SelfCheck finds in every connection, once at
// start, so a bad value or a lost key is in the log before a call meets it.
func LogSelfCheck(db Database) {
	for _, c := range ListConnectors(db) {
		for _, p := range SelfCheck(db, c) {
			Warn("[connections] %s: %s", c.Title(), p)
		}
	}
}
