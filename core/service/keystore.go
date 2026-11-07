package service

// A connection's key from the Keystore (core/keystore): the connection keeps
// the entry's ID in place of the key, and every call reads the key from the
// entry, checked against the entry's addresses, so a key rotated in the
// Keystore is the one the next call sends.

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/keystore"
)

// keystoreRef is how a page's save asks for a secret to come from the
// Keystore: "keystore:<entry id>". "keystore:" alone drops a reference.
const keystoreRef = "keystore:"

// KeystoreValue is the value a save takes to point a secret at entry id.
func KeystoreValue(id string) string { return keystoreRef + strings.TrimSpace(id) }

// isKeystoreValue splits a saved value into the entry it names.
func isKeystoreValue(v string) (string, bool) {
	id, ok := strings.CutPrefix(strings.TrimSpace(v), keystoreRef)
	return strings.TrimSpace(id), ok
}

// keystoreFits refuses entry e as the key for field f going to addrs: the
// wrong kind (a signing set for a key, or the other way), or an address it
// was not given for.
func keystoreFits(e keystore.Entry, f ConfigField, addrs []string) error {
	signs := strings.HasPrefix(f.Send, "aws_sigv4")
	if signs && e.Kind != keystore.KindAWS {
		return fmt.Errorf("%s is a key, and this connection signs with AWS: pick an AWS signing entry", e.Name)
	}
	if !signs && e.Kind != keystore.KindKey {
		return fmt.Errorf("%s is an AWS signing entry, and this connection takes a key", e.Name)
	}
	for _, a := range addrs {
		if signs && strings.Contains(a, ".amazonaws.com") && e.Covers(keystore.BedrockAddress) {
			continue
		}
		if !e.Covers(a) {
			return fmt.Errorf("the Keystore key %s is not given for %s: add that address to it under Services > Keystore, or pick another", e.Name, a)
		}
	}
	return nil
}

// keystoreSecret is the key entry id gives field f for addrs: its value, and
// for an AWS entry signing with a profile, that profile.
func keystoreSecret(id string, f ConfigField, addrs []string) (value, profile string, err error) {
	e, ok := keystore.Get(RootDB, id)
	if !ok {
		return "", "", fmt.Errorf("its %s names a Keystore key that is gone: pick another under Settings", f.Title())
	}
	if err := keystoreFits(e, f, addrs); err != nil {
		return "", "", err
	}
	value, _ = keystore.Secret(RootDB, id)
	if e.Kind == keystore.KindAWS && value == "" {
		profile = e.Profile
	}
	return value, profile, nil
}

// KeystoreChoices are the Keystore entries that may stand in for connection
// c's secret name: the right kind, given for every address the key goes to.
// None for a connection a peer manages, whose key the peer keeps.
func KeystoreChoices(c Connector, name string) []keystore.Entry {
	if c.Kind != Kind || strings.HasPrefix(c.Name, "peer-") {
		return nil
	}
	s, err := specOf(c)
	if err != nil {
		return nil
	}
	settled, err := s.settle(ConnectionConfig(RootDB, c))
	if err != nil {
		return nil
	}
	f, ok := settled.secretNamed(name)
	if !ok {
		return nil
	}
	addrs := settled.secretAddresses(f)
	var out []keystore.Entry
	for _, e := range keystore.List(RootDB) {
		if keystoreFits(e, f, addrs) == nil {
			out = append(out, e)
		}
	}
	return out
}

// SecretOptional reports whether connection c may go without its secret
// name: a local server started with no key, say. Its "Key from" choice then
// offers No key.
func SecretOptional(c Connector, name string) bool {
	s, err := specOf(c)
	if err != nil {
		return false
	}
	f, ok := s.secretNamed(name)
	return ok && !f.Required
}

// KeystoreOf is the Keystore entry connection c's secret name comes from, or
// "" when it keeps its own key or none.
func KeystoreOf(c Connector, name string) string {
	kept, ok := GetConnectionSecret(RootDB, c, name)
	if !ok {
		return ""
	}
	return kept.Keystore
}

