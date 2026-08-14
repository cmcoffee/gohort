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

// PathScopeRoot is one registered root, for a picker or a prompt that
// has to name the choices.
type PathScopeRoot struct {
	Ref    string // "kind:name", what a parameter declares
	Label  string // human name
	Detail string // what lands there, if the owner said
}

// PathScopeRootsFunc enumerates the roots of one kind for a user.
type PathScopeRootsFunc func(user string) []PathScopeRoot

// PathScope is one kind's implementation.
type PathScope struct {
	// Resolve proves a value lands inside a named root and returns its
	// absolute path. Required.
	Resolve PathScopeResolver
	// Values lists what is currently valid inside one root. Optional.
	Values PathScopeLister
	// Roots enumerates the roots of this kind. Optional, and what lets a
	// tool AUTHOR discover that a constraint is available at all — an
	// unadvertised constraint is one nobody declares.
	Roots PathScopeRootsFunc
}

type pathScope = PathScope

var (
	pathScopeMu sync.RWMutex
	pathScopes  = map[string]pathScope{}
)

// RegisterPathScope registers a resolver for one kind ("files"). Called
// at startup by the app that owns those roots, the same way a reference
// source or a tool provider registers.
func RegisterPathScope(kind string, sc PathScope) {
	if strings.TrimSpace(kind) == "" || sc.Resolve == nil {
		return
	}
	pathScopeMu.Lock()
	defer pathScopeMu.Unlock()
	pathScopes[kind] = sc
}

// PathScopeRoots lists every registered root across every kind, for a
// prompt or picker that has to say what constraints are available.
//
// Sorted, because it lands in a prompt: a list that reshuffles between
// calls costs a prefix-cache miss for nothing.
func PathScopeRoots(user string) []PathScopeRoot {
	pathScopeMu.RLock()
	kinds := make([]string, 0, len(pathScopes))
	for k := range pathScopes {
		kinds = append(kinds, k)
	}
	pathScopeMu.RUnlock()
	sort.Strings(kinds)

	var out []PathScopeRoot
	for _, k := range kinds {
		pathScopeMu.RLock()
		sc := pathScopes[k]
		pathScopeMu.RUnlock()
		if sc.Roots == nil {
			continue
		}
		roots := sc.Roots(user)
		sort.Slice(roots, func(i, j int) bool { return roots[i].Ref < roots[j].Ref })
		out = append(out, roots...)
	}
	return out
}

// SplitPathScopeRef splits a "kind:name" reference.
func SplitPathScopeRef(ref string) (kind, name string) {
	ref = strings.TrimSpace(ref)
	if i := strings.Index(ref, ":"); i >= 0 {
		return strings.TrimSpace(ref[:i]), strings.TrimSpace(ref[i+1:])
	}
	return ref, ""
}

// AgentHoldsReference, when set, reports whether agentID — belonging to
// user — carries the reference source item (kind, itemID) as an
// attachment. Wired by the app that owns agent records (orchestrate);
// nil in a deployment without one.
//
// This is the seam that lets a path scope ask a question it otherwise
// cannot: a producer app knows its own roots and knows nothing about
// agents, and the consumer that knows about agents cannot be imported by
// the producer without a cycle.
var AgentHoldsReference func(user, agentID, kind, itemID string) bool

// ResolvePathScope checks value against the root named by ref
// ("kind:name") and returns the absolute path it resolves to.
//
// agentID is the agent whose turn is running, or "" when no agent is in
// play (a CLI path, a test). When the scope's kind is ALSO an attachable
// reference source, the agent must carry that attachment — the scope is
// then gated by the same link the Sources picker manages, rather than by
// the user alone.
//
// That gate exists because the two halves had drifted apart. A file
// store's own tools (search_<store>, read_<store>) appear only on an
// agent it is attached to, but a servitor command tool declaring
// path_scope resolved against the USER, so an agent nobody had linked
// the store to could still run a command against it. One link, one
// meaning: "this agent may reach this folder".
//
// Three ways this deliberately does NOT refuse:
//
//   - agentID == "": no agent context to check. The user gate still
//     applies, and refusing here would break every non-agent caller.
//   - AgentHoldsReference == nil: no app owns agent records in this
//     deployment, so there is no attachment to have. Refusing would make
//     the feature dead rather than ungated.
//   - the kind is not a reference source: nothing to attach, so
//     attachment cannot be required. A future scope kind with no picker
//     behind it keeps working.
//
// An UNKNOWN kind is still an error rather than a pass. A parameter
// declaring a constraint nobody implements has to fail closed: the
// alternative is a tool that silently stops being constrained the day
// its app is compiled out, which is exactly when nobody is looking.
func ResolvePathScope(user, agentID, ref, value string) (string, error) {
	kind, name := SplitPathScopeRef(ref)
	pathScopeMu.RLock()
	sc, ok := pathScopes[kind]
	pathScopeMu.RUnlock()
	if !ok {
		return "", Error("this parameter is limited to " + strconv.Quote(kind) +
			", and nothing here provides that. Nothing ran. Tell the person the constraint names a source their deployment does not have.")
	}
	if agentID != "" && AgentHoldsReference != nil && ReferenceSourceKnown(kind) {
		if !AgentHoldsReference(user, agentID, kind, name) {
			// Says what would change the answer. The model cannot attach
			// anything itself, so the sentence is aimed at the person who
			// will read the refusal — and a sub-agent needs its OWN link,
			// which is the case most likely to surprise.
			return "", Error("this agent is not linked to " + strconv.Quote(ref) +
				", so nothing ran. Attach it to this agent under Configure → Sources. " +
				"A sub-agent needs its own link; holding it on the parent is not enough.")
		}
	}
	return sc.Resolve(user, name, value)
}

// PathScopeChoices lists the currently valid values for a scope, for a
// tool description. Empty when the scope has no lister or nothing is
// there.
func PathScopeChoices(user, ref string) []string {
	kind, name := SplitPathScopeRef(ref)
	pathScopeMu.RLock()
	sc, ok := pathScopes[kind]
	pathScopeMu.RUnlock()
	if !ok || sc.Values == nil {
		return nil
	}
	out := sc.Values(user, name)
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
