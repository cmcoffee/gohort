// Which apps have granted this agent anything, and where to go and change it.
//
// Six answers to "may this agent use that" already exist — OwnedBy,
// AllowedUsers, AgentScope, channel send authority, API-key tool scope, and
// servitor's per-machine command grants. Each was built for its own case and
// each has a genuinely different shape, which is the argument for a seam and
// the argument against a single permission model in equal measure.
//
// So this generalizes TWO of the three layers and deliberately not the third.
//
//	identity    which app, which agent          — uniform, lives here
//	visibility  what does it hold, where to edit — uniform, lives here
//	meaning     what the grant PERMITS           — app's own, stays there
//
// Forcing the third into a shared level enum is how these end up worse than
// what they replaced: risk categories, chat ids, credential names and tool
// allowlists do not reduce to Read/Write/Admin, and the moment they are made
// to, every app smuggles its real structure through a metadata blob and the
// abstraction has bought nothing but indirection.
//
// WHAT IS ENFORCED HERE is the one rule that IS uniform: absent means denied.
// A grantor reports what an agent HOLDS; there is no path through this file by
// which an app can report a default. An app that wants a capability on for
// everyone has to say so per agent, where somebody can see it and take it away.
package core

import (
	"sort"
	"strings"
	"sync"
)

// AgentGrant is one thing an app has given one agent. Label is what a person
// recognizes it by; Detail says how much, in that app's own terms, because
// only the app knows what its permissions mean.
type AgentGrant struct {
	Label  string `json:"label"`            // "Lab Box", "#ops", "billing-api"
	Detail string `json:"detail,omitempty"` // "nothing without asking", "read-only"
}

// AgentGrantor is an app that can give agents access to things it owns.
//
// Granted returns what this agent HOLDS — never what it could hold, and never
// a default. An app with nothing granted returns nil, and the row reads "none",
// which is the honest answer and the one that makes an unexpected grant
// visible.
type AgentGrantor struct {
	// Name is the stable id, used for ordering and logging. Label is what the
	// agent editor shows, and should name the RESOURCE ("Machines"), not the
	// app — a person reading an agent's capabilities is asking what it can
	// reach, not which package implements it.
	Name  string
	Label string
	// Granted lists what this agent holds. Called on a page render, so it must
	// be cheap and must not block.
	Granted func(user, agentID string) []AgentGrant
	// ManageURL is where the owner goes to change it. The app owns the detail
	// UI, because the detail is the part that does not generalize.
	ManageURL string
}

var (
	agentGrantorMu sync.RWMutex
	agentGrantors  = map[string]AgentGrantor{}
)

// RegisterAgentGrantor installs one. Call at startup from the app that owns the
// records. A repeat registration replaces and says so, for the same reason the
// tool provider registry does: which one survived would otherwise depend on map
// iteration.
func RegisterAgentGrantor(g AgentGrantor) {
	if strings.TrimSpace(g.Name) == "" || g.Granted == nil {
		return
	}
	agentGrantorMu.Lock()
	defer agentGrantorMu.Unlock()
	if _, dup := agentGrantors[g.Name]; dup {
		Log("[grants] agent grantor %q registered twice — replacing the earlier one", g.Name)
	}
	agentGrantors[g.Name] = g
}

// AgentGrantSummary is one app's answer for one agent, ready to render.
type AgentGrantSummary struct {
	Name      string       `json:"name"`
	Label     string       `json:"label"`
	Grants    []AgentGrant `json:"grants,omitempty"`
	ManageURL string       `json:"manage_url,omitempty"`
	// Text is the row's own sentence — "2 machines" or "none" — so a caller
	// rendering a list does not have to reimplement the empty case and get it
	// subtly different from every other caller.
	Text string `json:"text"`
}

