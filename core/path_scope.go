// Path scopes: turning a caller-supplied name into a path that is proved
// to be somewhere it is allowed to be.
//
// This exists because shell quoting is not containment, and the two get
// confused. A tool template renders "{dir}" by single-quoting whatever
// arrived, which stops the value contributing SYNTAX — it can never
// close a quote and start a new command. It does nothing about the value
// being "../../var/lib/something", which is a perfectly well-formed
// single argument that happens to point somewhere else. servitor's own
// note on the enum check says it plainly: the enum is the only thing
// that can stop a value dangerous in the APP's terms rather than the
// shell's.
//
// An enum is frozen when the tool is minted, which is fine for "--env
// production|staging" and useless for a set that changes — the folders
// under a drop directory, where new ones appearing without ceremony is
// the entire point. So a path scope is the enum's late-binding sibling:
// the tool declares WHICH registered root a parameter must land in, and
// the check runs when the tool runs.
//
// The resolver returns the ABSOLUTE path rather than approving the name,
// so a template gets something unambiguous regardless of what working
// directory the target happens to have.

package core

import (
	"sort"
	"strconv"
	"strings"
	"sync"
)

// PathScopeResolver resolves value within one named root belonging to
// user, returning an absolute path, or an error explaining the refusal in
// terms the model that supplied the value can act on.
//
// name identifies which root of this kind (a store slug, a workspace id).
// A resolver is expected to refuse traversal AND to resolve symlinks: a
// link inside the root pointing out of it is the same hole with better
// manners.
type PathScopeResolver func(user, name, value string) (string, error)

// PathScopeLister returns the values currently valid for one root, for a
// tool description. Best-effort: a scope with no lister still works, the
// model just has to discover the names some other way.
type PathScopeLister func(user, name string) []string

type pathScope struct {
	resolve PathScopeResolver
	list    PathScopeLister
}

var (
	pathScopeMu sync.RWMutex
	pathScopes  = map[string]pathScope{}
)

// RegisterPathScope registers a resolver for one kind ("files"). Called
// at startup by the app that owns those roots, the same way a reference
// source or a tool provider registers.
func RegisterPathScope(kind string, resolve PathScopeResolver, list PathScopeLister) {
	if strings.TrimSpace(kind) == "" || resolve == nil {
		return
	}
	pathScopeMu.Lock()
	defer pathScopeMu.Unlock()
	pathScopes[kind] = pathScope{resolve: resolve, list: list}
}

// SplitPathScopeRef splits a "kind:name" reference.
func SplitPathScopeRef(ref string) (kind, name string) {
	ref = strings.TrimSpace(ref)
	if i := strings.Index(ref, ":"); i >= 0 {
		return strings.TrimSpace(ref[:i]), strings.TrimSpace(ref[i+1:])
	}
	return ref, ""
}

// ResolvePathScope checks value against the root named by ref
// ("kind:name") and returns the absolute path it resolves to.
//
// An UNKNOWN kind is an error rather than a pass. A parameter declaring
// a constraint nobody implements has to fail closed: the alternative is
// a tool that silently stops being constrained the day its app is
// compiled out, which is exactly when nobody is looking.
func ResolvePathScope(user, ref, value string) (string, error) {
	kind, name := SplitPathScopeRef(ref)
	pathScopeMu.RLock()
	sc, ok := pathScopes[kind]
	pathScopeMu.RUnlock()
	if !ok {
		return "", Error("this parameter is limited to " + strconv.Quote(kind) +
			", and nothing here provides that. Nothing ran. Tell the person the constraint names a source their deployment does not have.")
	}
	return sc.resolve(user, name, value)
}

// PathScopeChoices lists the currently valid values for a scope, for a
// tool description. Empty when the scope has no lister or nothing is
// there.
func PathScopeChoices(user, ref string) []string {
	kind, name := SplitPathScopeRef(ref)
	pathScopeMu.RLock()
	sc, ok := pathScopes[kind]
	pathScopeMu.RUnlock()
	if !ok || sc.list == nil {
		return nil
	}
	out := sc.list(user, name)
	sort.Strings(out)
	return out
}

// PathScopeKnown reports whether a kind is registered — for validating a
// declaration at AUTHORING time, so a typo is caught while someone is
// looking at it rather than on the first call.
func PathScopeKnown(ref string) bool {
	kind, _ := SplitPathScopeRef(ref)
	pathScopeMu.RLock()
	defer pathScopeMu.RUnlock()
	_, ok := pathScopes[kind]
	return ok
}
