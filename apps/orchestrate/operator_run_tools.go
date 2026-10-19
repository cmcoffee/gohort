// The Operator's tools for work it hands off or waits on: delegating to an
// agent, reading the run ledger, and waiting on a deferred result. Listed in
// operatorToolDefsUnfolded (operator_tools.go).

package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func delegateToolDef(sess *ToolSession, owner, controllerAgentID string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "delegate",
			Description: "Delegate a task to an existing agent: this is how you DO work (you are a controller, you delegate rather than acting directly). It runs now and you get the result, unless the user set that agent to Ask: then it's queued for their approval in the Authorizations pane and you tell them it's waiting.",
			Parameters: map[string]ToolParam{
				"agent": {Type: "string", Description: "Name or id of the existing agent to delegate to."},
				"brief": {Type: "string", Description: "What the agent should do."},
				"attach": {Type: "array", Items: &ToolParam{Type: "string"},
					Description: "Pictures, audio or video to hand the agent, by your names for them: media#N (something sent to you), an image you kept, or a file in your working folder. They arrive as its own media#1, media#2 and so on. A text or other file needs no attach: name it in the brief, the agent shares your working folder. What the agent attaches comes back to you with its result."},
			},
			Required: []string{"agent", "brief"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			agent := strings.TrimSpace(oArgStr(args, "agent"))
			brief := strings.TrimSpace(oArgStr(args, "brief"))
			if err := missingArgs("the delegate call",
				reqArg{"agent", agent, "name or id of the existing agent to delegate to"},
				reqArg{"brief", brief, "what that agent should do, in your own words"},
			); err != nil {
				return "", err
			}
			// The same checks agents(run) makes, when the turn has them
			// (chatTurn.gateDelegate): dispatch policy, what the target
			// accepts, sub-agent privacy, cycles. Without a turn there is
			// nothing to check against, so only an explicit Allow runs it.
			gate := delegateGateFrom(ctx)
			var target AgentRecord
			if gate != nil {
				rec, err := gate(agent, brief)
				if err != nil {
					return "Not delegated: " + strings.TrimPrefix(err.Error(), "agents(run): "), nil
				}
				target = rec
			} else if rec, ok := findAgentByNameOrID(UserDB(orchestrateBaseDB, owner), owner, agent); ok {
				target = rec
			}
			policy, decided := delegationDecisionFor(owner, controllerAgentID, target, agent)
			if decided && policy == PolicyBlock {
				return fmt.Sprintf("Delegation to %q is blocked in the user's permission settings: not run.", agent), nil
			}
			// Run by id, which names one agent; say it by name.
			runKey := agent
			if target.ID != "" {
				runKey, agent = target.ID, chFirst(target.Name, agent)
			}
			if (decided && policy == PolicyAllow) || (gate != nil && !decided) {
				// Root the delegation in the PARENT TURN'S context (sess.Context())
				// so a Stop / cancel of the chat turn also cancels this outgoing
				// agent call — previously it ran on context.Background() and kept
				// going after the user stopped. ContextWithNetworkConnector also
				// carries the parent's network connector so a Private parent stays
				// private in the sub-run (applyForcePrivateToDispatch enforces it
				// from the blocked ctx). Both nil-safe.
				// controllerAgentID is the agent doing the delegating — passed
				// so the delegate can address the channels its delegator
				// reaches instead of handing text back up to be relayed.
				dctx := carryNonOwnerRequester(ctx, sess.ContextWithNetworkConnector(sess.Context()))
				// And who handed THIS run over, so the delegation counts as one
				// more hop of the same chain (dispatch_lineage.go).
				dctx = carryDispatchLineage(ctx, dctx)
				// And this call's card, so the delegated run's progress
				// shows on it (dispatch_progress.go).
				dctx = withToolCard(dctx, toolCardFrom(ctx))
				// And a place for what it attaches, handed on to this turn, and
				// what this agent hands it (agent_media.go).
				returned := &ToolSession{}
				dctx = withMediaReturn(dctx, returned)
				handed, handedNote, herr := handOut(sess, handerName(owner, controllerAgentID), stringSliceFromArgs(args, "attach"))
				if herr != nil {
					return "Not delegated: " + herr.Error(), nil
				}
				dctx = withHandedMedia(dctx, handed)
				brief += handedNote
				rec := RunDelegation(dctx, RootDB, owner, runKey, brief, controllerAgentID)
				back := handBack(agent, returned, sess)
				if rec.Status == RunFailed {
					return fmt.Sprintf("Delegated to %q but it failed: %s%s", agent, rec.Err, back), nil
				}
				// The whole result, as agents(run) hands it back. Summary is the
				// Activity feed's line, cut at 280 characters, and the
				// delegator worked from that cut. Under the agent-result banner,
				// as agents(run)'s: what a delegate returns can quote text it read.
				out := strings.TrimSpace(rec.Raw)
				if out == "" {
					out = strings.TrimSpace(rec.Summary)
				}
				fenced, _ := fenceAgentResult(agent)(out, nil)
				return fmt.Sprintf("Delegated to %q.%s Result:\n%s%s", agent, privateDelegationNote(dctx, true), fenced, back), nil
			}
			// FromAgent rides the queued record so an APPROVED delegation runs
			// with the same channel reach a pre-authorized one gets — the user
			// clicking Approve isn't making a scope decision. The asking
			// conversation rides it too, so the approved run reports back
			// here, and so does this turn's privacy, so it runs under it.
			a := Authorization{Owner: owner, Agent: agent, Brief: brief, FromAgent: controllerAgentID}
			qctx := stampRequestOrigin(&a, sess)
			a = SaveAuthorization(RootDB, a)
			if sess != nil && sess.PendingApprovalPrompt != nil {
				sess.PendingApprovalPrompt(a)
			}
			note := requestOriginNote(a)
			// A queued request keeps its brief and nothing else.
			if len(stringSliceFromArgs(args, "attach")) > 0 {
				note += " What you attached is not kept with a queued request: hand it over again when it runs."
			}
			return fmt.Sprintf("Queued a delegation to %q for the user's approval: it's in the Authorizations pane (id %s) and runs once approved. %s%s", agent, a.ID, note, privateDelegationNote(qctx, false)), nil
		},
	}
}