// AgentGrantSummaries reports what every registered app has given one agent,
// in a stable order.
//
// EVERY grantor appears, including those granting nothing. A row reading "none"
// is what tells an owner the capability exists and that this agent does not
// have it; omitting empties would make an app invisible until the moment it
// mattered, which is the wrong moment to discover it.
func AgentGrantSummaries(user, agentID string) []AgentGrantSummary {
	if strings.TrimSpace(agentID) == "" {
		return nil
	}
	agentGrantorMu.RLock()
	list := make([]AgentGrantor, 0, len(agentGrantors))
	for _, g := range agentGrantors {
		list = append(list, g)
	}
	agentGrantorMu.RUnlock()

	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	out := make([]AgentGrantSummary, 0, len(list))
	for _, g := range list {
		grants := safeGranted(g, user, agentID)
		out = append(out, AgentGrantSummary{
			Name: g.Name, Label: labelOr(g.Label, g.Name),
			Grants: grants, ManageURL: g.ManageURL, Text: grantText(grants),
		})
	}
	return out
}

// safeGranted runs one grantor behind a recover. An app that panics while
// listing grants reports NOTHING — never everything — because the failure mode
// of a permissions display that guesses is that somebody reads a capability
// their agent does not have, or misses one it does.
func safeGranted(g AgentGrantor, user, agentID string) (out []AgentGrant) {
	defer func() {
		if r := recover(); r != nil {
			Log("[grants] grantor %q panicked listing grants for agent %q (%v) — reporting none", g.Name, agentID, r)
			out = nil
		}
	}()
	return g.Granted(user, agentID)
}

// grantText renders the row's sentence. The empty case is a word, not a dash:
// "none" is a statement about this agent, and a dash reads like missing data.
func grantText(grants []AgentGrant) string {
	switch len(grants) {
	case 0:
		return "none"
	case 1:
		return grants[0].Label
	case 2:
		return grants[0].Label + " and " + grants[1].Label
	}
	return grants[0].Label + ", " + grants[1].Label + " and " + intToString(len(grants)-2) + " more"
}

func labelOr(label, fallback string) string {
	if l := strings.TrimSpace(label); l != "" {
		return l
	}
	return fallback
}

// --- confirmation ------------------------------------------------------------
//
// What it means for a tool to ask before it acts, and what "don't ask me
// again" is allowed to mean afterwards.
//
// A prompt on every call is a prompt nobody reads. The loop that makes an
// agentic coding session worth having is edit → build → read the error → fix
// → build, and if each build stops for approval then the safe configuration
// is the one that is too tedious to use — which is how gates get switched off
// wholesale. So the answer to a confirmation has three shapes, not two: no,
// yes this once, and yes to this KIND of call from now on.
//
// The third one is where the care goes. A grant is a standing decision made
// in one click during a task, so it has to be narrow enough that the user can
// predict what they just allowed:
//
//   - It is namespaced by Scope, an opaque key the app supplies. Allowing a
//     build in a throwaway checkout must not allow one in the source tree the
//     server is running from, and only the app knows those are different.
//   - It covers a tool outright ONLY when the tool has no meaningful argument
//     to vary — an operator-defined "run the tests" command is one fixed
//     string, so "always" is exactly as broad as it sounds.
//   - Where there IS a varying argument (a shell command), the grant is a
//     PREFIX of it, and prefix matching refuses anything a shell would treat
//     as more than one command. See CommandIsGrantable for why that guard is
//     load-bearing rather than defensive.
//   - A tool can refuse to offer one at all (NeverRemember), for actions
//     where a standing yes is not a thing a person should be able to hand out
//     mid-flow.

// ToolConfirmation describes a tool's confirmation behavior.
type ToolConfirmation struct {
	// Prompt is the question the user reads. It is prose, and it is the
	// tool author's job because only they can write one worth interrupting
	// for: "Allow run?" is a reflex click, "Run a command in gohort?" over
	// the command itself is a decision.
	//
	// FAILS CLOSED. A run with no interactive viewer (a schedule, a
	// dispatch, a channel wake) has nobody to ask, so the call is denied
	// rather than allowed. A tool that must work unattended must not set a
	// confirmation at all.
	Prompt string

	// Scope namespaces any grant the user hands out from this call's card.
	// Opaque to the framework — an app passes whatever "the same situation"
	// means to it (a project id, a workspace, a connection). Empty puts
	// grants in a namespace shared by everything else that left it empty,
	// which is rarely what an app wants and never what a dangerous tool
	// wants.
	Scope string

	// GrantArg names the argument a remembered grant is matched on. When
	// set, a grant records a PREFIX of that argument's value and applies
	// only to later calls whose value starts with it. When empty, a grant
	// covers the tool outright — correct only when the tool has no varying
	// argument that changes what it does.
	GrantArg string

	// NeverRemember withholds the "always allow" option, leaving only
	// once-or-deny. For calls where a standing yes should not be obtainable
	// by clicking a third button in the middle of something else.
	NeverRemember bool
}

