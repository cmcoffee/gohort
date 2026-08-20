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

// agentToolNames is what ONE agent can reach, by name.
//
// knownAgentToolNames answers a different question — "does anybody running
// this machine hold this name" — which is the right generosity for a save-time
// typo check and the wrong one for attaching: the moment that matters is when
// a machine meets a PARTICULAR agent, and a name three other agents hold does
// this one no good at all.
//
// Attached sources are resolved from the record rather than from a turn, so
// this can answer before any turn exists. That is the whole point of a
// preflight: the alternative is finding out on the first message.
func agentToolNames(udb Database, user string, ag AgentRecord) map[string]bool {
	known := make(map[string]bool, 256)
	for n := range machineControlTools {
		known[n] = true
	}
	for _, o := range availableWorkerToolOptions(user) {
		known[o.Value] = true
	}
	for _, ct := range RegisteredChatTools() {
		known[ct.Name()] = true
	}
	sess := &ToolSession{Username: user}
	for _, td := range Secure().BuildTools(sess) {
		known[td.Tool.Name] = true
	}
	for _, td := range AgentProvidedTools(sess, user, ag.ID) {
		known[td.Tool.Name] = true
	}
	// What THIS agent's attachments mint — the names no picker offers and
	// the ones most likely to differ between two agents.
	for _, ref := range ag.AttachedSources {
		for _, td := range ReferenceItemTools(user, strings.TrimSpace(ref.Kind), strings.TrimSpace(ref.ItemID)) {
			known[td.Tool.Name] = true
		}
	}
	return known
}

// machineAttachGaps reports what this machine's steps name that this agent
// cannot reach — the preflight for attaching one to the other.
//
// Attaching is the moment the question becomes answerable and the moment
// somebody is looking. Before this, the first report was a turnDiag on the
// first message, hours later, phrased as a tool that had gone missing.
func machineAttachGaps(udb Database, user string, def MachineDef, ag AgentRecord) []string {
	known := agentToolNames(udb, user, ag)
	var out []string
	for _, p := range def.Phases {
		var missing []string
		for _, n := range p.Tools {
			if n = strings.TrimSpace(n); n == "" || n == NoToolsMarker || known[n] {
				continue
			}
			missing = append(missing, n)
		}
		if len(missing) > 0 {
			out = append(out, "step "+p.Name+" names "+strings.Join(missing, ", ")+
				", which "+chFirst(ag.Name, ag.ID)+" does not carry")
		}
	}
	return out
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

// reachAdvice suggests the coarse control to a step that is approximating it
// with a list of names.
//
// Not a rewrite, and deliberately not phrased as one: a reach and a name list
// are not the same statement. "read" grants every read tool the agent has, and
// a step that named three of them on purpose is narrower BY DESIGN. So this
// fires only where the list is paying the fragility without buying the
// precision — where what it names is a class the author could have said in a
// word.
//
// Two triggers, both about what the names DEPEND on rather than what they say:
//
//   - The list is entirely read-only tools. Then "reach: read" expresses the
//     same intent, and keeps expressing it when this machine is carried to
//     another agent, another deployment, or the same agent after an MCP server
//     reconnects under different names.
//   - The list names a tool that exists only while something else is true — an
//     MCP server is connected, an attachment is in place. Those are the names
//     that stop resolving with nobody having edited the machine.
//
// Silent otherwise, which is most steps. Advice that fires on a correct
// configuration is advice people learn to scroll past, and it takes the
// findings that matter down with it.
func reachAdvice(udb Database, user string, def MachineDef) []string {
	named := false
	for _, p := range def.Phases {
		if len(p.Tools) > 0 && PhaseReach(p) == ReachAll {
			named = true
			break
		}
	}
	if !named {
		return nil
	}
	caps, dynamic := toolCapIndex(user, def)
	var out []string
	for _, p := range def.Phases {
		if len(p.Tools) == 0 || PhaseReach(p) != ReachAll {
			continue
		}
		allRead, anyDynamic, known := true, false, 0
		var fragile []string
		for _, n := range p.Tools {
			n = strings.TrimSpace(n)
			if n == "" || n == NoToolsMarker {
				continue
			}
			cs, ok := caps[n]
			if !ok {
				continue // a name nothing answers to is the other check's business
			}
			known++
			if !capsAreReadOnly(cs) {
				allRead = false
			}
			if dynamic[n] {
				anyDynamic = true
				fragile = append(fragile, n)
			}
		}
		if known == 0 {
			continue
		}
		switch {
		case anyDynamic:
			out = append(out, "step "+p.Name+" names "+strings.Join(fragile, ", ")+
				" — names that exist only while the server or attachment behind them does. A reach "+
				"(\"read\", \"none\") says what the step may DO without depending on what happens to be connected.")
		case allRead && known > 1:
			out = append(out, "step "+p.Name+" names "+strconv.Itoa(known)+
				" tools that all only read. If what you mean is \"this step may look, not act\", reach \"read\" "+
				"says it in a word and keeps saying it on another agent — the list grants exactly these and "+
				"nothing else, which is narrower, so keep it if that is the point.")
		}
	}
	return out
}

// toolCapIndex maps a tool name to its declared capabilities, and marks the
// ones whose existence is conditional — minted by an MCP server that has to be
// connected, or by an attachment on one agent.
//
// Both halves come from the same walk, because they answer one question about
// each name: what does it let a step do, and will it still be there tomorrow.
func toolCapIndex(user string, def MachineDef) (map[string][]Capability, map[string]bool) {
	caps := make(map[string][]Capability, 256)
	dynamic := map[string]bool{}
	for _, ct := range RegisteredChatTools() {
		caps[ct.Name()] = ChatToolCaps(ct)
		// An MCP proxy is registered like anything else, and disappears the
		// same way its server does.
		if strings.HasPrefix(ToolCategory(ct), MCPToolCategory("")+":") {
			dynamic[ct.Name()] = true
		}
	}
	sess := &ToolSession{Username: user}
	for _, td := range Secure().BuildTools(sess) {
		caps[td.Tool.Name] = td.Tool.Caps
	}
	// Attachment-minted names: per agent, so a machine carried elsewhere may
	// not find them at all.
	for _, g := range ReferenceGroups(user) {
		for _, it := range g.Items {
			for _, td := range ReferenceItemTools(user, g.Kind, it.ID) {
				caps[td.Tool.Name] = td.Tool.Caps
				dynamic[td.Tool.Name] = true
			}
		}
	}
	return caps, dynamic
}

// capsAreReadOnly reports whether a tool only reads. An UNANNOTATED tool
// (empty Caps) is not read-only for this purpose: the whole point of the
// advice is that the author can trust the word, and guessing "probably fine"
// about a tool nobody classified is how a step that posts ends up inside
// "this step may look, not act".
func capsAreReadOnly(cs []Capability) bool {
	if len(cs) == 0 {
		return false
	}
	for _, c := range cs {
		if c != CapRead {
			return false
		}
	}
	return true
}
