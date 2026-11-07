package service

import (
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/media"
)

// SelfCheck is what is wrong with connection c as it is saved, each in words
// for its row, or nothing. It reads what is kept and changes nothing.
//
// Whatever c's use, a name gohort keeps for something built in, a spec that
// no longer reads, a saved value its setting no longer takes, and a secret
// kept but unreadable are wrong. While a page references it, so is what
// would refuse its next call: a required setting or secret missing, a spec
// still naming a vault credential, and a secret given for an address its
// calls no longer go to. One nothing references yet may have no key, and
// that is not wrong.
//
// A connection of another kind is held to its own type's check (otherCheck).
//
// Without it, a bad value or a lost key surfaced only as some tool's call
// failing, on whichever turn reached it first.
func SelfCheck(db Database, c Connector) []string {
	if c.Kind != Kind {
		return otherCheck(c)
	}
	var out []string
	if media.Reserved(c.Name) {
		out = append(out, "its name is one gohort keeps for something built in, so no page can choose it: add it again under another name")
	}
	s, err := specOf(c)
	if err != nil {
		return append(out, "its spec no longer reads: "+err.Error()+". Fix it")
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
	if !Referenced(db, c.Name) {
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
	if has && kept.Keystore != "" {
		if _, _, err := keystoreSecret(kept.Keystore, f, settled.secretAddresses(f)); err != nil {
			out = append(out, "it is in use, and "+err.Error())
		}
		return out
	}
	switch {
	case !has && f.Required && !strings.Contains(strings.Join(out, "\n"), "is kept but cannot be read"):
		out = append(out, "it is in use with no "+f.Title()+": give it under Settings or on the page that uses it")
	case has && kept.Value != "" && !sameAddresses(kept.Addresses, settled.secretAddresses(f)):
		out = append(out, fmt.Sprintf("its %s was given for %s, and its calls now go to %s, so they are refused: give it again under Settings",
			f.Title(), strings.Join(kept.Addresses, ", "), strings.Join(settled.secretAddresses(f), ", ")))
	}
	return out
}

// otherCheck is SelfCheck for a connection of another kind (an MCP server,
// a poll, a messaging bridge, an adapted flow): its type's own check of its
// spec, the one a save runs, which looks up what it names (a credential, a
// peer) as it stands now; and, while it is in use, a credential it names
// switched off. Each was checked when it was saved, so what fails now is
// what changed since.
func otherCheck(c Connector) []string {
	if _, ok := ConnectorHandlerFor(c.Kind); !ok {
		return []string{fmt.Sprintf("its type %q is not one this build knows, so it is not brought up", c.Kind)}
	}
	var out []string
	if err := ValidateConnector(c); err != nil {
		out = append(out, "it no longer passes its own check: "+err.Error())
	}
	if !c.Approved {
		return out
	}
	for _, name := range ConnectorCredentialRefs(c.Spec) {
		cred, ok := Secure().Resolve(name, c.Owner)
		switch {
		case !ok:
			// One it names and no longer finds is its check's to say.
		case cred.AwaitingApproval:
			out = append(out, "it is in use and its credential "+name+" waits for an administrator's approval, so its calls are refused")
		case cred.Disabled || cred.AdminDisabled:
			out = append(out, "it is in use and its credential "+name+" is switched off, so its calls are refused: enable it under Admin > APIs")
		}
	}
	return out
}

// LogSelfCheck warns of what SelfCheck finds in every connection, and of
// every setting that needs setting again (StaleSettings), once at start, so
// a bad value or a lost key is in the log before a call meets it.
func LogSelfCheck(db Database) {
	for _, c := range ListConnectors(db) {
		for _, p := range SelfCheck(db, c) {
			Warn("[connections] %s: %s", c.Title(), p)
		}
	}
	for _, st := range StaleSettings(db) {
		Warn("[settings] %s needs setting again: %s", st.Page, st.Why)
	}
}
