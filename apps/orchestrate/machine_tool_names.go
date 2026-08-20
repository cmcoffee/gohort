// Catching a phase's mistyped tool name at SAVE time.
//
// A phase's Tools list is an exact-name filter (resolveStageTools). A name that
// misses matches nothing, and the cost lands at RUN time, hours later, on a
// turn nobody is watching: the tool is simply absent, the model reads that as a
// name it got wrong, and it retries spellings or reroutes. The author sees a
// step that "stopped working" with a list in front of them that still looks
// right.
//
// The class of typo is predictable, because the catalog name is not always the
// name the author has in hand. A remote MCP tool is published as
// "<server>_<tool>", lowercased (mcpExposedName → sanitizeToolName), so an
// author copying the server's own list writes atlassian_getConfluencePage and
// the catalog holds atlassian_getconfluencepage. That is one letter-case away
// from correct and impossible to see by eye.
//
// This reports, and does not refuse. The editor's whole posture is that a
// half-built machine is the normal state (see MachineDef.Problems), and the
// catalog is genuinely dynamic — an app contributes tools per agent at run
// time, a credential can be added tomorrow, an MCP server registers its tools
// when it first connects. A name we cannot resolve today is usually a typo and
// occasionally early. Saying so belongs in the checklist; blocking the save
// would be wrong in both cases.
package orchestrate

import (
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// knownAgentToolNames is every tool name a phase could legitimately hold,
// gathered from the same places the runtime assembles a catalog from.
//
// Deliberately generous. A false positive here is worse than a miss: it puts a
// permanent red mark against a correct name, and a checklist that cries wolf
// gets ignored wholesale — including the entry that was right.
func knownAgentToolNames(udb Database, user string, def MachineDef) map[string]bool {
	// Walking the agent list and synthesizing the credential tools is real
	// work; the common machine names no tools at all and needs none of it.
	named := false
	for _, p := range def.Phases {
		if len(p.Tools) > 0 {
			named = true
			break
		}
	}
	if !named {
		return nil
	}
	known := make(map[string]bool, 256)
	// The control plane survives narrowing whatever a phase says, so naming
	// one is redundant rather than wrong.
	for n := range machineControlTools {
		known[n] = true
	}
	// The curation picker's own pool: registered tools filtered by capability,
	// plus this user's shared persistent temp tools — and the tools this
	// user's attached reference sources mint, which live in no registry and
	// would otherwise be reported as typos every time an author named one.
	for _, o := range phaseToolOptions(user) {
		known[o.Value] = true
	}
	// Everything registered, including what the picker hides on purpose —
	// framework utilities, superseded backends, per-connector image tools. The
	// picker not offering a name is not the same as the name not existing, and
	// an agent already allowlisted onto one keeps working.
	for _, ct := range RegisteredChatTools() {
		known[ct.Name()] = true
	}
	// Credential-backed tools (fetch_url_<cred>) are synthesized per session
	// and never registered globally. A session carrying just the username is
	// enough to resolve which ones this user would get.
	sess := &ToolSession{Username: user}
	for _, td := range Secure().BuildTools(sess) {
		known[td.Tool.Name] = true
	}
	// App-contributed tools (servitor's per-appliance grants, and whatever
	// binds next) attach per AGENT at run time and appear in no catalog until
	// then. Ask on behalf of the agents that actually run this machine.
	for _, a := range listAgents(udb, user) {
		if a.Machine != def.ID {
			continue
		}
		for _, td := range AgentProvidedTools(sess, user, a.ID) {
			known[td.Tool.Name] = true
		}
	}
	return known
}

// unknownPhaseToolFindings reports phase tool names nothing in the catalog
// answers to, one checklist line each, with the likely correction when the
// name is recoverable.
func unknownPhaseToolFindings(udb Database, user string, def MachineDef) []string {
	return phaseToolFindings(def, knownAgentToolNames(udb, user, def))
}

// phaseToolFindings is the judgement, separated from gathering the catalog so
// the rules can be tested against a catalog written out by hand rather than
// against whatever the process happens to have registered.
func phaseToolFindings(def MachineDef, known map[string]bool) []string {
	var out []string
	for _, p := range def.Phases {
		matched := 0
		var missing []string
		for _, n := range p.Tools {
			if n = strings.TrimSpace(n); n == "" || n == noToolsSentinel {
				continue // the explicit "nothing" is a setting, not a name
			}
			if known[n] {
				matched++
				continue
			}
			missing = append(missing, n)
		}
		for _, n := range missing {
			line := "step " + p.Name + ": tool " + strconv.Quote(n) + " is not a tool this agent can reach"
			if suggestion := didYouMeanTool(n, known); suggestion != "" {
				line += " — did you mean " + strconv.Quote(suggestion) + "?"
			} else {
				line += ` — names must match the catalog exactly (a remote MCP tool is published as "<server>_<tool>", lowercased)`
			}
			out = append(out, line)
		}
		// The whole list missing is its own finding, because the consequence
		// is different in kind: the step keeps its tools rather than losing
		// them (narrowCatalog refuses to resolve a total miss to nothing), so
		// it runs UNNARROWED — the opposite of what the list was written for.
		if matched == 0 && len(missing) > 0 {
			out = append(out, "step "+p.Name+": none of its "+strconv.Itoa(len(missing))+
				" tool names exist, so the step runs with the FULL catalog instead of the ones it names")
		}
	}
	return out
}

// didYouMeanTool recovers the correction for the ways a phase name misses
// that are mechanical rather than imaginative: the case the author had, the
// separator they used, and the raw remote name before a server prefixed it.
// Returns "" when nothing close enough is known — a wrong guess costs more
// than no guess.
func didYouMeanTool(name string, known map[string]bool) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	// Case, and the dot an author writes between server and tool because that
	// is how MCP itself spells the pair.
	for _, cand := range []string{lower, strings.ReplaceAll(lower, ".", "_"), strings.ReplaceAll(lower, "-", "_")} {
		if cand != name && known[cand] {
			return cand
		}
	}
	// The raw remote name, unprefixed: "getConfluencePage" against a catalog
	// holding "atlassian_getconfluencepage". Only when exactly one tool ends
	// that way — two servers offering "search" is precisely when a guess
	// would send someone to the wrong system.
	suffix := "_" + lower
	hit := ""
	for k := range known {
		if strings.HasSuffix(k, suffix) {
			if hit != "" {
				return ""
			}
			hit = k
		}
	}
	return hit
}

// machineChecklist is what the editor shows as work remaining: the
// definition's own problems, then the tool names that will not resolve.
//
// One function so the page, the modal spec, and every save answer with the
// same list. They used to answer with def.Problems() individually, which is
// how a check added in one place quietly failed to exist in the others.
func machineChecklist(udb Database, user string, def MachineDef) []string {
	return append(def.Problems(), unknownPhaseToolFindings(udb, user, def)...)
}
