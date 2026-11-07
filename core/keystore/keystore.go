// Package keystore is the one home for the keys gohort's own services use:
// a provider account's key (Google Gemini, OpenAI, Anthropic), or an AWS
// signing set for Bedrock, kept once and named by every model tier and
// connector that talks to that provider. Rotating an entry rotates it for all
// of them. See docs/design/integration-model.md, "Keystore".
//
// The vault (API Credentials) is a different store with a different job: the
// keys agents' API tools call. Both keep the same rules: a key is sent only to
// the addresses it was given for, moving those addresses means giving the key
// again, and the key is never shown, only a line saying which one is kept.
//
// The package is a leaf: it reads and writes through the slice of a database
// it needs, which core's Database satisfies.
package keystore

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Store is the slice of a key/value database the keystore uses.
type Store interface {
	Get(table, key string, output interface{}) bool
	Set(table, key string, value interface{})
	CryptSet(table, key string, value interface{})
	Unset(table, key string)
	Keys(table string) []string
}

// Table holds the entries, keyed by ID, and beside each its secret under
// ID + secretSuffix, encrypted.
const (
	Table        = "keystore"
	secretSuffix = "__secret"
)

// Kinds of entry.
const (
	// KindKey is one key, sent as the model tier or connector using it says
	// (a bearer token, a header, a query parameter).
	KindKey = "key"
	// KindAWS is an AWS signing set for Bedrock: an access key pair
	// ("ACCESS_KEY_ID:SECRET") or a named profile resolved on this machine.
	KindAWS = "aws"
)

// BedrockAddress is the address an AWS entry is given for when it signs for
// Bedrock in any region: the pseudo-address Bedrock model tiers bind a key
// to (core's tierKeyAddress).
const BedrockAddress = "bedrock"

// Entry is one key's record. The secret is kept apart, encrypted.
type Entry struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Addresses []string  `json:"addresses"`
	Profile   string    `json:"profile,omitempty"` // KindAWS: an AWS profile in place of a key pair
	Set       time.Time `json:"set"`               // when the secret was last given
}

type secret struct {
	Value string
}

// Error is a refusal the caller can show as it is.
type Error string

func (e Error) Error() string { return string(e) }

// NormalizeAddress is an address as entries compare it: a URL's scheme and
// host in lower case with no path or trailing slash, or BedrockAddress.
// It reports false for anything else.
func NormalizeAddress(a string) (string, bool) {
	a = strings.TrimSpace(a)
	if strings.EqualFold(a, BedrockAddress) {
		return BedrockAddress, true
	}
	u, err := url.Parse(a)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", false
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host), true
}

// Covers reports whether e may be sent to address addr: addr is one of the
// addresses it was given for.
func (e Entry) Covers(addr string) bool {
	n, ok := NormalizeAddress(addr)
	if !ok {
		return false
	}
	for _, a := range e.Addresses {
		if a == n {
			return true
		}
	}
	return false
}

