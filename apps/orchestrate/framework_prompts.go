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

// clarifyingSectionHeading marks (and dedup-keys) the clarifying-questions block.
const clarifyingSectionHeading = "## Asking the user clarifying questions"

// frameworkClarifyingBlock — when to pause and ask vs. when to just search, and
// how to shape the ask (ask_user vs ask_user_form). Lifted verbatim from the
// Chat seed. Gated on the interactive-web surface (hasPlanSet): ask_user /
// ask_user_form only exist there — a dispatch/worker surface can't prompt the
// user, so the guidance would be noise.
const frameworkClarifyingBlock = clarifyingSectionHeading + `

**When to ask** — Pause and ask whenever GUESSING is the alternative (not when SEARCHING is). Concrete triggers:

- A tool returned 2+ plausible matches and you'd be picking arbitrarily ("there are 3 agents named 'helper' — which one?" → ask_user with the 3 names as options).
- The user must choose between meaningfully different approaches that no tool can resolve ("PDF or HTML?", "shallow or deep?", "version 2 or 3?").
- They must supply personal info you can't look up (which appliance, which file, their preference).
- The request has an unresolved scope you can't infer from history ("clean up the database" — which one?).

DON'T ask when a tool call would just answer the question. "What's the price of X?" → web_search, not ask_user.

**How to ask** —

- One question, enumerable choices → ask_user with options[].
- Several questions, each with their own choices → ask_user_form with steps[]. NEVER stuff multiple questions into one ask_user as a numbered list; that forces the user to type "1. … 2. … 3. …" instead of clicking through.
- Several specific VALUES the user must TYPE (an API base URL, a key, a count, an endpoint) → ask_user_form with steps[] where each step sets type ("text"/"number"/"textarea"/"select"/"password"). Any typed step renders the whole thing as ONE form (all fields at once, single Submit) instead of a step-through — the right shape for "fill these fields in." Use type:"password" for secrets/keys, type:"select" with options for a dropdown.
- Open-ended single question with no clear options → ask_user without options.`

// toolsSelfServeMarker / exportMarker dedup-key the tool-gated blocks (each
// leads with a bold sentence, not a `##` heading).
const toolsSelfServeMarker = "**Tools are self-serve."
const exportMarker = "**Producing a document FILE"

// frameworkToolsSelfServeBlock — a TOOL is the calling agent's own job (author
// it with tool_def), not Builder's. Lifted verbatim from the Chat seed. Gated
// on the agent actually having tool_def (see agentAllowsFrameworkTool) so a
// restricted agent that lacks it is never told to reach for it.
const frameworkToolsSelfServeBlock = `**Tools are self-serve.** When the user wants a new TOOL, author it yourself with tool_def — you don't punt this to Builder. The loop: for an API endpoint, tool_def(mode="api", credential=...) wraps it directly; for local processing, write and run a script in the workspace (workspace write + run) to prove it works, then tool_def(mode="shell", script_body=...) to wrap it. The tool is callable immediately in this session; cross-session persistence is auto-queued for admin approval in the background (don't ask permission, just author it). Before you tell the user whether you HAVE some tool ("do you have a vapi tool?"), or build one that might already exist, call tool_def(action="list") and look: it is the only surface that shows every custom tool in scope (session drafts, your approved persistent tools, AND ones still pending approval). Answer from that list, never from memory, and never claim you "previously set one up" without checking it.`

// frameworkExportBlock — producing a downloadable document/spreadsheet/deck is
// the built-in export tool's job, never hand-built file bytes. Lifted verbatim
// from the Chat seed. Gated on the agent having the export tool.
const frameworkExportBlock = `**Producing a document FILE (pdf / Word .docx / Excel .xlsx / PowerPoint .pptx) is the built-in export tool's job — never hand-build the file format.** When the user wants a downloadable document, spreadsheet, or deck ("export a docx", "save this as a PDF", "make me an xlsx", "the docx export is broken"), use the export tool: export(action="formats") shows each format's expected data shape, export(action="create", format=..., data=...) generates the file and attaches it to your reply. It renders through real libraries (python-docx, openpyxl, python-pptx, the markdown-to-PDF renderer) and provisions their dependencies for you, so the output opens cleanly in Word / Excel / Preview. Do NOT author a bespoke tool_def that hand-assembles OOXML / PDF / zip bytes, and do NOT try to patch an old hand-rolled document tool — a hand-built office file is exactly what macOS Word rejects with "Word experienced an error trying to open the file" (this has burned us). If export genuinely lacks a format the user needs (CSV, a bespoke layout), register it ONCE via export(action="define") with a generator script — that is the extension path, not a standalone tool.`

// agentAllowsFrameworkTool reports whether a framework tool (tool_def, export)
// is in the agent's scope, using the same AllowedTools semantics the tool
// filter uses: the no-tools sentinel means none; an empty list means the full
// default pool (which includes the authoring/export tools); a non-empty list is
// an allowlist, so the tool must be named in it. Errs toward NOT showing a
// block when uncertain — a false negative is harmless; the anti-pattern to
// avoid is telling an agent to use a tool it doesn't have.
func agentAllowsFrameworkTool(agent AgentRecord, name string) bool {
	if isNoToolsSentinel(agent.AllowedTools) {
		return false
	}
	if len(agent.AllowedTools) == 0 {
		return true
	}
	for _, t := range agent.AllowedTools {
		if strings.TrimSpace(t) == name {
			return true
		}
	}
	return false
}

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
	// Clarifying-questions guidance — ask_user rides the same interactive-web
	// signal as plan_set; a dispatch/worker surface can't prompt the user.
	add(hasPlanSet, clarifyingSectionHeading, frameworkClarifyingBlock)
	// Tools-self-serve and document-export — gated on the agent actually having
	// the tool (tool_def / export), not on the surface: unlike plan_set these
	// tools can exist off the interactive surface too, so capability is the gate.
	add(agentAllowsFrameworkTool(agent, "tool_def"), toolsSelfServeMarker, frameworkToolsSelfServeBlock)
	add(agentAllowsFrameworkTool(agent, "export"), exportMarker, frameworkExportBlock)
	// Builder routing — only a delegating (Fleet) agent that is NOT Builder
	// itself. Builder is the authoring agent; routing it to itself is nonsense.
	add(agent.Fleet && !isBuilderAgent(agent.ID), builderRoutingMarker, frameworkBuilderRoutingBlock)
	return b.String()
}
