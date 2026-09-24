package core

import (
	"context"
	"strings"
)

// Reach values for MachinePhase.Reach — the coarse tool scope, and the
// only part of a phase's tool setting that survives being carried to
// another agent or another deployment. See the field's own comment.
const (
	ReachUnset = ""     // not chosen: resolves by the step's kind (see PhaseReach)
	ReachAll   = "all"  // inherit the agent's whole catalog
	ReachRead  = "read" // only what reads: nothing that writes, runs, or reaches the network
	ReachNone  = "none" // nothing at all; the step decides or reshapes and hands on
)

// ReachAllowsCaps is the capability set a reach permits, or nil for "no
// restriction". One definition, so the filter and anything that explains
// the filter cannot disagree about what "read-only" means.
func ReachAllowsCaps(reach string) []Capability {
	if strings.TrimSpace(reach) == ReachRead {
		return []Capability{CapRead}
	}
	return nil
}

// validReach reports whether a stored reach names one of the three
// settings, or is unset. Shared by the two authoring surfaces so neither
// can accept a value the other refuses.
func validReach(reach string) bool {
	switch strings.ToLower(strings.TrimSpace(reach)) {
	case ReachUnset, ReachAll, ReachRead, ReachNone:
		return true
	}
	return false
}

// PhaseReach is the phase's reach, reading the legacy marker as the
// setting it always meant. A stored ["__none__"] predates the Reach
// field and says exactly what ReachNone says.
//
// An unset reach resolves by what the step IS. A transient step that
// names no tools, denies none, and hands its work to nobody else gets
// NOTHING: it is a bounded transform in front of somebody's reply, and a
// catalog turns its one request into a tool loop that goes looking before
// it answers. Everything else inherits the whole catalog, as before: a
// resident step is the conversation, a step naming tools or denying some
// wants a catalog to narrow, and a delegate, pipeline, or child run
// passes the catalog on to work that does use it.
func PhaseReach(ph MachinePhase) string {
	if r := strings.TrimSpace(ph.Reach); r != "" {
		return r
	}
	for _, n := range ph.Tools {
		if strings.TrimSpace(n) == NoToolsMarker {
			return ReachNone
		}
	}
	if ph.onlyReasons() {
		return ReachNone
	}
	return ReachAll
}

// onlyReasons reports a step whose unset reach resolves to nothing: see
// PhaseReach.
func (p MachinePhase) onlyReasons() bool {
	return !p.Resident && len(p.Tools) == 0 && len(p.Deny) == 0 && !p.hasRunner()
}

// hasRunner reports a step whose work is done by something other than
// the model call itself: another agent, a pipeline, a child machine, or
// one tool.
func (p MachinePhase) hasRunner() bool {
	return strings.TrimSpace(p.Agent) != "" || strings.TrimSpace(p.Pipeline) != "" ||
		strings.TrimSpace(p.Machine) != "" || strings.TrimSpace(p.Tool) != ""
}

// PhaseTools narrows a catalog to what a phase may reach: the reach
// first (a capability class, which travels), then the phase's own names
// on top of what is left (exact strings, which do not).
//
// Empty Tools inherits whatever the reach allowed — matching
// resolveStageTools, so an author who learned one surface has learned
// the other.
func PhaseTools(ph MachinePhase, catalog []AgentToolDef) []AgentToolDef {
	switch PhaseReach(ph) {
	case ReachNone:
		return nil
	case ReachRead:
		catalog = FilterToolsByCaps(catalog, ReachAllowsCaps(ReachRead))
	}
	// Deny goes LAST, after the reach and after the name list, so it is the
	// final word however permissive the stages above were.
	return keepPagingTools(applyPhaseDeny(ph, resolveStageTools(ph.Tools, catalog)), catalog)
}

// PagingToolNames finish a result the caller ALREADY has: read_output reads
// the rest of a reply that was too big to return whole, release_output lets
// go of one the caller is done with. Neither reaches anything — the data
// arrived when the tool that produced it ran.
//
// So they are not reach, and a control that narrows reach has nothing to say
// about them. Every capped reply ends in a trailer naming read_output, and a
// step handed that trailer without the tool is shown the way to the rest of
// its own result and finds no such tool, which is the shape that produces a
// second guess instead of a second call.
var PagingToolNames = map[string]bool{
	"read_output":         true,
	ReleaseOutputToolName: true,
}