// connectionsUsing is what of the connections names Keystore entry id, as
// its row says: "connector <title>".
func connectionsUsing(id string) []string {
	if RootDB == nil {
		return nil
	}
	var out []string
	for _, c := range ListConnectors(RootDB) {
		if c.Kind != Kind {
			continue
		}
		s, err := specOf(c)
		if err != nil {
			continue
		}
		for _, f := range s.Config {
			if f.IsSecret() && KeystoreOf(c, f.Name) == id {
				out = append(out, "connector "+c.Title())
				break
			}
		}
	}
	return out
}

func init() { keystore.RegisterUse(connectionsUsing) }

// secretKind is the kind of Keystore entry field f takes.
func secretKind(f ConfigField) string {
	if strings.HasPrefix(f.Send, "aws_sigv4") {
		return keystore.KindAWS
	}
	return keystore.KindKey
}

// keepsInKeystore reports whether a key typed for connection c's field f,
// going to addrs, goes into the Keystore rather than staying the
// connection's own: when nothing there fits yet, so the first key for a
// provider is kept once and the next connection finds it. A connection a
// peer manages keeps the peer's key; an address the Keystore cannot hold
// keeps the key where it is.
func keepsInKeystore(c Connector, f ConfigField, addrs []string) bool {
	if strings.HasPrefix(c.Name, "peer-") || len(addrs) == 0 {
		return false
	}
	for _, a := range addrs {
		if _, ok := keystore.NormalizeAddress(a); !ok {
			return false
		}
	}
	for _, e := range keystore.List(RootDB) {
		if keystoreFits(e, f, addrs) == nil {
			return false
		}
	}
	return true
}

// DefaultKeystoreFor is the Keystore entry connection c's secret name is
// offered by default when it keeps no key of its own yet: the first that
// fits, so a second connection to a provider needs no key typed.
func DefaultKeystoreFor(c Connector, name string) string {
	// Any record, an empty one included, is a choice made: a key of its own,
	// a Keystore key, or None.
	if _, chosen := GetConnectionSecret(RootDB, c, name); chosen {
		return ""
	}
	if choices := KeystoreChoices(c, name); len(choices) > 0 {
		return choices[0].ID
	}
	return ""
}

// MoveToKeystore moves each key connection c keeps of its own into the
// Keystore and points the connection at it: an entry already holding that
// key for those addresses is reused, else one is made, named for the
// provider. It answers the entries' names.
func MoveToKeystore(c Connector) ([]string, error) {
	if c.Kind != Kind || strings.HasPrefix(c.Name, "peer-") {
		return nil, fmt.Errorf("%s keeps no key the Keystore can hold", c.Name)
	}
	s, err := specOf(c)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, f := range s.Config {
		if !f.IsSecret() {
			continue
		}
		kept, ok := GetConnectionSecret(RootDB, c, f.Name)
		if !ok || kept.Keystore != "" || kept.Value == "" {
			continue
		}
		e, _, err := keystore.Keep(RootDB, secretKind(f), c.Title(), kept.Addresses, kept.Value)
		if err != nil {
			return names, fmt.Errorf("%s's %s: %w", c.Name, f.Title(), err)
		}
		if err := SetConnectionSecretRef(RootDB, c, f.Name, e.ID); err != nil {
			return names, err
		}
		names = append(names, e.Name)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("%s keeps no key of its own", c.Name)
	}
	return names, nil
}

// HeldKey is a key a connection keeps of its own, as the Keystore page lists
// it beside its entries, to be moved in.
type HeldKey struct {
	Connector string
	Title     string
	Addresses []string
	Hint      string
}

// KeysHeldByConnections are the keys connections keep of their own that the
// Keystore could hold.
func KeysHeldByConnections() []HeldKey {
	var out []HeldKey
	for _, c := range ListConnectors(RootDB) {
		if c.Kind != Kind || strings.HasPrefix(c.Name, "peer-") {
			continue
		}
		s, err := specOf(c)
		if err != nil {
			continue
		}
		hints := KeptSecrets(RootDB, c)
		for _, f := range s.Config {
			if !f.IsSecret() {
				continue
			}
			if kept, ok := GetConnectionSecret(RootDB, c, f.Name); ok && kept.Keystore == "" && kept.Value != "" {
				out = append(out, HeldKey{Connector: c.Name, Title: c.Title(), Addresses: kept.Addresses, Hint: hints[f.Name]})
				break
			}
		}
	}
	return out
}
