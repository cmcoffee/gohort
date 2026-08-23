// Sibling dispatch fan-out — whether the agents(action="run") calls a model
// batches into ONE response run one after another, or concurrently.
//
// The agent loop runs a round's tool calls in parallel; a tool opts out by
// declaring a batch lane, and calls sharing a lane run sequentially in
// submission order. Dispatch declared ONE lane for every call, so a batch of
// them was a strict sequence. That was the right default and is still the
// shipped one, but it is stronger than the actual constraint: what cannot
// overlap is two dispatches to the SAME target, not two dispatches.
//
// Same target is unsafe for a reason that has nothing to do with counters.
// A dispatch runs under a sub-session id derived from (parent session, target)
// — deterministic, because that is what re-threads the ephemeral continuity a
// follow-up dispatch reads. Two calls to one target therefore share it, and
// they:
//
//   - both load the same prior history, both append their exchange, both save.
//     The second write wins and the first exchange is gone from the thread.
//   - both hold `defer DeleteSessionTempTools(udb, subSessID)` and
//     `defer clearAuthoringInProgress(udb, subSessID)`. Whichever returns
//     first tears down session state the other is still running against.
//
// Neither is a race a mutex fixes; the id is genuinely shared. So the lane key
// is the target's identity: dispatches to different agents fan out, dispatches
// to one agent stay a sequence and keep their thread intact.
//
// Bounded, and OFF by default. Off because the win is backend-dependent — sub
// -agent runs go out on the worker route, and both the llama.cpp and Ollama
// schedulers serialize at MaxParallel=1 by default, so on a single-slot local
// backend this only moves the queue from the tool executor to the LLM
// serializer. Turn it up where the backend actually has slots (llama.cpp
// started with --parallel N, or a cloud provider).

package orchestrate

import (
	"hash/fnv"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	RegisterTunable(TunableSpec{Key: "tune_dispatch_fanout_lanes", Category: "Limits",
		Label: "Parallel sibling dispatch lanes",
		Help: "How many agents(run) calls from ONE model response may execute at once. 1 (the default) runs them strictly one after another. " +
			"Higher fans out across DISTINCT targets only — two dispatches to the same agent always stay sequential, because they share a sub-session id and would overwrite each other's continuity thread and tear down each other's session temp tools. " +
			"Raise this only when the worker LLM can actually serve concurrent requests (llama.cpp started with --parallel N and its max-parallel knob raised, Ollama likewise, or a cloud provider); against a single-slot backend the calls just queue at the LLM instead of at the tool executor.",
		Kind: KindInt, Default: 1, Min: 1, Max: 12})
}

// dispatchFanoutLanes is how many dispatches may run concurrently. 1 disables
// the fan-out.
func dispatchFanoutLanes() int { return TuneInt("tune_dispatch_fanout_lanes") }

// agentsBatchLane is the agents tool's core.AgentToolDef.BatchLane: it decides
// which batched calls this round are a sequence and which may overlap.
//
// Returning "" puts a call in the shared serial lane — the pre-fan-out
// behavior, and the answer for every case this cannot resolve to a definite
// target. Wrong-but-serial costs latency; wrong-but-parallel corrupts a
// session, so every uncertain branch resolves toward "".
//
// Only action=run is ever laned apart. list/get are cheap enough that ordering
// them costs nothing, and run_tool executes another agent's tool directly,
// which is not worth reasoning about concurrently for a Builder-only verify
// path.
func (t *chatTurn) agentsBatchLane(args map[string]any) string {
	lanes := dispatchFanoutLanes()
	if lanes <= 1 {
		return ""
	}
	if strings.TrimSpace(stringArg(args, "action")) != "run" {
		return ""
	}
	// The identity is resolved, not taken from the argument as written: an
	// agent can be named by name OR by id, and the two spellings of one target
	// must not land in different lanes — that is precisely the collision the
	// lane exists to prevent.
	var key string
	switch {
	case strings.TrimSpace(stringArg(args, "pipeline")) != "":
		def, err := t.dispatchablePipeline(stringArg(args, "pipeline"))
		if err != nil {
			return ""
		}
		key = dispatchPipelineCapKey(def.ID)
	case strings.TrimSpace(stringArg(args, "machine")) != "":
		def, err := t.dispatchableMachine(stringArg(args, "machine"))
		if err != nil {
			return ""
		}
		key = dispatchMachineCapKey(def.ID)
	default:
		fleetDB, fleetUser := t.fleetView()
		target, ok := findAgentByNameOrID(fleetDB, fleetUser, stringArg(args, "agent"))
		if !ok {
			return ""
		}
		key = "agent:" + target.ID
	}
	// Hashed into a fixed number of lanes rather than one lane per target, so
	// the operator's number is a real ceiling on concurrency: a 20-target batch
	// fans out to `lanes` goroutines, not 20. A collision only ever serializes
	// two targets that could have overlapped, which is the safe direction.
	h := fnv.New32a()
	h.Write([]byte(key))
	return "slot" + strconv.Itoa(int(h.Sum32()%uint32(lanes)))
}
