package orchestrate

// Builder's agent recipe: the shape of the agents a build makes, written
// down before any of them is.
//
// The shape is the part of a build that is hard to change afterwards: how
// many agents, which kind each is, what each can reach, and why a part was
// split off. Settings are tuned later by whoever uses the agent. Builder's
// prompt is about agents, tools, apps, pipelines and credentials at once,
// and a library of fixed recipes it could read went unread: over
// four days of logs it never called archetype once. So each build gets its
// own recipe, written by a call of its own, with a brief about nothing else, and it runs because the
// build is an agent rather than because the model remembered to ask.
//
// Forced by the intake, not by a prompt. A session whose intake said Create
// or Change and Agent cannot create or update an agent until agent_recipe has
// run in it (recipeGateRefusal). A session with no intake (another agent sent
// it, or a one-line request) is not gated, and Builder may still call the
// tool when a request turns out to change what an agent can reach.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
)

// BuilderRecipeKey is the prompt-registry key for the recipe writer's brief.
const BuilderRecipeKey = "agent.builder.recipe"

// builderRecipeRouteKey routes the recipe call. Its own stage so an
// administrator sees it on the routing page and can send it to the worker.
const builderRecipeRouteKey = "app.orchestrate.builder_recipe"

func init() {
	RegisterPromptBlock(PromptBlock{
		Key:      BuilderRecipeKey,
		Title:    "Builder: the recipe for an agent build",
		Category: "Builder",
		Gate:     "The agent_recipe call, made before Builder creates or changes an agent.",
		Text:     builderRecipeShipped,
	})
	RegisterRouteStage(RouteStage{
		Key:   builderRecipeRouteKey,
		Label: "Builder: Agent recipe (before anything is created)",
		// One call per agent build or change, carrying the request and, on a
		// change, the agent as it stands. Judgement about structure, so the
		// lead; falls back to the worker when there is none or the session
		// may not reach it.
		Default: "lead",
		Group:   "Builder",
	})
}

const builderRecipeShipped = `You write the RECIPE for the agents a request needs, before anything is built: how many agents, what kind each is, what each can reach, and why any part is split off. Everything past that (tone, round limits, thinking) is tuned later; leave it out.

Two kinds:
- ASSISTANT: people talk with it (the owner, a group chat, a contact). It keeps a standing mind and remembers who it talks to. memory_mode "chatbot".
- SPECIALIST: one job, used directly or handed work by another agent. It remembers lessons, not people. memory_mode "agent". "An agent that does X" is a specialist unless people will talk to it. A specialist whose job is mapping something (a codebase, a system, how a project's parts connect) gets memory_mode "cartographer": it also keeps a map in its graph memory, which is worth attaching to other agents as a Source.

Split a part off into its own sub-agent (owned_by the parent, hidden) only for one of three reasons, and name the reason:
1. REACH that should live on it alone: the parent should not hold that tool at all (sending messages to people, running code, changing a system).
2. A heavy TRANSCRIPT: the part is many tool calls (walking a repository, reading many pages, working through files) and the parent needs only the result.
3. A MEMORY of its own: what it learns (a catalogue, a dataset's layout, which drafts break which rule) should not mix with the parent's.
No reason, no split: the agent gets the tools itself. Looking something up before answering every message is a machine phase on the agent, which keeps the transcript out without a second agent.

Reach, for each agent:
- Attach the subject as a Source where one fits (a folder, a guide, a Servitor system, another agent) rather than loose tools. An agent attached as a Source lets its parent read what it has learned without running it.
- Only tools that look, unless this is the agent that acts.
- What it reaches is the person's choice: name what it NEEDS ("a way to read the sales exports") and ask when the request does not say.
- The lead model costs more. For an agent whose job is judgement (reviewing work, deciding), offer it as a question; never assume it.

Facts that decide a recipe, the ones builds get wrong:
- An agent with channels gets send_message over them. When an agent people talk to in a group must not message anyone else, the messaging goes on a sub-agent and the parent gets a guardrail; its replies in its own conversation are unaffected.
- A sub-agent sends through its parent's channels and is scheduled by its parent (schedule with agent set to the sub-agent).
- A step limited to reading also drops remote reads (an MCP tool, a remote source's search), because reaching the network is not a read. An agent that must look at a remote system is bounded by denying its write tools instead.
- A scheduled or other unwatched run refuses a tool that needs approval and queues the request; once the owner approves it, later runs use it.
- A bulletin board has one poster (a sub-agent posts under its parent's right). Followers see the latest post on every turn with no tool call, at most 600 characters, and a post goes out of date after the board's ttl_hours. A monitor created with bulletin=<board> posts a single value with no model.
- Reporting every time on a clock is a schedule with when "every". Staying silent until something changes or crosses a line is a monitor.
- An agent that must answer ONLY from documents: attach them (attached_collections, or ingest_attachments for uploads), force_private so nothing outside reaches it, disable_inferred and disable_explicit so it grows no memory to compete with the documents, disable_skills so no skill adds instructions of its own, and a rule that it answers only from what it found this turn and says plainly when the documents do not cover a question.
- Cited web research has a tuned built-in, Research (seed-research): when a specialist is that, say to clone it (clone_agent) and change what differs.

On a CHANGE you see the agent as it stands, with its sub-agents. Most changes leave the shape alone (a prompt, a rule, a setting, a tool that fits what it already does): answer exactly "No shape change." and one sentence saying why. Write a new recipe only when the change adds reach or a job that meets one of the three reasons, and say what moves where.

Otherwise answer in this form, and keep it short:
Recipe: <one sentence>
Agents:
- <name> (<assistant or specialist>[, sub-agent of <parent>]): <its job>. Reaches: <what>. Split because: <reason, omitted for the top agent>
Wiring: <how they connect: delegation, a Source, a schedule, a board, a guardrail>, or none
Ask the person: <only what they must decide>, or none`