func listRunsToolDef(owner string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "list_runs",
			Description: "List recent background/fleet runs (delegated tasks, standing-agent executions, recurring-task fires), status-level. These are NOT your own chat turns. Each line shows the run id for use with inspect_run.",
			Parameters: map[string]ToolParam{
				"name":  {Type: "string", Description: "Optional: restrict to one name, an agent's display name OR a recurring task's name, whichever you have."},
				"agent": {Type: "string", Description: "Optional: same as name. Accepted so either spelling works."},
				"limit": {Type: "number", Description: "Optional: max rows (default 15, max 50)."},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			limit := oArgInt(args, "limit")
			if limit <= 0 || limit > 50 {
				limit = 15
			}
			name := strings.TrimSpace(oArgStr(args, "name"))
			if name == "" {
				name = strings.TrimSpace(oArgStr(args, "agent"))
			}
			// A name is an agent label or a schedule's name, and the caller
			// asking "did X run?" does not know which kind of thing X is.
			// Ask both ways and merge: the previous single Agent-match
			// answered a recurring task's name with "No runs recorded yet.",
			// indistinguishable from a task that never fired.
			runs := ListRuns(RootDB, owner, RunFilter{Agent: name, Limit: limit})
			if name != "" {
				seen := make(map[string]bool, len(runs))
				for _, rr := range runs {
					seen[rr.ID] = true
				}
				for _, rr := range ListRuns(RootDB, owner, RunFilter{Task: name, Limit: limit}) {
					if !seen[rr.ID] {
						runs = append(runs, rr)
					}
				}
				sort.Slice(runs, func(i, j int) bool { return runs[i].Started.After(runs[j].Started) })
				if len(runs) > limit {
					runs = runs[:limit]
				}
			}
			if len(runs) == 0 {
				if name == "" {
					return "No runs recorded yet.", nil
				}
				// Say which of the two things happened. "No runs recorded"
				// for an unknown name reads as "it never ran", and that
				// reading has already been believed and reported as fact.
				total := len(ListRuns(RootDB, owner, RunFilter{Limit: 200}))
				if total == 0 {
					return fmt.Sprintf("No runs recorded yet for %q, and no runs recorded at all for this owner.", name), nil
				}
				return fmt.Sprintf("No runs match the name %q. There ARE %d run(s) recorded for this owner, so this is a name that does not appear in the ledger rather than an empty ledger. Call list_runs with no name to see what did run. Note that intentional early exits (agent or session gone) are logged but never recorded, so a schedule can fire without leaving a row.", name, total), nil
			}
			var b strings.Builder
			for _, rr := range runs {
				sum := rr.Summary
				if len(sum) > 120 {
					sum = sum[:120] + "…"
				}
				// The task name when the run came from a named schedule:
				// without it every recurring fire prints under its agent's
				// label and nothing on the line says which task ran.
				label := rr.Agent
				if rr.Task != "" && rr.Task != rr.Agent {
					label = rr.Agent + " - " + rr.Task
				}
				fmt.Fprintf(&b, "- [%s] %s %s (%s): %s\n",
					rr.ID, rr.Started.Local().Format("Jan 2 3:04 PM"), label, rr.Status, sum)
			}
			return strings.TrimSpace(b.String()), nil
		},
	}
}

