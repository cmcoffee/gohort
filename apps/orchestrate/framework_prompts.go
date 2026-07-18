package orchestrate

import "strings"

// Framework prompt blocks — capability-gated orchestration guidance the
// framework injects into EVERY orchestrator's system prompt, so the behavior
// lives ONCE instead of being copy-authored into each seed's persona. This is
// the shared library the Chat seed's `##` sections are being lifted into (see
// project_fleet_framework_prompts): today most of the Chat persona is not
// chat-specific voice at all, it's framework/fleet behavior that Research,
// Knowledge, and every user-created agent should inherit when they have the
// matching capability — but can't, because it's trapped in one seed's string.
// Cloning made this concrete: a clone of Chat froze a snapshot of these blocks
// (never refreshed), while clones of other seeds and fresh agents got NONE.
//
// PROTOTYPE STAGE: only the plan_set (inline-vs-decompose) block is lifted, to
// prove the injection seam is behavior-preserving before moving the rest. The
// block is gated on the surface actually offering plan_set (hasPlanSet) — the
// same signal preMortemPlanningBlock already gates on — so a dispatch surface,
// which has no plan_set, never sees it. Add further blocks here (clarifying
// questions, document export, Builder routing, channel/fleet) with their own
// capability gates as the lift proceeds.

// planSetSectionHeading marks the plan_set block. Also used as the dedup key so
// a pre-lift clone that still carries the section in its frozen persona doesn't
// get a second, injected copy (see frameworkPromptBlocks).
const planSetSectionHeading = "## Inline tools vs plan_set"

// frameworkPlanSetBlock is the detailed "when to go inline vs decompose"
// guidance, lifted verbatim from the Chat seed. The only wording change from
// the seed original is dropping "(see the section below)" after Builder: the
// block now renders in the capability-block region rather than mid-persona, so
// a relative "below" pointer would be wrong.
const frameworkPlanSetBlock = planSetSectionHeading + `

Two execution surfaces; pick by the shape of the work.

**Inline** — call tools across multiple rounds, accumulating context as you go. Right for conversational turns, single-thread research where one result genuinely informs the next call, "list X then update Y", "search for X then fetch the result".

**plan_set** — decompose into discrete steps, each executed by a fresh-context worker. Right for 2+ INDEPENDENT investigations to compare ("research three vendors and pick one"). Making an AGENT or PIPELINE isn't your job; point the user at Builder. Making a TOOL is your job: author it yourself with tool_def (don't decompose tool-making into plan_set).`

// builderRoutingMarker keys the Builder-routing block for dedup (the block
// leads with a bold sentence, not a `##` heading).
const builderRoutingMarker = "**An APP goes to Builder"

// frameworkBuilderRoutingBlock — authoring an APP, AGENT, PIPELINE, or SKILL is
// Builder's job, not the calling orchestrator's. Lifted verbatim from the Chat
// seed (the block the division-of-labor rule the user flagged as fleet-level,
// not chat-specific). Gated on Fleet (only a delegating agent can hand work to
// Builder) AND not-Builder (Builder IS the authoring agent — telling it to
// route to itself is nonsense). Making a TOOL stays self-serve; that guidance
// is a separate block (still in the seed until its own lift).
const frameworkBuilderRoutingBlock = `**An APP goes to Builder — a gohort "app" is a dashboard SURFACE, never a downloadable file.** When the user asks to "build an app" / "a page/UI/dashboard where I can…" / "track / log / visualize / chart X", that's a gohort app: a surface under Custom Apps at /custom/<slug>/, built by Builder's app_def tool. Hand the WHOLE thing to Builder via agents(action="run", agent="builder", ...) — author none of it yourself, and do NOT peel off "the graph part" into a tool_def or emit a standalone HTML file and call that "your app" (a downloadable HTML file is a browser artifact, not a gohort app — this has burned us repeatedly). It just shows up under Custom Apps, so skip file-format questions ("image or HTML?"); the only things to pin before dispatch are the DATA (records/fields, source) and whether it needs a bound agent. Produce a standalone file only when the user EXPLICITLY asks for one to use outside gohort.

**Agents, pipelines, and skills are built THROUGH Builder, in this thread: you do the quick intake, then Builder does the build.** When the user wants an AGENT, PIPELINE, or SKILL made:

1. FIRST pin any design decision a build needs that the user has NOT already given (typically: the data or source, the schedule or trigger, and the output shape such as format and length). If one is genuinely missing AND would change what gets built, ask it yourself with ask_user / ask_user_form before building. Do NOT make Builder guess at a decision you could just ask about; equally, do NOT re-ask anything the user already told you.

2. THEN dispatch Builder as a sub-agent: agents(action="run", agent="builder", message="<a full brief: what to build, who it is for, the answers from step 1, plus the relevant detail from this conversation>"). Builder inherits your read-only tools (read_chat and the like) so it can inspect what you can see while it drafts. Whatever it creates is saved HELD FOR APPROVAL and becomes a sub-agent of yours the moment the user approves it in their Authorizations pane. Do NOT author agents / pipelines / skills yourself or via plan_set. After dispatching, tell the user what you had built and that it is waiting for their approval:

  "I had Builder draft that for you. Approve it in your Authorizations pane and it goes live: <one line on what it does>."

For a complex or open-ended design ("help me figure out what I even want"), you don't need one perfect dispatch: go BACK AND FORTH with Builder in this thread. Dispatch what you have; if it needs a decision it can't assume, it says so — relay that, get the answer, dispatch again in the SAME thread (it remembers the prior exchange). Keep iterating until the design is captured, then report what was built and that it's held for approval. (Tools you still make yourself.)`

// frameworkPromptBlocks returns the capability-gated framework orchestration
// sections for this agent + surface, joined for splicing into the system
// prompt. `existing` is the prompt assembled so far (persona + prior blocks);
// a block whose heading is ALREADY present is skipped, so a pre-lift clone that
// froze the section into its own persona keeps that one copy instead of getting
// a duplicate. Pure (no DB / no I/O) so the gating is unit-testable independent
// of the assembly plumbing; appendAgentCapabilityBlocks calls it.
func frameworkPromptBlocks(existing string, agent AgentRecord, hasPlanSet bool) string {
	var b strings.Builder
	add := func(gated bool, heading, block string) {
		if !gated {
			return
		}
		// Dedup guard — don't inject a block the persona already carries
		// (e.g. an agent cloned from Chat before this block was lifted).
		if strings.Contains(existing, heading) || strings.Contains(b.String(), heading) {
			return
		}
		b.WriteString("\n\n")
		b.WriteString(block)
	}
	// plan_set guidance — only where the surface actually offers plan_set.
	add(hasPlanSet, planSetSectionHeading, frameworkPlanSetBlock)
	// Builder routing — only a delegating (Fleet) agent that is NOT Builder
	// itself. Builder is the authoring agent; routing it to itself is nonsense.
	add(agent.Fleet && !isBuilderAgent(agent.ID), builderRoutingMarker, frameworkBuilderRoutingBlock)
	return b.String()
}
