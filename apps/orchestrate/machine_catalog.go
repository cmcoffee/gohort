// The tool pool of a machine run with nobody watching.
//
// Every turn-free door built it from the registered tools alone: the Run
// panel, a schedule, a pipeline's machine stage, a dispatch from another
// agent. So a step pointed at a tool the owner built ("geo", say) failed at
// run time with "tool geo is not available to this agent", while the same
// machine worked when an agent's conversation hosted it, and the Run panel
// said every step runs "with your tools". Found by the tuning suite's
// machine task, whose good build could not pass.
//
// A machine run now reaches its owner's tools the way an agent with no
// allow-list does, and the unattended rules decide what may not run: the
// switches on the tool itself (turned off, Builder-only, bound-only, never
// unattended), the machine's own deny list, and the credential a tool
// dispatches through when that credential asks before every call. The same
// rule the scheduled-agent gate applies (autonomousToolAllowed), so the two
// unattended surfaces cannot disagree about a tool.

package orchestrate

import (
	"strconv"
	"strings"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/oddjob/tools/temptool"
)

// machineRunPool is what a machine run may reach, and what it may not and why.
type machineRunPool struct {
	Tools []AgentToolDef
	// Withheld maps a tool the owner HAS to why this run does not get it, so a
	// step naming one is told the reason instead of "not available".
	Withheld map[string]string
}

// names is every tool name the run reaches.
func (p machineRunPool) names() map[string]bool {
	out := make(map[string]bool, len(p.Tools))
	for _, td := range p.Tools {
		out[td.Tool.Name] = true
	}
	return out
}

// machineRunCatalog builds the pool for one unattended run of def, run by
// user. agentID is the agent behind the run when there is one (a dispatch),
// whose own never-unattended marks then apply too; empty for the Run panel,
// a schedule or a pipeline stage. udb is the user's own store.
func machineRunCatalog(udb Database, user, agentID string, def MachineDef) machineRunPool {
	sess := &ToolSession{Username: user, DB: AuthDB(), AgentID: agentID}
	if ws, err := EnsureWorkspaceDir(user); err == nil {
		sess.WorkspaceDir = ws
	}
	pool := machineRunPool{Withheld: map[string]string{}}
	builtins, err := GetAgentToolsWithSession(sess, availableWorkerToolNames()...)
	if err != nil {
		Log("[orchestrate.machines] run of %q: tool catalog partly unresolved for %q: %v", def.Name, user, err)
	}
	have := map[string]bool{}
	for _, td := range builtins {
		have[td.Tool.Name] = true
	}

	// The owner's pool, then what they took from others; an own tool wins a
	// name, as it does on a turn.
	loaded := LoadPersistentTempTools(AuthDB(), user)
	own := map[string]bool{}
	for _, p := range loaded {
		own[p.Tool.Name] = true
	}
	for _, lent := range AdoptedToolsFor(AuthDB(), user) {
		if !own[lent.Tool.Name] {
			loaded = append(loaded, lent.PersistentTempTool)
		}
	}
	never := autonomousNoUnattendedSet(udb, agentID)
	for _, p := range loaded {
		name := p.Tool.Name
		switch {
		case have[name]:
			continue // a built-in owns the name
		case p.Tool.Disabled:
			pool.Withheld[name] = "is turned off"
		case p.Tool.BuilderOnly:
			pool.Withheld[name] = "is reserved for Builder"
		case p.Tool.BoundOnly:
			pool.Withheld[name] = "is available only where it is bound"
		case len(p.ScopeAgents) > 0 && (agentID == "" || !p.ScopedToAgent(agentID)):
			pool.Withheld[name] = "belongs to particular agents, and this run has none of them behind it"
		case p.Tool.NoUnattended || never[name]:
			pool.Withheld[name] = "is marked never to run unattended"
		case toolAlwaysConfirms(udb, user, sess, name):
			pool.Withheld[name] = "dispatches through a credential that asks before every call, and nobody is here to answer"
		default:
			tool := p.Tool
			if err := sess.AppendTempTool(&tool); err != nil {
				Log("[orchestrate.machines] run of %q: tool %q failed to load: %v", def.Name, name, err)
			}
		}
	}
	pool.Tools = append(builtins, temptool.BuildAgentToolDefs(sess)...)
	pool.Tools = applyMachineDeny(def, pool.Tools, pool.Withheld)
	return pool
}

// applyMachineDeny removes what the machine denies, wherever it runs. withheld,
// when given, records why.
func applyMachineDeny(def MachineDef, tools []AgentToolDef, withheld map[string]string) []AgentToolDef {
	if len(def.Deny) == 0 {
		return tools
	}
	deny := map[string]bool{}
	for _, n := range def.Deny {
		if n = strings.TrimSpace(n); n != "" {
			deny[n] = true
		}
	}
	out := tools[:0:0]
	for _, td := range tools {
		if deny[td.Tool.Name] {
			if withheld != nil {
				withheld[td.Tool.Name] = "is denied by this machine"
			}
			continue
		}
		out = append(out, td)
	}
	return out
}

// toolStepFindings reports each step that calls one tool the run will not
// have, before the run, rather than at the step.
//
// An unattended machine is checked against the pool a run would build, so the
// finding can say WHY a tool the owner has is out of reach. A conversational
// machine runs its tool steps with the hosting agent's catalog, which differs
// per agent, so it is checked only for names that exist nowhere at all.
func toolStepFindings(udb Database, user string, def MachineDef) []string {
	var steps []MachinePhase
	for _, p := range def.Phases {
		if strings.TrimSpace(p.Tool) != "" {
			steps = append(steps, p)
		}
	}
	if len(steps) == 0 {
		return nil
	}
	var out []string
	if def.Unattended {
		pool := machineRunCatalog(udb, user, "", def)
		known := pool.names()
		for _, p := range steps {
			tool := strings.TrimSpace(p.Tool)
			if known[tool] {
				continue
			}
			why, has := pool.Withheld[tool]
			if !has {
				why = "is not a tool you have"
			}
			out = append(out, "step "+p.Name+" calls "+strconv.Quote(tool)+", which "+why+", so a run will fail at this step")
		}
		return out
	}
	exists := map[string]bool{}
	for _, ct := range RegisteredChatTools() {
		exists[ct.Name()] = true
	}
	for _, p := range LoadPersistentTempTools(AuthDB(), user) {
		exists[p.Tool.Name] = true
	}
	for _, lent := range AdoptedToolsFor(AuthDB(), user) {
		exists[lent.Tool.Name] = true
	}
	for _, td := range Secure().BuildTools(&ToolSession{Username: user}) {
		exists[td.Tool.Name] = true
	}
	for _, p := range steps {
		if tool := strings.TrimSpace(p.Tool); !exists[tool] {
			out = append(out, "step "+p.Name+" calls "+strconv.Quote(tool)+", which is not a tool you have")
		}
	}
	return out
}

// machineRunProblems is what stops an unattended run before it starts: the
// definition's own problems, then each tool step that names a tool the run
// will not have. The Run panel, a schedule, a pipeline stage and a dispatch
// all refuse on this one list, so none of them can start a run another would
// have refused, and a step that cannot run is found before the steps ahead of
// it have spent anything.
func machineRunProblems(udb Database, user string, def MachineDef) []string {
	return append(def.Problems(), toolStepFindings(udb, user, def)...)
}
