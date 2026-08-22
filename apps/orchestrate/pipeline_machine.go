// The host half of a kind=machine stage: resolving which machine, and
// deciding what a stage is allowed to start.
//
// core executes the recipe and knows nothing about whose machines these
// are (PipelineMachineRunner is the seam). This is the closure that
// answers it, and the three refusals below are the whole reason the seam
// exists rather than the interpreter reaching into a store: which machines
// a run may reach, and how deep runs may nest, are the host's policy.

package orchestrate

import (
	"context"
	"strconv"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// pipelineMachineRunner returns the hook a kind=machine stage runs through,
// bound to one owner.
//
// Depth is carried on the context exactly as it is for a machine's own
// child phase (MachineDepth), and for the same reason: a machine stage
// inside a pipeline that is itself running as a machine's phase is two
// levels deep, and nothing but the context can tell it so.
func (T *OrchestrateApp) pipelineMachineRunner(owner string) PipelineMachineRunner {
	return func(ctx context.Context, ref, input string) (string, map[string]any, error) {
		if d := MachineDepth(ctx); d >= MaxMachineDepth {
			return "", nil, Error("already " + strconv.Itoa(d) + " machine(s) deep and the limit is " +
				strconv.Itoa(MaxMachineDepth) + "; flatten the work into one machine rather than nesting further")
		}
		udb := UserDB(T.DB, owner)
		def, ok := findMachineByNameOrID(udb, owner, ref)
		if !ok {
			return "", nil, Error("no machine named " + strconv.Quote(ref) + " — it was deleted, renamed, or belongs to somebody else")
		}
		if !def.Unattended {
			return "", nil, Error("machine " + strconv.Quote(def.Name) +
				" converses rather than runs: it has a step that waits for a person, and a stage has nobody waiting in it")
		}
		if probs := def.Problems(); len(probs) > 0 {
			return "", nil, Error("machine " + strconv.Quote(def.Name) + " will not run yet — " + probs[0] +
				" (" + strconv.Itoa(len(probs)) + " outstanding)")
		}

		sess := &ToolSession{Username: owner, DB: AuthDB()}
		catalog, err := GetAgentToolsWithSession(sess, availableWorkerToolNames()...)
		if err != nil {
			Log("[orchestrate.pipelines] machine stage %q: tool catalog partly unresolved for %q: %v", def.Name, owner, err)
		}
		catalog = WrapToolsWithRunCache(NewRunToolCache(), catalog)
		cur := &MachineCursor{}
		// A stage has no turn to hang a diagnostic on, so the child's
		// breadcrumbs go to the log rather than being dropped: they are how
		// somebody works out why a run stopped where it did.
		note := func(kind, detail string) {
			Log("[orchestrate.pipelines] machine %q: %s: %s", def.Name, kind, detail)
		}
		// The full host, so a step of this machine that delegates or runs its
		// own pipeline does that here too. The depth already on the context is
		// what stops the nesting going round forever.
		runner := T.unattendedHost(unattendedRun{
			User:   owner,
			ID:     "machine-stage:" + def.ID + ":" + strconv.FormatInt(time.Now().UnixNano(), 36),
			Tools:  catalog,
			Cursor: cur,
			Note:   note,
		}).phaseRunner()
		final, out, err := T.RunUnattended(WithMachineDepth(ctx, MachineDepth(ctx)+1), def, cur, MachineTurn{
			Input: input,
			User:  owner,
			Now:   time.Now().In(UserLocation(owner)).Format("Mon, January 2, 2006 at 3:04 PM MST"),
		}, runner, note)
		if err != nil {
			return "", nil, err
		}
		return out, cur.State[final.Name].Fields, nil
	}
}