// asks reports whether this confirmation actually gates anything. A nil
// confirmation, or one with no question in it, does not.
func (c *ToolConfirmation) asks() bool {
	return c != nil && strings.TrimSpace(c.Prompt) != ""
}

// Asks is the exported form, for the layers outside core that decide whether
// to escalate.
func (c *ToolConfirmation) Asks() bool { return c.asks() }

// CanRemember reports whether this call may offer a standing grant.
func (c *ToolConfirmation) CanRemember() bool {
	return c.asks() && !c.NeverRemember
}

// shellMetaChars are the characters that let one command line become more
// than one command, or become a command whose text is computed at run time.
const shellMetaChars = ";&|`$><\n\r()"

// CommandIsGrantable reports whether a command line may take part in prefix
// matching at all.
//
// This is the guard that decides whether the whole grant mechanism is a
// convenience or a hole. Without it, granting the prefix "go build" would
// also allow:
//
//	go build ./... ; rm -rf /
//
// which starts with the granted prefix, was never shown to the user, and
// would run without a prompt. So a command containing anything a shell reads
// as chaining, substitution, or redirection is never matched against a grant
// and never offered as one — it goes to the user every time, which is the
// correct answer for a line that does more than one thing.
//
// Deliberately a blunt character test rather than a parser. A parser that is
// subtly wrong here fails open, and the cost of the blunt version is only
// that a legitimate piped command keeps asking.
func CommandIsGrantable(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return false
	}
	return !strings.ContainsAny(cmd, shellMetaChars)
}

// GrantPrefixFor derives the prefix a card offers for a command: its leading
// words, up to the first one that looks like an argument rather than part of
// the verb.
//
// "go build ./..."        → "go build"
// "npm run test -- -w"    → "npm run"
// "make"                  → "make"
// "./scripts/ci.sh --fast"→ "./scripts/ci.sh"
//
// Two words at most, because the useful unit is the verb ("go test") and
// anything past it is the part that legitimately varies between iterations —
// which is the whole reason a prefix beats an exact match here. Returns ""
// when the command may not be granted at all.
func GrantPrefixFor(cmd string) string {
	if !CommandIsGrantable(cmd) {
		return ""
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}
	prefix := fields[0]
	// A second word joins the verb only when it is a bare subcommand — not a
	// flag, not a path, not a value. "go build" is a verb; "make -j8" is a
	// verb plus a setting, and granting "make -j8" would be narrower than the
	// user expects rather than broader.
	if len(fields) > 1 && isBareWord(fields[1]) {
		prefix += " " + fields[1]
	}
	return prefix
}

// isBareWord reports whether s is a plain subcommand token.
func isBareWord(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") || strings.ContainsAny(s, "/\\=.") {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') &&
			!(r >= '0' && r <= '9') && r != '_' && r != ':' {
			return false
		}
	}
	return true
}

// CommandMatchesPrefix reports whether cmd is covered by a granted prefix.
//
// The match is at a WORD boundary, so a grant of "go build" does not cover
// "go buildsomethingelse". Both sides must be grantable, which is what stops
// a chained command from riding in on a grant made for its first clause.
func CommandMatchesPrefix(cmd, prefix string) bool {
	cmd = strings.TrimSpace(cmd)
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || !CommandIsGrantable(cmd) {
		return false
	}
	if cmd == prefix {
		return true
	}
	return strings.HasPrefix(cmd, prefix+" ")
}