// keepPagingTools puts back any paging tool the narrowing took out, in
// CATALOG order, so the payload the model sees stays byte-stable across
// turns and the prompt cache stays warm. A catalog that never held them is
// unchanged: this restores, it never invents.
func keepPagingTools(narrowed, catalog []AgentToolDef) []AgentToolDef {
	missing := false
	have := make(map[string]bool, len(narrowed))
	for _, td := range narrowed {
		have[td.Tool.Name] = true
	}
	for _, td := range catalog {
		if PagingToolNames[td.Tool.Name] && !have[td.Tool.Name] {
			missing = true
			break
		}
	}
	if !missing {
		return narrowed
	}
	out := make([]AgentToolDef, 0, len(narrowed)+len(PagingToolNames))
	for _, td := range catalog {
		if have[td.Tool.Name] || PagingToolNames[td.Tool.Name] {
			out = append(out, td)
		}
	}
	return out
}

// phaseDenied reports whether a phase's Deny list names this tool.
//
// Matched on the trimmed name exactly, the way Tools is: a catalog name is
// already normalized by the time it gets here (sanitizeToolName lowercases
// remote names at registration), so a second normalization would only invent
// disagreements between the allow list and the deny list about what a name is.
//
// Mirrored by the same check inside orchestrate's narrowCatalog, which has to
// apply the subtraction again against tools that bypass the ordinary narrowing
// (an agent's attachments). Both read this field with this rule; if one grows a
// normalization step, so must the other.
func phaseDenied(ph MachinePhase, name string) bool {
	for _, d := range ph.Deny {
		if strings.TrimSpace(d) == name {
			return true
		}
	}
	return false
}

// applyPhaseDeny subtracts the phase's Deny list. Subtraction only: it never
// adds a tool back, and an empty result is an honest answer rather than a
// signal to give up (see the Deny field comment on why it takes no part in the
// allow-list's total-miss rescue).
func applyPhaseDeny(ph MachinePhase, tools []AgentToolDef) []AgentToolDef {
	if len(ph.Deny) == 0 {
		return tools
	}
	out := make([]AgentToolDef, 0, len(tools))
	for _, td := range tools {
		if !phaseDenied(ph, td.Tool.Name) {
			out = append(out, td)
		}
	}
	return out
}

// PhaseThink applies a phase's reasoning override on top of whatever
// the host already resolved (route default, then per-agent). The phase
// is the most specific setting in that chain, so it goes last; an empty
// Think inherits and returns base untouched.
func PhaseThink(ph MachinePhase, base bool) bool {
	switch strings.ToLower(strings.TrimSpace(ph.Think)) {
	case "on":
		return true
	case "off":
		return false
	}
	return base
}

// PhaseTier maps a phase's Model onto the loop's tier override.
// TierUnset (the zero value) follows the agent's own routing.
func PhaseTier(ph MachinePhase) LLMTier {
	switch strings.ToLower(strings.TrimSpace(ph.Model)) {
	case "worker":
		return WORKER
	case "lead":
		return LEAD
	}
	return TierUnset
}

// --- child runs -------------------------------------------------------

// MaxMachineDepth caps how deep a machine may run machines.
//
// One, which means a run may have children and those children may not.
// The case this exists for is research forking a gap-filling run per gap
// it finds, and research's own guard is exactly this: a child skips gap
// filling so the tree cannot grow a third level.
//
// A NUMBER rather than a bool because the next case that wants two should
// be able to argue for two by changing this, rather than by unpicking a
// design that assumed one.
const MaxMachineDepth = 1

// machineDepthKey carries the current nesting depth on the context.
//
// On the CONTEXT rather than on a struct because the child runs through
// the host's own PhaseRunner, which is the same closure the parent uses.
// The depth has to travel with the call rather than with the caller, or a
// child's phases would look exactly like a parent's and nothing would stop
// the third level.
type machineDepthKey struct{}

// WithMachineDepth returns a context carrying d as the nesting depth.
func WithMachineDepth(ctx context.Context, d int) context.Context {
	return context.WithValue(ctx, machineDepthKey{}, d)
}

// MachineDepth reports how many machines deep this call already is. Zero
// for a top-level run, which is what an absent value means.
func MachineDepth(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	d, _ := ctx.Value(machineDepthKey{}).(int)
	return d
}