func inspectRunToolDef(sess *ToolSession, owner string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "inspect_run",
			Description: "Get the full detail and output of a DELEGATED or standing-agent RUN, identified by a run id from list_runs. A \"run\" is a background or fleet execution. This is NOT how you review your own chat replies, and NOT a way to check what you just said or sent in this conversation. Only call it with an id that came from list_runs; never construct one.",
			Parameters:  map[string]ToolParam{"id": {Type: "string", Description: "The run id."}},
			Required:    []string{"id"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			id := strings.TrimSpace(oArgStr(args, "id"))
			// Binding-slip guard. Small models sometimes reason correctly
			// ("call get_joke directly") but emit THIS tool with the wanted
			// tool's NAME shoved into id (observed live: get_run(id="get_joke")
			// five rounds running). Renaming get_run->inspect_run broke the
			// get_* adjacency that drove it, but does not eliminate the slip.
			// If id names a loaded temp tool it is NOT a run id — redirect the
			// model to call that tool directly instead of erroring on a run
			// lookup it will just retry identically until the guard trips.
			if id != "" && sess.HasTempTool(id) {
				return "", fmt.Errorf("%q is not a run id: it is a tool you already have loaded and callable. Call %s directly with its own arguments now. inspect_run is ONLY for opaque run ids from list_runs, never a tool name.", id, id)
			}
			rec, ok := GetRun(RootDB, owner, id)
			if !ok {
				// The id wasn't found — usually fabricated (a run-id-shaped string
				// the model invented, or an attempt to "fetch" a cortex note, which
				// is not a run). Do NOT enumerate real run ids here: a fixated small
				// model treats the list as a menu, calls one, gets a valid-but-
				// irrelevant run back (a SUCCESS, so the loop-guard error counter
				// never trips) and polls it dozens of times making zero progress
				// (observed live). Return a hard, id-less directive instead.
				return "", fmt.Errorf("no run with that id: run ids are opaque and come ONLY from list_runs, never constructed. If you were trying to answer the user, this is the WRONG tool: call the tool that does what they asked. If you genuinely need a run, call list_runs first, then inspect_run with an id it returned.")
			}
			task := ""
			if rec.Task != "" {
				task = "task: " + rec.Task + "\n"
			}
			// What the run's prompt was made of. Printed only when a digest
			// was recorded, so an older row reads exactly as it always did
			// rather than gaining a line of zeroes that looks like a
			// measurement.
			if d := rec.Prompt; d.Window > 0 || d.SystemBytes > 0 {
				line := fmt.Sprintf("prompt: ~%d tokens (system %d + tools %d over %d tool(s) + history %d over %d msg(s))",
					d.Estimated, d.SystemTokens, d.ToolTokens, d.ToolCount, d.HistoryTokens, d.Messages)
				if d.Window > 0 {
					line += fmt.Sprintf(", window %d, headroom %d", d.Window, d.Headroom)
				}
				if d.InputTokens > 0 {
					line += fmt.Sprintf(", provider charged %d", d.InputTokens)
				}
				if d.Tight {
					line += ", TIGHT: this prompt was within a tenth of the window, which fails intermittently rather than cleanly"
				}
				task += line + "\n"
			}
			// The captured prompt, for a run whose agent had capture on.
			// LAST, and only then: it is by far the largest thing here, and
			// everything above it is the summary a reader normally wants.
			// Its absence is the normal case, not a missing measurement.
			captured := ""
			if txt := strings.TrimSpace(rec.Prompt.Text); txt != "" {
				captured = "\n\n=== PROMPT AS SENT (round 1) ===\n" + txt
			}
			return fmt.Sprintf("Run %s\nagent: %s\n%sstatus: %s\ntrigger: %s\nbrief: %s\nsummary: %s\n%soutput:\n%s%s",
				rec.ID, rec.Agent, task, rec.Status, rec.Trigger, rec.Brief, rec.Summary, formatRunSteps(rec.Steps), rec.Raw, captured), nil
		},
	}
}