// List is every entry, by name.
func List(db Store) []Entry {
	var out []Entry
	for _, k := range db.Keys(Table) {
		if strings.HasSuffix(k, secretSuffix) {
			continue
		}
		var e Entry
		if db.Get(Table, k, &e) && e.ID != "" {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// Get is the entry with id.
func Get(db Store, id string) (Entry, bool) {
	var e Entry
	id = strings.TrimSpace(id)
	if id == "" || strings.HasSuffix(id, secretSuffix) {
		return e, false
	}
	return e, db.Get(Table, id, &e) && e.ID == id
}

// Secret is the entry's kept secret: the key, or an AWS key pair. Empty for
// an AWS entry that signs with a profile.
func Secret(db Store, id string) (string, bool) {
	var s secret
	if !db.Get(Table, strings.TrimSpace(id)+secretSuffix, &s) {
		return "", false
	}
	return s.Value, s.Value != ""
}

// Hint says which secret is kept without showing it: "ends in …WXYZ, given
// 2026-11-07" for a key long enough that four characters give nothing away,
// "set, given ..." otherwise, or the profile an AWS entry signs with.
func Hint(db Store, e Entry) string {
	given := ""
	if !e.Set.IsZero() {
		given = ", given " + e.Set.Format("2006-01-02")
	}
	v, ok := Secret(db, e.ID)
	switch {
	case e.Kind == KindAWS && !ok && e.Profile != "":
		return "signs with AWS profile " + e.Profile + given
	case !ok:
		return "no key kept"
	case e.Kind == KindAWS:
		id, _, _ := strings.Cut(v, ":")
		if len(id) >= 16 {
			return "key pair " + id[:4] + "…" + id[len(id)-4:] + given
		}
		return "key pair set" + given
	case len(v) >= 16:
		return "ends in …" + v[len(v)-4:] + given
	}
	return "set" + given
}

// Save adds or changes an entry. A new entry needs its secret (a key, an AWS
// key pair or profile); on a change a blank secret keeps the one kept, and
// changing the addresses refuses unless the secret is given again, since a
// key goes only where it was given for. It answers the entry as saved.
func Save(db Store, e Entry, newSecret string) (Entry, error) {
	e.Name = strings.TrimSpace(e.Name)
	e.Kind = strings.TrimSpace(e.Kind)
	e.Profile = strings.TrimSpace(e.Profile)
	newSecret = strings.TrimSpace(newSecret)
	if e.Name == "" {
		return e, Error("give the key a name, such as the provider's: Google Gemini, OpenAI")
	}
	if e.Kind == "" {
		e.Kind = KindKey
	}
	if e.Kind != KindKey && e.Kind != KindAWS {
		return e, Error(fmt.Sprintf("kind must be %s or %s", KindKey, KindAWS))
	}
	var addrs []string
	seen := map[string]bool{}
	for _, a := range e.Addresses {
		if strings.TrimSpace(a) == "" {
			continue
		}
		n, ok := NormalizeAddress(a)
		if !ok {
			return e, Error(fmt.Sprintf("%q is not an address: give an https address such as https://api.openai.com, or %q for Bedrock", a, BedrockAddress))
		}
		if !seen[n] {
			seen[n] = true
			addrs = append(addrs, n)
		}
	}
	if len(addrs) == 0 {
		return e, Error("say where this key may be sent: the provider's address, such as https://generativelanguage.googleapis.com")
	}
	sort.Strings(addrs)
	e.Addresses = addrs
	if e.Kind == KindAWS && newSecret != "" {
		id, sec, ok := strings.Cut(newSecret, ":")
		if !ok || strings.TrimSpace(id) == "" || strings.TrimSpace(sec) == "" {
			return e, Error("an AWS key pair is ACCESS_KEY_ID:SECRET_ACCESS_KEY")
		}
	}
	if e.Kind == KindKey {
		e.Profile = ""
	}
	for _, other := range List(db) {
		if other.ID != e.ID && strings.EqualFold(other.Name, e.Name) {
			return e, Error(fmt.Sprintf("a key named %q is already kept: give this one another name", e.Name))
		}
	}
	prev, exists := Get(db, e.ID)
	if !exists {
		e.ID = newID()
		if newSecret == "" && !(e.Kind == KindAWS && e.Profile != "") {
			if e.Kind == KindAWS {
				return e, Error("give the AWS key pair, or the profile to sign with")
			}
			return e, Error("give the key")
		}
	} else if newSecret == "" && !sameAddresses(prev.Addresses, e.Addresses) {
		if _, kept := Secret(db, e.ID); kept {
			return e, Error("the addresses changed: give the key again, since a key is sent only to the addresses it was given for")
		}
	}
	if newSecret != "" {
		e.Set = time.Now()
		db.CryptSet(Table, e.ID+secretSuffix, secret{Value: newSecret})
	} else if exists {
		e.Set = prev.Set
		if e.Kind == KindAWS && e.Profile != "" && prev.Profile != e.Profile {
			e.Set = time.Now()
		}
	} else {
		e.Set = time.Now()
	}
	db.Set(Table, e.ID, e)
	changed(e.ID)
	return e, nil
}

// Delete removes an entry and its secret. It refuses while anything names it,
// saying what does, so a tier or connector is never left naming nothing.
func Delete(db Store, id string) error {
	if _, ok := Get(db, id); !ok {
		return Error("no such key")
	}
	if uses := UsedBy(id); len(uses) > 0 {
		return Error("still used by " + strings.Join(uses, ", ") + ": point those at another key first")
	}
	db.Unset(Table, id+secretSuffix)
	db.Unset(Table, id)
	changed(id)
	return nil
}

// Use is a user of entries, registered by the packages whose settings name
// them (model tiers, connections): what names entry id, as people read it
// ("Lead model", "connector gemini-images").
type Use func(id string) []string

var (
	hookMu    sync.RWMutex
	uses      []Use
	listeners []func(id string)
)

// RegisterUse adds a user of entries.
func RegisterUse(u Use) {
	hookMu.Lock()
	uses = append(uses, u)
	hookMu.Unlock()
}

// UsedBy is everything that names entry id.
func UsedBy(id string) []string {
	hookMu.RLock()
	defer hookMu.RUnlock()
	var out []string
	for _, u := range uses {
		out = append(out, u(id)...)
	}
	sort.Strings(out)
	return out
}

// OnChange subscribes to entry changes: a model client copies its key when it
// is built, so rotating an entry has to rebuild every tier that names it.
func OnChange(fn func(id string)) {
	hookMu.Lock()
	listeners = append(listeners, fn)
	hookMu.Unlock()
}

func changed(id string) {
	hookMu.RLock()
	fns := append([]func(string){}, listeners...)
	hookMu.RUnlock()
	for _, fn := range fns {
		fn(id)
	}
}

func sameAddresses(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "k_" + hex.EncodeToString(b[:])
}