// recipeState is one Builder session's standing with the recipe: whether its
// latest intake asks for one, and whether one was written since.
type recipeState struct {
	mu       sync.Mutex
	required bool
	sig      string // the intake it was required for; a new intake starts over
	written  bool
	action   string
	request  string
	target   string
	buildOn  string
}

// builderRecipeStates is per owner and session. In memory, like the session's
// change approvals: a restart asks for the recipe again, which costs one call.
var builderRecipeStates sync.Map // owner\x00session -> *recipeState

func recipeStateFor(owner, session string) *recipeState {
	v, _ := builderRecipeStates.LoadOrStore(owner+"\x00"+session, &recipeState{})
	return v.(*recipeState)
}

// latestIntake is the newest intake in the session: its stored values, or
// else what its packed text says ("**<label>:** value"), which every intake
// send carries. from says which, and is "" when the session has none.
func (t *chatTurn) latestIntake() (vals map[string]string, from string) {
	if t == nil || t.session == nil {
		return nil, ""
	}
	for i := len(t.session.Messages) - 1; i >= 0; i-- {
		m := t.session.Messages[i]
		if m.Role != "user" {
			continue
		}
		if len(m.IntakeValues) > 0 {
			out := map[string]string{}
			for k, v := range m.IntakeValues {
				out[k] = strings.TrimSpace(v)
			}
			return out, "values"
		}
		out := map[string]string{}
		for _, f := range t.agent.IntakeForm {
			label := strings.TrimSpace(f.Label)
			if label == "" {
				continue
			}
			marker := "**" + label + ":**"
			j := strings.Index(m.Content, marker)
			if j < 0 {
				continue
			}
			rest := strings.TrimSpace(m.Content[j+len(marker):])
			if k := strings.Index(rest, "\n\n**"); k >= 0 {
				rest = rest[:k]
			}
			out[f.Name] = strings.TrimSpace(rest)
		}
		if len(out) > 0 {
			return out, "text"
		}
	}
	return nil, ""
}

// noteRecipeRequirement records, at the start of a Builder turn, whether the
// session's latest intake is an agent being made or changed. A gate rather
// than a grant, so reading the intake's text when the stored values are
// missing can only ask for a recipe more often, never let a build skip one.
func (t *chatTurn) noteRecipeRequirement() {
	if t == nil || t.session == nil || !isBuilderAgent(t.agent.ID) {
		return
	}
	vals, _ := t.latestIntake()
	action := strings.ToLower(vals["action"])
	required := strings.EqualFold(vals["kind"], "agent") && (action == "create" || action == "change")
	st := recipeStateFor(t.user, t.chatSessionID())
	st.mu.Lock()
	defer st.mu.Unlock()
	sig := intakeSignature(vals)
	if sig != st.sig {
		st.sig, st.written = sig, false
	}
	st.required = required
	st.action = action
	st.request = firstNonBlank(vals["goal"], vals["change"])
	st.target = vals["target"]
	st.buildOn = vals["build_on"]
}

