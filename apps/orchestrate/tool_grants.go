// Standing answers to a confirmation: what the user allowed, and how narrowly.
//
// A grant is created by one click on an approval card ("Always allow ...") and
// consulted before every later escalation. It lives in the granting user's own
// store, so one person's standing yes is never another's.
//
// The shape is deliberately small. A grant names a scope, a tool, and — where
// the tool has a varying argument — a prefix of it. There is no wildcard
// syntax, no allow-everything entry, and no way to write one by hand from the
// browser: the only thing that mints a grant is a user answering a card that
// showed them the actual call. That is what keeps "what did I agree to" a
// question with a readable answer.
//
// See core.ToolConfirmation and core.CommandIsGrantable for the matching rules
// and, in particular, for why a chained shell command can never match one.
package orchestrate

import (
	"net/http"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// toolGrantTable holds ToolGrant records in the granting user's own store.
const toolGrantTable = "tool_grants"

// ToolGrant is one standing approval.
type ToolGrant struct {
	ID string `json:"id"`
	// Scope is the app-supplied namespace the grant applies within (a
	// project id, a workspace). A grant never applies outside it.
	Scope string `json:"scope"`
	// Tool is the tool name the grant covers.
	Tool string `json:"tool"`
	// Prefix, when set, narrows the grant to calls whose granted argument
	// starts with it. Empty covers the tool outright within the scope.
	Prefix string `json:"prefix,omitempty"`
	// Label is what the card said the user was allowing, kept verbatim so
	// the manager screen can show them the sentence they agreed to rather
	// than a reconstruction of it.
	Label string    `json:"label,omitempty"`
	At    time.Time `json:"at"`
}

// covers reports whether this grant answers a call.
func (g ToolGrant) covers(scope, tool, argValue string) bool {
	if g.Scope != scope || g.Tool != tool {
		return false
	}
	if g.Prefix == "" {
		return true
	}
	return CommandMatchesPrefix(argValue, g.Prefix)
}

// listToolGrants returns a user's grants, newest first. A blank scope lists
// every scope, for the manager screen.
func listToolGrants(udb Database, scope string) []ToolGrant {
	out := []ToolGrant{}
	if udb == nil {
		return out
	}
	for _, id := range udb.Keys(toolGrantTable) {
		var g ToolGrant
		if !udb.Get(toolGrantTable, id, &g) {
			continue
		}
		if scope != "" && g.Scope != scope {
			continue
		}
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// findToolGrant returns the grant covering this call, if the user has one.
func findToolGrant(udb Database, scope, tool, argValue string) (ToolGrant, bool) {
	for _, g := range listToolGrants(udb, scope) {
		if g.covers(scope, tool, argValue) {
			return g, true
		}
	}
	return ToolGrant{}, false
}

// commandParts splits a command line chained with &&, ||, ; or | into the
// simple commands it runs, so each can be checked against a grant the way
// Claude Code checks a compound command: the line runs unasked only when every
// part is allowed. False when the line cannot be split with certainty, and
// then it is asked about whole, as before:
//
//   - command or process substitution, variables, subshells or grouping
//     ($, backticks, parentheses), a backslash, a newline, or a lone &
//     sending a command to the background;
//   - a redirect, except 2>&1 (and its kin) and one to /dev/null, which only
//     move or drop output;
//   - an unclosed quote.
//
// Quoted text is passed through with its quotes, so a part with a separator
// inside quotes ("a|b") is a part no grant matches (CommandIsGrantable) and is
// asked about. A single command comes back as one part.
func commandParts(cmd string) ([]string, bool) {
	var parts []string
	var cur strings.Builder
	end := func() bool {
		p := strings.Join(strings.Fields(cur.String()), " ")
		cur.Reset()
		if p == "" {
			return false
		}
		parts = append(parts, p)
		return true
	}
	rs := []rune(strings.TrimSpace(cmd))
	quote := rune(0)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if quote != 0 {
			if r == quote {
				quote = 0
			} else if quote == '"' && (r == '$' || r == '`' || r == '\\') {
				return nil, false
			}
			cur.WriteRune(r)
			continue
		}
		next := rune(0)
		if i+1 < len(rs) {
			next = rs[i+1]
		}
		switch {
		case r == '\'' || r == '"':
			quote = r
			cur.WriteRune(r)
		case r == '$' || r == '`' || r == '(' || r == ')' || r == '\\' || r == '\n' || r == '\r' || r == '{' || r == '}':
			return nil, false
		case r == ';':
			if !end() {
				return nil, false
			}
		case (r == '&' && next == '&') || (r == '|' && next == '|'):
			if !end() {
				return nil, false
			}
			i++
		case r == '|':
			if !end() {
				return nil, false
			}
		case r == '&' && next == '>', r == '>', r == '<', r >= '0' && r <= '9' && (next == '>' || next == '<') && (i == 0 || rs[i-1] == ' '):
			n := harmlessRedirect(rs[i:])
			if n == 0 {
				return nil, false
			}
			i += n - 1
			cur.WriteRune(' ')
		case r == '&':
			return nil, false
		default:
			cur.WriteRune(r)
		}
	}
	if quote != 0 || !end() {
		return nil, false
	}
	return parts, true
}

// harmlessRedirect is how many runes at the start of rs make a redirect that
// only moves or drops output (2>&1, >&2, >/dev/null, 2>/dev/null,
// &>/dev/null, </dev/null), or 0 for any other.
func harmlessRedirect(rs []rune) int {
	s := string(rs)
	for _, p := range []string{"&>", "2>>", "1>>", "2>", "1>", ">>", ">", "<"} {
		if !strings.HasPrefix(s, p) {
			continue
		}
		rest := s[len(p):]
		if strings.HasPrefix(p, "2") || strings.HasPrefix(p, "1") || p == ">" {
			if strings.HasPrefix(rest, "&1") || strings.HasPrefix(rest, "&2") {
				return len([]rune(p)) + 2
			}
		}
		trimmed := strings.TrimLeft(rest, " ")
		if strings.HasPrefix(trimmed, "/dev/null") {
			after := trimmed[len("/dev/null"):]
			if after == "" || strings.HasPrefix(after, " ") || strings.HasPrefix(after, ";") || strings.HasPrefix(after, "|") || strings.HasPrefix(after, "&") {
				return len([]rune(p)) + (len(rest) - len(trimmed)) + len("/dev/null")
			}
		}
		return 0
	}
	return 0
}

// isDirChange reports whether a command part only changes directory (cd, or
// cd to one place). It needs no grant: it decides where the next part runs,
// and that part is checked.
func isDirChange(part string) bool {
	f := strings.Fields(part)
	return len(f) >= 1 && len(f) <= 2 && f[0] == "cd"
}

// grantPrefixesFor is what a card for cmd offers to remember: the prefix of
// each part a grant would have to cover, once each, in order. Nil when the
// line cannot be split with certainty, or a part cannot be granted.
func grantPrefixesFor(cmd string) []string {
	parts, ok := commandParts(cmd)
	if !ok {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, p := range parts {
		if isDirChange(p) {
			continue
		}
		prefix := GrantPrefixFor(p)
		if prefix == "" {
			return nil
		}
		if !seen[prefix] {
			seen[prefix] = true
			out = append(out, prefix)
		}
	}
	return out
}

// compoundGrant answers a chained command line when every command in it is
// covered by a grant the user gave: the grants used, as one for the note in
// the conversation. A line of only cd, or one that cannot be split with
// certainty, is not answered.
func compoundGrant(udb Database, scope, tool, argValue string) (ToolGrant, bool) {
	parts, ok := commandParts(argValue)
	if !ok || len(parts) < 2 {
		return ToolGrant{}, false
	}
	var used []string
	seen := map[string]bool{}
	for _, p := range parts {
		if isDirChange(p) {
			continue
		}
		g, ok := findToolGrant(udb, scope, tool, p)
		if !ok {
			return ToolGrant{}, false
		}
		if label := firstNonBlank(g.Prefix, tool); !seen[label] {
			seen[label] = true
			used = append(used, label)
		}
	}
	if len(used) == 0 {
		return ToolGrant{}, false
	}
	return ToolGrant{Scope: scope, Tool: tool, Prefix: strings.Join(used, ", ")}, true
}

// saveToolGrant records a standing approval, replacing any existing grant that
// would already have covered the same ground.
//
// Replacing rather than appending keeps the list readable: clicking "always"
// twice on the same command should leave one entry saying what you allowed,
// not two saying it twice. A BROADER new grant also absorbs the narrower ones
// it makes redundant, so the manager screen never shows an entry that can
// never be the reason a call went through.
func saveToolGrant(udb Database, g ToolGrant) ToolGrant {
	if udb == nil {
		return g
	}
	g.ID = UUIDv4()
	g.At = time.Now().UTC()
	for _, old := range listToolGrants(udb, g.Scope) {
		if old.Tool != g.Tool {
			continue
		}
		// The new grant makes the old one redundant when it covers at least
		// as much: a tool-wide grant absorbs every prefix, and a shorter
		// prefix absorbs the longer ones under it.
		if g.Prefix == "" || (old.Prefix != "" && CommandMatchesPrefix(old.Prefix, g.Prefix)) {
			udb.Unset(toolGrantTable, old.ID)
		}
	}
	udb.Set(toolGrantTable, g.ID, g)
	return g
}

// deleteToolGrant revokes one grant.
func deleteToolGrant(udb Database, id string) bool {
	if udb == nil {
		return false
	}
	id = strings.TrimSpace(id)
	var g ToolGrant
	if !udb.Get(toolGrantTable, id, &g) {
		return false
	}
	udb.Unset(toolGrantTable, id)
	return true
}

// --- the manager surface -----------------------------------------------------

// PublicHandleGrants serves a host app's Permissions screen: GET lists the
// caller's standing approvals, DELETE revokes one.
//
// A grant is only ever created by answering a card, but it has to be
// REVOCABLE somewhere that isn't mid-conversation — otherwise "always allow"
// is a decision you can make in one click and never take back, which is not a
// bargain anyone should be offered.
//
// scope narrows the listing to one app-supplied namespace; the app passes
// whatever it scoped its grants under, so one project's Permissions screen
// never shows (or revokes) another's.
func (T *OrchestrateApp) PublicHandleGrants(w http.ResponseWriter, r *http.Request, scope, grantID string) {
	jsonapi.Methods{
		"GET": jsonapi.Get(T.DB, func(c *jsonapi.Call) (any, error) {
			return listToolGrants(c.UDB, scope), nil
		}),
		"DELETE": jsonapi.Delete(T.DB, func(c *jsonapi.Call, _ struct{}) (any, error) {
			// Revoking is scoped the same way listing is: an id the caller
			// cannot see in this scope is one they cannot delete through it.
			for _, g := range listToolGrants(c.UDB, scope) {
				if g.ID == grantID {
					deleteToolGrant(c.UDB, grantID)
					return map[string]any{"ok": true}, nil
				}
			}
			return nil, errNotFound
		}),
	}.Serve(w, r)
}