func awaitResultToolDef(sess *ToolSession, owner, agentID string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "await_result",
			Description: "Wait for a DEFERRED result without blocking or hand-polling. Use this whenever a step's result arrives LATER (a contact's reply, a phone call's outcome, an external job you kicked off), instead of looping on the tool yourself round after round. It runs `tool_name` in the background every interval and WAKES you here exactly once, the moment that tool's output CHANGES from now, then removes itself. END your turn right after calling it; you'll be re-woken with the result and continue the plan from there. This is the right move whenever you set something in motion whose answer comes back on someone else's schedule. Examples: after message_contact(\"Alex\", \"...questions...\"), call await_result(tool_name=\"read_chat\", tool_args={\"chat_id\":\"<Alex's chat>\"}, note=\"Alex's answers, then draft the spec and email it to the owner\") to resume when he replies; after placing a call, await_result on the call-status tool to resume when the call reports back; after starting a backup or a restart on a system in your Sources, await_result on that system's read-only status tool. A tool that can make changes cannot be awaited.",
			Parameters: map[string]ToolParam{
				"tool_name":        {Type: "string", Description: "The tool polled each interval to detect the result: e.g. \"read_chat\" for a reply, a call-status tool for a call's outcome. Its output is hashed; you're woken when it changes. Must be a tool you can already call."},
				"tool_args":        {Type: "object", Description: "Arguments passed to tool_name every check, e.g. {\"chat_id\":\"any;+;chat123\"}. Use the same args you'd pass calling it directly."},
				"note":             {Type: "string", Description: "What you're waiting for AND what to do once it arrives: this is handed back to you on wake to continue the plan. E.g. \"Alex's answers to the spec questions; then write the spec and email it to the owner.\""},
				"from_sender":      {Type: "string", Description: "Optional but STRONGLY recommended when awaiting a reply in a GROUP chat: the name of the person you're waiting on (e.g. \"Alex Rivera\", as shown in read_chat). The wake then fires ONLY when a new message from THAT person arrives: not on every message in the chat (other participants, or your own outbound). Without it, a busy group wakes you on any change and you'll have to re-await. Omit for a one-on-one chat or a non-chat result (a call/job status) where any change IS the result."},
				"interval_seconds": {Type: "number", Description: "How often to check, in seconds (minimum 30; default 60). A human reply can be slow: 60-300 is usually right; don't poll faster than the result could plausibly arrive."},
			},
			Required: []string{"tool_name"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			toolName := strings.TrimSpace(oArgStr(args, "tool_name"))
			if toolName == "" {
				return "", fmt.Errorf("tool_name is required: the tool whose output signals the result (e.g. read_chat for a reply)")
			}
			var toolArgs map[string]any
			if ta, ok := args["tool_args"].(map[string]any); ok {
				toolArgs = ta
			}
			interval := oArgInt(args, "interval_seconds")
			if interval < 30 {
				interval = 60
			}
			note := strings.TrimSpace(oArgStr(args, "note"))
			fromSender := strings.TrimSpace(oArgStr(args, "from_sender"))
			// Transient, collision-proof name — an await is short-lived and
			// removes itself on fire, so it must not clash with a user's real
			// monitor names.
			name := "await_" + slugify(toolName) + "_" + UUIDv4()[:8]
			m := EventMonitor{
				Name:            name,
				Owner:           owner,
				Kind:            EventKindWatch,
				Notify:          EventNotifyChannel,
				OneShot:         true,
				ToolName:        toolName,
				ToolArgs:        toolArgs,
				MatchNew:        fromSender, // fire only on a new line from this sender (group-chat scope)
				WakeAgent:       agentID,
				WakeBrief:       note,
				IntervalSeconds: interval,
				Created:         time.Now(),
			}
			if sess != nil {
				m.WakeSession = sess.ChatSessionID // resume in THIS conversation
			}
			// Seed the change-baseline from a probe NOW, so only a change AFTER
			// this call wakes you — not the current content. Best-effort: if the
			// probe fails, the first successful poll seeds it instead.
			body, perr := InvokeWatchTool(owner, agentID, toolName, toolArgs)
			if errors.Is(perr, errWatchToolMakesChanges) {
				return "", perr
			}
			if perr == nil {
				m.LastHash = HashWatcherBody(body)
			}
			SaveEventMonitor(RootDB, m)
			if err := ScheduleEventMonitor(RootDB, m); err != nil {
				DeleteEventMonitor(RootDB, owner, name)
				return "", fmt.Errorf("couldn't schedule the await: %w", err)
			}
			return fmt.Sprintf("Awaiting a result from %s: checking every %ds; I'll wake here once its output changes, then continue. END this turn now; I'll resume when the result arrives.", toolName, interval), nil
		},
	}
}