// intakeSignature tells one intake from the next, so a second build in the
// same session gets a recipe of its own.
func intakeSignature(vals map[string]string) string {
	if len(vals) == 0 {
		return ""
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%s\x00", k, vals[k])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// recipeGateRefusal is why create_agent or update_agent may not run yet in
// this session, or "".
func recipeGateRefusal(sess *ToolSession, tool string) string {
	if sess == nil || sess.Username == "" || sess.ChatSessionID == "" {
		return ""
	}
	v, ok := builderRecipeStates.Load(sess.Username + "\x00" + sess.ChatSessionID)
	if !ok {
		return ""
	}
	st := v.(*recipeState)
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.required || st.written {
		return ""
	}
	return fmt.Sprintf("%s waits for the recipe: this session's intake is an agent being %s, so call agent_recipe first with the request in the person's words (and agent, on a change). Build from what it proposes, and show it to the person before you build. Nothing was saved.", tool, map[string]string{"create": "made", "change": "changed"}[st.action])
}

// recipeChat is the recipe call's model: the lead when this session may reach it,
// the worker otherwise. A Builder run another agent sent has no turn, and
// gets the worker its session carries.
func recipeChat(t *chatTurn, sess *ToolSession) func(ctx context.Context, msgs []Message, opts ...ChatOption) (*Response, error) {
	if t != nil && t.app != nil {
		if t.leadPermitted() {
			return t.app.LeadChat
		}
		return t.app.WorkerChat
	}
	if sess != nil && sess.LLM != nil {
		return sess.LLM.Chat
	}
	return nil
}

// agentRecipeToolDef is the agent_recipe tool.
func agentRecipeToolDef(t *chatTurn, sess *ToolSession) AgentToolDef {
	return agentRecipeTool(sess, recipeChat(t, sess))
}

func agentRecipeTool(sess *ToolSession, chat func(ctx context.Context, msgs []Message, opts ...ChatOption) (*Response, error)) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "agent_recipe",
			Description: "Write the RECIPE for an agent build before you create or change anything: how many agents, assistant or specialist, what each reaches, and why any part is its own sub-agent. " +
				"Call it first whenever you are about to make an agent or change what an existing one can reach or does; an agent intake requires it before create_agent or update_agent. " +
				"Pass the request in the person's words with anything you have learned since; on a change, name the agent. Returns the recipe, or \"No shape change.\" Build from it and show it to the person before building.",
			Parameters: map[string]ToolParam{
				"request": {Type: "string", Description: "What the person wants, in their words, plus anything settled since. Defaults to the intake's request."},
				"agent":   {Type: "string", Description: "On a change: the existing agent, by name or id. Defaults to the intake's target."},
			},
			Caps: []Capability{CapRead},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return runAgentRecipe(ctx, sess, chat, stringArg(args, "request"), stringArg(args, "agent"))
		},
	}
}

// runAgentRecipe writes one recipe. A call that cannot answer marks the
// session done anyway and says so: the recipe is advice, and a build held
// up by an unreachable model would be held up for nothing it needed.
func runAgentRecipe(ctx context.Context, sess *ToolSession, chat func(ctx context.Context, msgs []Message, opts ...ChatOption) (*Response, error), request, agentRef string) (string, error) {
	if sess == nil || sess.Username == "" {
		return "", fmt.Errorf("agent_recipe needs a session")
	}
	var st *recipeState
	if sess.ChatSessionID != "" {
		st = recipeStateFor(sess.Username, sess.ChatSessionID)
	}
	action, buildOn := "", ""
	if st != nil {
		st.mu.Lock()
		if strings.TrimSpace(request) == "" {
			request = st.request
		}
		if strings.TrimSpace(agentRef) == "" && st.action == "change" {
			agentRef = st.target
		}
		action, buildOn = st.action, st.buildOn
		st.mu.Unlock()
	}
	request = strings.TrimSpace(request)
	if request == "" {
		return "", fmt.Errorf("request is required: what the person wants, in their words")
	}
	input := recipeInput(sess, action, request, strings.TrimSpace(agentRef), buildOn)
	markWritten := func() {
		if st != nil {
			st.mu.Lock()
			st.written = true
			st.mu.Unlock()
		}
	}
	if chat == nil {
		markWritten()
		return "The recipe call has no model to ask. Decide the shape yourself: assistant or specialist, and a sub-agent only for reach that must live on it alone, a heavy transcript, or a memory of its own. Then build.", nil
	}
	resp, err := chat(ctx, []Message{{Role: "user", Content: input}},
		WithSystemPrompt(EffectivePromptText(BuilderRecipeKey, builderRecipeShipped)), WithRouteKey(builderRecipeRouteKey))
	markWritten()
	if err != nil || resp == nil || strings.TrimSpace(resp.Content) == "" {
		Log("[builder] recipe: no answer (%v); the build goes on without one", err)
		return "The recipe call did not answer this time. Decide the shape yourself: assistant or specialist, and a sub-agent only for reach that must live on it alone, a heavy transcript, or a memory of its own. Then build.", nil
	}
	recipe := strings.TrimSpace(resp.Content)
	Log("[builder] recipe (tier=%s): %s", resp.Tier, truncateObs(recipe, 200))
	if strings.HasPrefix(recipe, "No shape change") {
		return recipe + "\n\nGo ahead with the change as asked.", nil
	}
	return "Proposed recipe:\n\n" + recipe + "\n\nBuild from this. Show it to the person in a few lines before you build (they may change it), and ask them the questions it lists.", nil
}

// recipeInput is what the recipe call reads: the request, what the person ticked
// to build on, and on a change the agent as it stands with its sub-agents.
func recipeInput(sess *ToolSession, action, request, agentRef, buildOn string) string {
	var b strings.Builder
	if action != "" {
		fmt.Fprintf(&b, "THE PERSON ASKED TO %s AN AGENT.\n\n", strings.ToUpper(action))
	}
	fmt.Fprintf(&b, "REQUEST:\n%s\n", truncateObs(request, 3000))
	if buildOn = strings.TrimSpace(buildOn); buildOn != "" {
		fmt.Fprintf(&b, "\nWHAT THEY TICKED TO BUILD ON: %s\n", buildOn)
	}
	if agentRef == "" || sess.DB == nil {
		return b.String()
	}
	rec, ok, err := resolveAgentRef(sess.DB, sess.Username, agentRef)
	if err != nil || !ok {
		fmt.Fprintf(&b, "\nTHE AGENT BEING CHANGED (%q) was not found.\n", agentRef)
		return b.String()
	}
	b.WriteString("\nTHE AGENT AS IT STANDS:\n")
	b.WriteString(recipeAgentSummary(rec, ""))
	for _, sub := range listAgents(sess.DB, sess.Username) {
		if sub.OwnedBy == rec.ID {
			b.WriteString(recipeAgentSummary(sub, "sub-agent "))
		}
	}
	return b.String()
}

// recipeAgentSummary is one agent in the recipe's terms: its kind, its job,
// what it reaches.
func recipeAgentSummary(rec AgentRecord, prefix string) string {
	var b strings.Builder
	kind := "specialist"
	switch {
	case strings.EqualFold(rec.MemoryMode, "chatbot"):
		kind = "assistant"
	case strings.EqualFold(rec.MemoryMode, memoryModeCartographer):
		kind = "specialist, cartographer"
	}
	fmt.Fprintf(&b, "- %s%s (%s)", prefix, rec.Name, kind)
	if d := strings.TrimSpace(rec.Description); d != "" {
		fmt.Fprintf(&b, ": %s", truncateObs(d, 300))
	}
	b.WriteString("\n")
	switch {
	case isNoToolsSentinel(rec.AllowedTools):
		b.WriteString("  tools: none\n")
	case len(rec.AllowedTools) > 0:
		fmt.Fprintf(&b, "  tools: %s\n", strings.Join(rec.AllowedTools, ", "))
	}
	if len(rec.AttachedSources) > 0 {
		var srcs []string
		for _, s := range rec.AttachedSources {
			srcs = append(srcs, s.Kind+":"+s.ItemID)
		}
		fmt.Fprintf(&b, "  sources: %s\n", strings.Join(srcs, ", "))
	}
	if rec.Machine != "" {
		fmt.Fprintf(&b, "  machine: %s\n", rec.Machine)
	}
	if len(rec.Bulletins) > 0 {
		fmt.Fprintf(&b, "  follows boards: %s\n", strings.Join(rec.Bulletins, ", "))
	}
	if r := strings.TrimSpace(rec.Rules); r != "" {
		fmt.Fprintf(&b, "  rules: %s\n", truncateObs(strings.ReplaceAll(r, "\n", "; "), 300))
	}
	if rec.LeadModel {
		b.WriteString("  runs on the lead model\n")
	}
	return b.String()
}
