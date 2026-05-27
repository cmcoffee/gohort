package orchestrate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/temptool"
)

// inflightCancels keys per-session cancel funcs so /api/cancel can
// abort an in-flight runner. Keyed by sessionID; runner cleans up
// its own entry on exit.
//
// inflightConnectors does the same for the per-turn NetworkConnector
// so the chat surface can flip privacy LIVE mid-turn: when the user
// toggles Private ON during a running turn, the privacy endpoint
// looks up the active connector and calls SetAllowed(false). All
// in-flight + subsequent tool refusal sites re-read Allowed() on
// each call, so the flip propagates immediately.
var (
	inflightCancels    sync.Map // sessionID -> context.CancelFunc
	inflightConnectors sync.Map // sessionID -> *NetworkConnector
)

// Per-agent limits fall back to these when the agent record leaves
// the field at zero (older records, freshly cloned starters). Kept
// modest — agents that need more should set the field explicitly so
// the budget shows up in the editor.
const (
	defaultMaxPlanSteps    = 5
	defaultMaxWorkerRounds = 5
)

func resolveMaxPlanSteps(a AgentRecord) int {
	if a.MaxPlanSteps > 0 {
		return a.MaxPlanSteps
	}
	return defaultMaxPlanSteps
}

func resolveMaxWorkerRounds(a AgentRecord) int {
	if a.MaxWorkerRounds > 0 {
		return a.MaxWorkerRounds
	}
	return defaultMaxWorkerRounds
}

// sseWriter wraps an SSE response with a flusher so emit helpers
// stay tidy. Each Send writes one event and flushes.
type sseWriter struct {
	w  http.ResponseWriter
	f  http.Flusher
	mu sync.Mutex
}

func newSSEWriter(w http.ResponseWriter) *sseWriter {
	f, _ := w.(http.Flusher)
	return &sseWriter{w: w, f: f}
}

// Send writes one SSE event in the AgentLoopPanel protocol shape:
// `data: <json>\n\n`. Each call grabs the writer lock so concurrent
// emit-helpers (e.g. a future cancellation goroutine) can't interleave.
func (s *sseWriter) Send(payload map[string]any) {
	if s == nil || s.w == nil {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.w.Write([]byte("data: "))
	_, _ = s.w.Write(body)
	_, _ = s.w.Write([]byte("\n\n"))
	if s.f != nil {
		s.f.Flush()
	}
}

// SendChatEvent writes an SSE event in the ChatPanel runtime's format:
// `event: <type>\ndata: <json>\n\n`. ChatPanel's parser dispatches on
// the SSE event-name and ignores any frame without one. Used by the
// design endpoint (which mounts a ChatPanel); AgentLoopPanel uses
// Send() — different parser, different shape, kept distinct so
// callers pick the one matching their UI primitive.
func (s *sseWriter) SendChatEvent(eventType string, payload map[string]any) {
	if s == nil || s.w == nil {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.w.Write([]byte("event: "))
	_, _ = s.w.Write([]byte(eventType))
	_, _ = s.w.Write([]byte("\ndata: "))
	_, _ = s.w.Write(body)
	_, _ = s.w.Write([]byte("\n\n"))
	if s.f != nil {
		s.f.Flush()
	}
}

// Ping writes an SSE comment line (`: keepalive\n\n`). Comments are
// silently dropped by EventSource clients but keep the TCP connection
// alive through proxies / CDNs that close idle streams. Used by the
// runner's keepalive ticker so a long-thinking LLM doesn't get its
// SSE response cut off by an upstream timeout.
func (s *sseWriter) Ping() {
	if s == nil || s.w == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.w.Write([]byte(": keepalive\n\n"))
	if s.f != nil {
		s.f.Flush()
	}
}

// startKeepalive fires SSE comment pings every 10 seconds until the
// returned stop function is called. Use during long LLM calls so the
// connection stays open through nginx/CDN proxy_read_timeout (60s
// default in nginx, 100s in CloudFlare).
func startKeepalive(sse *sseWriter) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				sse.Ping()
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

// chatTurn is the per-request state shared across the runner
// pipeline (plan → step* → synthesis → consolidation). Bundling the
// (ctx, sse, udb, user, agent) quartet here keeps method signatures
// honest about what's actually shared vs. per-step input — a step
// only takes its own (prior, cur), the rest is on the receiver.
//
// Memory is cached per-turn: loaded once on first access so each
// pipeline phase sees the same snapshot, and any notes the
// consolidator writes in the background goroutine don't race the
// already-fired prompt injection.
type chatTurn struct {
	app          *OrchestrateApp
	ctx          context.Context
	sse          *sseWriter
	udb          Database
	user         string
	agent        AgentRecord
	queue        *injectionQueue   // pending mid-flight user notes for this session
	session      *ChatSession      // mutable session pointer so drained notes can be persisted
	privateMode  bool              // per-turn: drop internet tools from worker pool
	network      *NetworkConnector // SHARED instance: ctx + sess.Network + inflight registry all reference this same pointer so SetAllowed flips propagate to every read site mid-turn
	// inferredDisabled is the per-turn snapshot of the user's "Clean"
	// toggle preference — when true (or when agent.DisableInferred is
	// true), the Reference Memory layer is suppressed for this turn:
	// memory_save / memory_search / memory_forget stripped, synthesis
	// auto-ingest skipped, skills classifier suppressed (skills emit
	// derived chunks via self-training). Combined helper
	// t.inferredOff() returns the effective state. The Knowledge layer
	// (uploaded files) and Explicit Memory (facts) are unaffected.
	inferredDisabled bool
	isNewSession      bool // first turn for this session; gates background title generation
	userImages   [][]byte        // decoded image attachments from the chat panel; attached to the orchestrator's last user message

	// topic is the snake_case slug classified from this turn's user
	// message. Scopes knowledge save/search to a per-subject bucket
	// so retrieval stays sharp as the agent accumulates findings
	// across unrelated topics. Read by the knowledge tool handlers
	// (default arg), pre-plan auto-search, and consolidation, ALWAYS
	// via resolveTopic() — never the field directly — because the
	// classify call now runs CONCURRENTLY with round-1 planning
	// (handleSend launches it into topicCh) rather than blocking
	// before the response. resolveTopic blocks on topicCh exactly
	// once, the first time a caller actually needs the topic; by
	// then the classify worker call has almost always finished, so
	// it adds no perceptible latency. Empty → generalTopic fallback.
	topic         string
	topicCh       chan string
	topicOnce     sync.Once

	// staticTempToolNames is the set of persistent temp tool names
	// that were included in the orchestrator's static catalog at
	// turn start. dynamicTempTools filters these out of its per-
	// round output so the same temp tool isn't both in the static
	// set AND surfaced freshly on every round.
	staticTempToolNames map[string]bool

	// Active orchestrator bubble id — set by runPlan's streamHandler
	// when text begins, cleared by onStepHandler at the round
	// boundary. wrapToolsForActivity reads this to attach tool_call
	// and tool_result SSE events to the right bubble so the user
	// sees inline tool affordances on the right message.
	currentMsgID string
	currentMu    sync.Mutex // guards currentMsgID (handler runs in goroutines)

	// lastUsage holds the most-recent assistant-turn stats payload
	// (tokens, throughput, elapsed) captured by emitStats. handleSend
	// reads + clears it when persisting the assistant ChatMessage so
	// the saved record carries per-message usage — the AgentLoopPanel
	// then renders the same hover-only stats footer on session reload
	// that it shows live via the SSE stats event.
	lastUsageMu sync.Mutex
	lastUsage   *ChatMessageUsage

	// pipelineDepth tracks recursion into pipeline-mode sub-agents.
	// Capped at maxPipelineDepth so a pipeline tool calling another
	// pipeline tool calling another doesn't tear through the budget.
	pipelineDepth int

	// dispatchDepth tracks recursion into dispatch_to_agent. Distinct
	// from pipelineDepth because they're separate failure modes —
	// pipelines compose tools, dispatch fans out to named specialists.
	// Both are capped to prevent runaway recursive chains.
	dispatchDepth int

	// dispatchChain carries the IDs of agents already invoked higher
	// up in this dispatch chain. agentsRunAction refuses to dispatch
	// to an agent already in the chain — catches cycles like A→B→A
	// that depth alone misses (depth resets to 0 on each sub-turn,
	// so A→B→A→B→… would have looked depth-fine for a long time
	// before the cap tripped). Includes the current turn's agent ID
	// when propagated to a sub-turn.
	dispatchChain []string

	// explorerMode is flipped by the enter_explorer_mode tool when
	// AllowExplorer is set on the agent. While true, the worker
	// loop's StopRound hook stops enforcing the soft cap, so rounds
	// can continue up to the absolute hard cap (explorerHardCap).
	// Resets per worker step.
	explorerMode   bool
	explorerReason string

	// Skills active for this turn — resolved once on first call to
	// activeSkillsForTurn, cached so the system prompt assembly +
	// catalog union both see the same set. skillActivations carries
	// the classifier's reasoning (which trigger matched, what the
	// score was) — surfaced in the activity pane for confidence
	// visibility.
	skillsResolved   bool
	skillsActive     []SkillRecord
	skillActivations []SkillActivation

	// Per-turn tool log + dedup cache. Wrapped handlers append to
	// the log on first call and short-circuit to the cached result
	// on every subsequent identical call. Subsequent step prompts
	// include the log so the LLM can see "web_search('X latest')
	// already returned Y" and skip the round entirely.
	toolMu    sync.Mutex
	toolCalls []toolCallRecord

	// userDocsThisTurn flips true when the inbound chat send carried
	// at least one extracted document (PDF/DOCX/audio/text). Used by
	// the consolidation loop-break gate (turnHasFreshExternalContent)
	// alongside CapNetwork tool calls — both count as "new info
	// entered the conversation this turn."
	userDocsThisTurn bool
	toolCache map[string]string // canonical(name, args) → result
}

// toolCallRecord is one entry in the per-turn tool log. Used to build
// the "## Tool calls already made this turn" prompt section that every
// step 2+ sees, so the worker stops re-running identical searches.
type toolCallRecord struct {
	Name   string
	Args   map[string]any
	Result string
	Err    string // set when the original call failed
}

// isNetworkTool reports whether a registered ChatTool contacts the
// internet. Checks BOTH signals a tool can declare network access:
//   - IsInternetTool() bool — legacy explicit declaration
//   - Caps() containing CapNetwork — modern capability-style
// Either signal is sufficient. Tools that declare neither are
// treated as local-only and pass the Private-mode filter.
func isNetworkTool(ct ChatTool) bool {
	if ct == nil {
		return false
	}
	if it, ok := ct.(InternetTool); ok && it.IsInternetTool() {
		return true
	}
	if cp, ok := ct.(CapabilityTool); ok {
		for _, c := range cp.Caps() {
			if c == CapNetwork {
				return true
			}
		}
	}
	return false
}

// (turnHasFreshExternalContent retired alongside the synthesis
// auto-ingest path. Reference Memory is now explicit-save only —
// the LLM calls memory_save when it consciously wants to record
// something, no framework-driven capture. The loop-break gate
// this function fed isn't needed anymore.)

// appendActiveSkills folds the cached-active skills' instructions
// + per-skill corpus chunks onto the supplied system prompt and
// returns the result. Shared by the orchestrator round and the
// synthesis call so the user-facing reply gets the same skill
// voice the planning round saw.
//
// Builder is excluded upstream (activeSkillsForTurn returns nil
// for the Builder seed), so this is a no-op for Builder turns.
func (t *chatTurn) appendActiveSkills(sys string, msgs []ChatMessage) string {
	skills := t.activeSkillsForTurn(msgs)
	if len(skills) == 0 {
		return sys
	}
	for _, s := range skills {
		section := SkillPromptSection(s)
		sys += section
		Debug("[orchestrate.skills] injected section for %q (%d chars):\n%s", s.Name, len(section), section)
		// Skill corpus injection removed — skills no longer carry
		// chunks (no SelfTraining, no AttachedCollections). The
		// section above is the full payload now.
		Log("[orchestrate.skills] activated skill %q for turn (agent=%s, section=%d chars)", s.Name, t.agent.ID, len(section))
	}
	return sys
}

// renderAvailableSkillsBlock produces a compact "Available skills"
// section listing skills the user has authored that are NOT already
// active this turn. Always-in-prompt so the LLM can recognize when
// the conversation drifts into a skill's domain that the classifier
// missed (e.g. a follow-up "were you talking about X or Y?" without
// trigger words) and pull it in via the activate_skill tool. Empty
// string when DisableSkills is on, the user has no skills, or every
// skill is already active.
//
// Listing format mirrors the facts block: one line per entry,
// **name** — one-sentence description. Description is truncated to
// keep prompt cost bounded as the user's skill count grows.
func (t *chatTurn) renderAvailableSkillsBlock() string {
	if t == nil || t.agent.DisableSkills {
		return ""
	}
	// Strict allowlist — only skills the agent has explicitly
	// opted into can surface here. Without an allowlist there's
	// nothing to render.
	if len(t.agent.AllowedSkills) == 0 {
		return ""
	}
	allowed := make(map[string]bool, len(t.agent.AllowedSkills))
	for _, id := range t.agent.AllowedSkills {
		allowed[id] = true
	}
	skills := LoadSkills(t.udb, t.user)
	if len(skills) == 0 {
		return ""
	}
	activeIDs := map[string]bool{}
	for _, a := range t.skillsActive {
		activeIDs[a.ID] = true
	}
	var available []SkillRecord
	for _, s := range skills {
		if s.Disabled {
			continue
		}
		if !allowed[s.ID] {
			continue
		}
		if activeIDs[s.ID] {
			continue
		}
		available = append(available, s)
	}
	if len(available) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## Available skills\n\n")
	b.WriteString("Skills you can pull in via `activate_skill(name)` when the conversation clearly enters a domain a listed skill covers. Don't activate speculatively — only when the current question or topic shift would benefit from the skill's instructions / tools. Format: **name** — one-sentence purpose.\n\n")
	for _, s := range available {
		desc := strings.TrimSpace(s.Description)
		if len(desc) > 140 {
			desc = desc[:140] + "…"
		}
		if desc == "" {
			desc = "(no description)"
		}
		b.WriteString("- **")
		b.WriteString(s.Name)
		b.WriteString("** — ")
		b.WriteString(desc)
		b.WriteString("\n")
	}
	return b.String()
}

// classifierTrimTools shrinks an agent's default-pool tool surface
// to just the top-K tools whose descriptions match the user's
// latest message. The framework essentials (knowTools — control,
// knowledge, memory) bypass this filter entirely; they're always-
// on. This is the catalog-saturation fix: instead of dumping 40+
// tools on the LLM each turn, classifier picks the ~8 most likely
// to be useful for THIS message, and find_tools (an always-on
// meta-tool) is the LLM's escape hatch when the classifier misses
// the user's intent.
//
// Returns the input slices unchanged when:
//   - tools list is small enough not to bother (<= classifierKeepBudget)
//   - no user message is available to embed
//   - the embedder is offline (RelevantTools returns nil — we keep
//     the full surface so the agent still works)
//
// Tools surfaced from the classifier always include the high-score
// matches first; ties get stable ordering by name. Anything below
// threshold OR beyond k is dropped from THIS turn's catalog but
// still callable via find_tools.
func classifierTrimTools(ctx context.Context, msgs []ChatMessage, tools []AgentToolDef, names []string) ([]AgentToolDef, []string) {
	if len(tools) <= classifierKeepBudget {
		return tools, names
	}
	query := latestUserMessageText(msgs)
	if query == "" {
		return tools, names
	}
	hits := RelevantTools(ctx, query, classifierKeepBudget, classifierThreshold)
	if len(hits) == 0 {
		// Embedder offline OR nothing matched above threshold —
		// keep full surface rather than ship an empty catalog.
		return tools, names
	}
	keep := make(map[string]bool, len(hits))
	for _, h := range hits {
		keep[h.Name] = true
	}
	keptTools := tools[:0]
	keptNames := names[:0]
	for i, t := range tools {
		// Always preserve framework-tagged tools. They're excluded
		// from the embedding index by design (find_tools doesn't
		// surface itself from a "search for tools" query, etc.),
		// so they'd never be in `keep` — but they're always-on
		// safety nets and have to stay in the catalog regardless
		// of what the classifier matched this turn.
		if keep[t.Tool.Name] || IsFrameworkToolDef(t) {
			keptTools = append(keptTools, t)
			if i < len(names) {
				keptNames = append(keptNames, names[i])
			}
		}
	}
	return keptTools, keptNames
}

// IsFrameworkToolDef reports whether an AgentToolDef wraps a
// framework-tagged tool. We can't IsFrameworkTool() the AgentToolDef
// directly because the wrapping decorator hides the underlying
// ChatTool's interface; the registered name is the way back to
// the source tool for the check.
func IsFrameworkToolDef(td AgentToolDef) bool {
	if ct, ok := LookupChatTool(td.Tool.Name); ok {
		return IsFrameworkTool(ct)
	}
	return false
}

// classifierKeepBudget caps how many tools the classifier surfaces
// per turn. 8 fits comfortably alongside the always-on essentials
// (~12 tools — control + knowledge + memory + framework meta) for a
// ~20-tool round-0 catalog, well within local-model tolerance.
const classifierKeepBudget = 8

// classifierThreshold is the minimum cosine similarity for a tool
// to be surfaced from classifier match. Modest — high enough to
// drop tools genuinely unrelated to the message, low enough to
// surface plausibly-relevant tools without missing obvious matches.
const classifierThreshold = 0.30

// latestUserMessageText returns the most recent user-role message's
// text content, or "" if there isn't one. Skips any role that isn't
// "user" so an assistant-only history (rare; usually only on the
// first turn before the user has spoken) doesn't crash the embed.
func latestUserMessageText(msgs []ChatMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return strings.TrimSpace(msgs[i].Content)
		}
	}
	return ""
}

// renderAvailableAgentsBlock surfaces the OTHER agents in the user's
// fleet so the host LLM knows what it can dispatch to via
// agents(action="run", agent=..., message=...). Without this block
// the LLM has to call agents(action="list") first to discover them,
// which it almost never does speculatively — the result is that
// authored specialist agents (Pickleball Expert, Code Reviewer,
// etc.) are effectively invisible. Empty when the user has no other
// agents or the current agent is the only one.
//
// Excludes the current agent (don't suggest dispatching to yourself)
// and Builder (Builder is a separate routing concern handled by the
// "Building agents and tools — delegate to Builder" persona section).
func (t *chatTurn) renderAvailableAgentsBlock() string {
	if t == nil || t.udb == nil || t.user == "" {
		return ""
	}
	// Build the caller's explicit-link set once. AllowedDispatchTargets
	// has two modes depending on emptiness:
	//   - Empty (default): "allow all" — every non-Hidden agent is
	//     visible.
	//   - Non-empty: "allowlist" — ONLY listed agents are visible
	//     (regardless of their Hidden status; the explicit pick wins).
	linked := make(map[string]bool, len(t.agent.AllowedDispatchTargets))
	for _, id := range t.agent.AllowedDispatchTargets {
		linked[id] = true
	}
	restrictMode := len(linked) > 0
	all := listAgents(t.udb, t.user)
	available := all[:0]
	for _, a := range all {
		if a.ID == t.agent.ID {
			continue
		}
		if isBuilderAgent(a.ID) {
			continue
		}
		if restrictMode {
			// Allowlist mode — show only what's explicitly picked.
			if !linked[a.ID] {
				continue
			}
		} else {
			// Default mode — hide Hidden agents from the fleet block.
			if a.Hidden {
				continue
			}
		}
		available = append(available, a)
	}
	if len(available) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## Available agents\n\n")
	b.WriteString("Specialists the user has authored. **If a question lands in one of these agents' domains, DELEGATE FIRST — answer yourself only when no agent's description matches the question.** Each one is a focused brain with its own persona, tool surface, and reference material; they handle their domains better than you will from general knowledge.\n\nDelegate via `agents(action=\"run\", agent=\"<name>\", message=\"<the question or follow-up>\")`. The dispatched agent **retains its conversation across calls within this session** — repeated dispatches to the same agent see prior turns as history, so you can ask follow-ups (\"can you go deeper on point 3?\", \"what about X variant?\") without re-explaining the original context. Integrate the answers into your reply as if they were your own — don't say \"I asked X\" or \"the X agent said\"; the user doesn't know the fleet structure. Just answer with the substance.\n\nFormat: **name** — when to delegate.\n\n")
	for _, a := range available {
		desc := strings.TrimSpace(a.Description)
		if len(desc) > 140 {
			desc = desc[:140] + "…"
		}
		if desc == "" {
			desc = "(no description)"
		}
		b.WriteString("- **")
		b.WriteString(a.Name)
		b.WriteString("** — ")
		b.WriteString(desc)
		b.WriteString("\n")
	}
	return b.String()
}

// activateSkillToolDef builds the per-turn activate_skill tool.
// Looks up a skill by name (case-insensitive), appends it to the
// turn's active set, and returns the skill's instructions as the
// tool result so the LLM has them in this turn's context (without
// waiting for the next turn's system-prompt rebuild). Skill-attached
// tools join the catalog from the next round via the dynamic-tools
// feed (skillsActive is the source of truth there).
//
// chatTurn-bound (mutates t.skillsActive). Stripped when the agent
// has DisableSkills=true.
func (t *chatTurn) activateSkillToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "activate_skill",
			Description: "Activate a named skill from the 'Available skills' list for the rest of this turn. Use when the conversation has clearly entered a domain a listed skill covers but the classifier didn't fire on its triggers (topical drift, follow-up question, context-implied domain). The skill's instructions are returned in the tool result so you can apply them immediately; the skill's attached tools (if any) join your catalog next round. Don't activate speculatively or for skills that are already active — re-activation is a no-op.",
			Parameters: map[string]ToolParam{
				"name": {
					Type:        "string",
					Description: "The exact skill name from the 'Available skills' block (case-insensitive).",
				},
			},
			Required: []string{"name"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			name := strings.TrimSpace(stringArg(args, "name"))
			if name == "" {
				return "", errors.New("name is required")
			}
			skills := LoadSkills(t.udb, t.user)
			var found *SkillRecord
			for i := range skills {
				if skills[i].Disabled {
					continue
				}
				if strings.EqualFold(skills[i].Name, name) {
					tmp := skills[i]
					found = &tmp
					break
				}
			}
			if found == nil {
				return "", fmt.Errorf("no skill named %q (check the 'Available skills' block in your system prompt)", name)
			}
			// Allowlist gate — match the renderAvailableSkillsBlock
			// filter. LLM shouldn't be able to bypass scoping by
			// guessing names of skills that weren't opted into.
			allowed := false
			for _, id := range t.agent.AllowedSkills {
				if id == found.ID {
					allowed = true
					break
				}
			}
			if !allowed {
				return "", fmt.Errorf("skill %q is not enabled for this agent — ask the admin to add it via the Knowledge button's Skills picker", found.Name)
			}
			for _, a := range t.skillsActive {
				if a.ID == found.ID {
					return fmt.Sprintf("Skill %q is already active this turn — no-op.", found.Name), nil
				}
			}
			t.skillsActive = append(t.skillsActive, *found)
			t.skillActivations = append(t.skillActivations, SkillActivation{
				Skill:  *found,
				Reason: "manual",
				Score:  1.0,
			})
			Log("[orchestrate.skills] manual activation of %q for agent=%s user=%s via activate_skill", found.Name, t.agent.ID, t.user)
			body := strings.TrimSpace(found.Instructions)
			if body == "" {
				body = "(this skill has no instructions body — its value is in attached tools / corpus.)"
			}
			return fmt.Sprintf("Skill %q activated. Apply these instructions for the rest of this turn:\n\n%s\n\nAttached tools (if any) will appear in your catalog next round.", found.Name, body), nil
		},
	}
}


// roundShapePreamble returns the universal "How this round works"
// framework block that sits ABOVE the agent persona for agents
// without their own detailed phased rhythm. Builder skips it
// entirely — its phases govern the rhythm.
//
// Position above-persona is deliberate: LLMs weight recent prompt
// content heavier, so the persona (more recent) reads as the
// authoritative voice. The preamble is reference material for when
// the persona is silent on a question.
func roundShapePreamble(maxSteps int) string {
	stepBudget := fmt.Sprintf("up to %d step%s", maxSteps, plural(maxSteps))
	return "## How this round works\n\n" +
		"You have four ways to act this turn. If the persona below says otherwise, the persona wins; this is the default behavior for any case it doesn't address.\n\n" +
		"**Direct tools** — the agent's worker tool surface (web_search, fetch_url, calculate, datemath, agents, etc.). Call these inline: call → see result → call again → reply. This is the default for most turns, including ones that need several tool calls to resolve. Calling tools across multiple orchestrator rounds is fine — you see each result before the next call.\n\n" +
		"**Don't speculate-then-correct.** If you're about to call a tool, do NOT first write a full answer from training-prior knowledge that you'll then have to revise after the tool result comes back. The user sees both — a confident-sounding first answer followed by a corrected second answer reads as confused and wastes tokens. Before tools fire, either say nothing or say something terse and clearly preliminary (\"Looking that up…\", \"Let me check the corpus…\"). Save the actual answer for AFTER you have the tool result.\n\n" +
		"**Control tools** — these END the round; only one fires per response:\n\n" +
		"- **ask_user(question)** — Pause and ask the user. Use when GUESSING is the alternative (not when SEARCHING is): tool results returned multiple plausible matches, the user must choose between meaningfully different approaches, they must supply personal info you can't look up, or the request has an unresolved choice no search will settle. Pass enumerable choices as `options`. Multi-question → ask_user_form. The turn pauses here and waits for the user's next message.\n" +
		"- **respond_directly(text)** — Explicit terminate-with-reply. The text you pass IS the final reply. Use when you've done the work (inline) and want to end cleanly with a specific reply.\n" +
		"- **plan_set(steps)** — Hand off to a multi-step worker pipeline (" + stepBudget + "). Use ONLY when the turn genuinely needs decomposition into INDEPENDENT subquestions that should run as separate fresh-context workers and synthesize — research-style \"investigate A and B and C in parallel.\" Minimum 2 steps. NOT a wrapper for sequential tool calls you could make inline across rounds.\n\n" +
		"Pure conversation (greetings, opinions, well-known textbook concepts, follow-ups already answered): just reply as text — no tool needed. Time-sensitive or verifiable facts: call the relevant tool inline; don't answer from training.\n\n" +
		"**Delivering files to the user — the two-step rule.** No tool auto-attaches anymore. Producer tools (find_image, fetch_image, generate_image, download_video, screenshot_page, and any custom tool that saves a file) WRITE INTO YOUR WORKSPACE and return the saved path. To actually deliver the file to the user, follow up with:\n\n" +
		"  workspace(action=\"attach\", path=\"<the-path-the-tool-returned>\", cleanup=true)\n\n" +
		"Read the tool's result text carefully — if it says \"Stored at X\" / \"Saved at X\" / \"Written to X\" / similar, that file is in your workspace but NOT yet delivered. You must explicitly attach it. Use cleanup=true for one-shot deliveries (search results, fresh downloads); use cleanup=false when the file is also work product you might reference later. Some older tools may have descriptions that imply auto-attach — trust the result text over the description: if it returned a path, you still need to attach.\n\n" +
		"**Multiple files in one turn is fine.** When the user asks for several attachments (\"send me three duck pictures\"), produce each one (multiple find_image / fetch_image / etc. calls are OK in one turn) and then call workspace(attach) once per file you want to deliver. Each workspace(attach) delivers exactly one file; chain as many as the user asked for.\n\n" +
		"**Save what's worth remembering.** When you discover a non-obvious finding worth recalling later — an API quirk you figured out, a working approach that took effort, a configuration recipe, a complicated reference detail — call memory_save WITHOUT being asked. Use your judgment: a normal answer to a normal question isn't worth saving; a hard-won discovery is. Nothing captures findings automatically anymore; the corpus only grows when you actively save. If you recall something from a prior turn, call memory_search first to verify it's in memory; if you only RE-derive what you already had saved, don't save the duplicate.\n\n"
}

// drainNotes pulls all queued interjections and persists them as
// user messages on the session (so reload sees them too). Returns
// the drained slice for the caller to fold into the next prompt.
// Empty when there's nothing queued or no queue at all.
func (t *chatTurn) drainNotes() []injectionNote {
	if t == nil || t.queue == nil {
		return nil
	}
	taken := t.queue.Drain()
	if len(taken) == 0 {
		return nil
	}
	if t.session != nil {
		now := time.Now()
		for _, n := range taken {
			t.session.Messages = append(t.session.Messages, ChatMessage{
				Role: "user", Content: n.Text, Created: now,
			})
		}
		if saved, err := saveChatSession(t.udb, *t.session); err == nil {
			*t.session = saved
		}
	}
	ids := make([]string, len(taken))
	for i, n := range taken {
		ids[i] = n.ID
	}
	// Tell the client to mark these interjection bubbles as consumed
	// (servitor's pattern — the framework runtime already tags the
	// bubbles with data-note-id on submit).
	t.sse.Send(map[string]any{
		"kind": "block",
		"type": "orchestrate_notes_consumed",
		"ids":  ids,
	})
	return taken
}

// notesContextBlock formats a drained slice for prepending to a
// round's user content. Returns empty string when nothing was drained
// so callers can append unconditionally.
func notesContextBlock(notes []injectionNote) string {
	if len(notes) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## User notes added mid-flight\n")
	b.WriteString("The user added the following notes after the turn started. Treat as additional context / direction:\n\n")
	for _, n := range notes {
		b.WriteString("- ")
		b.WriteString(strings.ReplaceAll(strings.TrimSpace(n.Text), "\n", " "))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

// newToolSession constructs the per-turn ToolSession, then loads the
// user's approved persistent temp tools onto it so the LLM sees them
// alongside the built-in registry. Private mode mirrors the chat
// app's policy: shell-mode persistent tools stay (no network), API-
// mode persistent tools (HTTP calls) get dropped.
//
// sess.DB is the user-scoped sub-store (t.udb), not the app's root
// DB — agent-CRUD tools (create_agent / update_agent / etc.) write
// AgentRecord entries via this DB, and listAgents in the chat page
// reads from the user-scoped store. Using the app DB here would save
// agents to a bucket nobody reads from, making them invisible.
//
// Shared by runPlan (orchestrator's inline tool surface) and
// runWorkerStep (worker step) so both paths get the same persistent
// tool pool without drift.
func (t *chatTurn) newToolSession() *ToolSession {
	sess := &ToolSession{
		LLM:      t.app.LLM,
		LeadLLM:  t.app.LeadLLM,
		Username: t.user,
		DB:       t.udb,
		// Network connector — the SAME instance carried on t.ctx +
		// the inflight registry. Sharing one pointer is what makes
		// the mid-turn cutoff propagate: when the privacy endpoint
		// flips it, sess.NetworkAllowed() and
		// NetworkAllowedFromContext(ctx) both see the new state.
		Network: t.network,
		// Pipeline-mode temp tools dispatch through this runner. Caps
		// recursion depth via t.pipelineDepth (incremented on entry,
		// decremented on exit) so pipeline-calls-pipeline can't
		// infinite-loop. See runPipelineSubAgent.
		SubAgentRunner: t.runPipelineSubAgent,
	}
	// Tag with the active chat session id so SaveSessionTempTool /
	// LoadSessionTempTools can scope tool drafts to this conversation.
	// Tools the LLM authors mid-conversation (via create_pipeline_tool
	// or create_agent's inline tools[]) land in the session_temp_tools
	// bucket keyed by this id so they're callable in this session
	// immediately for verification before any other commit step.
	if t.session != nil {
		sess.ChatSessionID = t.session.ID
	}
	// Default workspace = per-user root. Tools that author scripts
	// via script_body ship them here at registration; dispatch finds
	// the same path. Tools that want explicit per-conversation
	// isolation call workspace(create) which switches sess.WorkspaceDir
	// to a managed workspace; otherwise everything operates at the
	// user root.
	//
	// History note: this was briefly auto-minting a managed workspace
	// per session. That broke existing persistent_temp_tools whose
	// script_body was shipped to the user root at authoring time —
	// the new managed workspace had no copy. Reverted; per-session
	// isolation is now opt-in via workspace(create).
	if ws, err := EnsureWorkspaceDir(t.user); err == nil {
		sess.WorkspaceDir = ws
	}
	if sess.DB == nil || sess.Username == "" {
		return sess
	}
	// Build the per-turn allowlist gate for temp tools. Mirrors the
	// registered-pool semantics in resolveWorkerTools:
	//   - no-tools sentinel → drop all temp tools (admin unchecked everything)
	//   - non-empty list    → only temp tools whose name is in the list
	//   - empty list        → no restriction (default pool)
	// Without this, unchecking a persistent / agent-scoped / draft
	// temp tool in the Tools modal had no effect: the loader added it
	// unconditionally and the LLM still saw it in the catalog.
	noTools := isNoToolsSentinel(t.agent.AllowedTools)
	var allow map[string]bool
	if !noTools && len(t.agent.AllowedTools) > 0 {
		allow = make(map[string]bool, len(t.agent.AllowedTools))
		for _, n := range t.agent.AllowedTools {
			allow[n] = true
		}
	}
	tempToolAllowed := func(name string) bool {
		if noTools {
			return false
		}
		if allow == nil {
			return true
		}
		return allow[name]
	}
	loaded := LoadPersistentTempTools(sess.DB, sess.Username)
	for _, p := range loaded {
		// Private mode hides API-mode temp tools (network side
		// effects); shell-mode is allowed because the sandbox keeps
		// them local.
		if t.privateMode && p.Tool.Mode == TempToolModeAPI {
			continue
		}
		if !tempToolAllowed(p.Tool.Name) {
			continue
		}
		tool := p.Tool
		if err := sess.AppendTempTool(&tool); err != nil {
			Log("[orchestrate.tools] persistent temp tool %q failed to load: %v", tool.Name, err)
		}
	}
	if n := len(loaded); n > 0 {
		Log("[orchestrate.tools] loaded %d persistent temp tool(s) for %s", n, t.user)
	}
	// Agent-scoped tools (AgentRecord.Tools) layer on top of the
	// user's persistent pool. Same private-mode + name-conflict
	// rules; AppendTempTool surfaces a clean error if an agent-
	// scoped name collides with a persistent one (admin renames to
	// resolve). These don't go through the approval queue — they
	// live inside the AgentRecord and inherit its trust boundary.
	agentToolsSkipped := 0
	for i := range t.agent.Tools {
		tool := t.agent.Tools[i]
		if t.privateMode && tool.Mode == TempToolModeAPI {
			continue
		}
		// NO AllowedTools gate here: agent.Tools entries are attached
		// DIRECTLY to this agent's record (via add_tool, the editor's
		// Tools field, or autoCopySessionToolsForAgent). They're
		// inherently allowed by virtue of being on the agent — the
		// AllowedTools list is for the registered-pool intersection,
		// not for per-agent attachments. Gating these here was a
		// regression that hid pipeline tools / session-drafts-promoted-
		// to-agent / admin-added shell tools from agents whose
		// AllowedTools didn't happen to name them.
		//
		// Skip silently when the tool is already loaded from the
		// user's persistent pool above — that's a common case (admin
		// approves the tool to the pool AND it stays attached to the
		// agent that authored it) and isn't a real failure. The
		// persistent copy already satisfies the agent's tool surface.
		if sess.HasTempTool(tool.Name) {
			agentToolsSkipped++
			continue
		}
		if err := sess.AppendTempTool(&tool); err != nil {
			Log("[orchestrate.tools] agent-scoped tool %q failed to load: %v", tool.Name, err)
		}
	}
	if agentToolsSkipped > 0 {
		Debug("[orchestrate.tools] skipped %d agent-scoped tool(s) already loaded from persistent pool for agent=%s", agentToolsSkipped, t.agent.ID)
	}
	if n := len(t.agent.Tools); n > 0 {
		Log("[orchestrate.tools] loaded %d agent-scoped tool(s) for agent=%s", n, t.agent.ID)
	}
	// Session-scoped tool drafts — the LLM authored these in THIS
	// conversation (e.g. for_agent-attached pipeline + bundled inline
	// tools from create_agent). Load them so the LLM can dispatch by
	// name to verify the tool works before relying on it. Persistence
	// to the agent record / approval queue already happened at author
	// time; this is purely for in-session testability.
	//
	// Drafts intentionally BYPASS the AllowedTools gate: the agent's
	// allowlist is the *committed* surface, but drafts are the
	// authoring scratchpad — the LLM needs to dispatch a draft to
	// verify it works *before* the user approves it into the agent.
	// Gating drafts on AllowedTools would make authoring impossible
	// (the tool can't be tested until it's allowed, but it isn't
	// allowed until the user has seen it work).
	if t.session != nil {
		drafts := LoadSessionTempTools(t.udb, t.session.ID)
		var cleaned int
		for i := range drafts {
			tool := drafts[i]
			if t.privateMode && tool.Mode == TempToolModeAPI {
				continue
			}
			if err := sess.AppendTempTool(&tool); err != nil {
				// Name conflict with persistent or agent-scoped pool —
				// the committed version wins (it's the canonical copy).
				// add_tool deliberately writes BOTH the session draft
				// and the agent.Tools entry so the new tool is callable
				// mid-turn; on the next turn the committed copy is the
				// real one and this draft is just stale duplication.
				// Drop it from session_temp_tools so the Tools UI and
				// the runtime stop showing two of the same thing.
				RemoveSessionTempTool(t.udb, t.session.ID, tool.Name)
				cleaned++
				Debug("[orchestrate.tools] dropped redundant session draft %q (committed copy exists)", tool.Name)
			}
		}
		if n := len(drafts); n > 0 {
			Log("[orchestrate.tools] loaded %d session-draft tool(s) for session=%s (cleaned %d redundant)", n, t.session.ID, cleaned)
		}
	}
	return sess
}

// dynamicTempTools returns a DynamicTools callback that exposes the
// session's loaded temp tools to the agent loop each round. Wraps
// each one through wrapToolsForActivity so they get the same inline
// tool_call / tool_result SSE emissions + activity-pane rendering +
// per-turn cache as the built-in tools — without this they fire
// invisibly and the user can't see the temp tool ran.
func (t *chatTurn) dynamicTempTools(sess *ToolSession) func() []AgentToolDef {
	return func() []AgentToolDef {
		defs := temptool.BuildAgentToolDefs(sess)
		t.wrapToolsForActivity(sess, defs)
		return defs
	}
}

// dynamicNewTempTools returns the agent loop's per-round dynamic
// tool feed: ONLY temp tools created mid-turn that weren't in the
// static snapshot. Persistent temp tools live in the static catalog;
// this surfaces freshly-authored ones so the LLM can dispatch by
// name to verify a new tool works before the user approves it.
//
// Replaced the old dynamicToolsWithGroups now that group-expansion
// is retired (vector pre-selection picks tools by relevance; no
// expand_tool_group meta-tool, no synthetic group placeholders).
func (t *chatTurn) dynamicNewTempTools(sess *ToolSession) func() []AgentToolDef {
	base := t.dynamicTempTools(sess)
	return func() []AgentToolDef {
		raw := base()
		out := make([]AgentToolDef, 0, len(raw))
		for _, td := range raw {
			if t.staticTempToolNames[td.Tool.Name] {
				continue // already in the static catalog this turn
			}
			out = append(out, td)
		}
		// Source-hook auto-tools: walked per-round so admin
		// add/remove/toggle takes effect without a process restart.
		// Filtered to ExposeToLLM hooks; paywall hooks excluded
		// (they augment fetch_url, not stand-alone). Cheap call —
		// reads from in-memory sourceHookRegistry.
		out = append(out, BuildSourceHookAgentToolDefs(t.app.DB)...)
		// Private-mode backstop. The dynamic feed re-introduces tools
		// that the static catalog filter already dropped:
		//   - mid-turn temp tools (LLM authored after runPlan started)
		//   - source-hook auto-tools (RSS / API readers — network by definition)
		// Without this pass, a Private turn gets network tools handed
		// back round-by-round even though the opening catalog was clean.
		// Mirrors the static-catalog filter in runPlan.
		if t.privateMode {
			filtered := out[:0]
			for _, td := range out {
				hasNet := false
				for _, c := range td.Tool.Caps {
					if c == CapNetwork {
						hasNet = true
						break
					}
				}
				if hasNet {
					continue
				}
				filtered = append(filtered, td)
			}
			out = filtered
		}
		return out
	}
}

// decodeUserImages turns the base64 strings the chat panel ships in
// the send body into raw bytes for the Message.Images field. Failures
// are logged and skipped — a corrupt image shouldn't kill the turn.
func decodeUserImages(b64s []string) [][]byte {
	if len(b64s) == 0 {
		return nil
	}
	out := make([][]byte, 0, len(b64s))
	for i, s := range b64s {
		if s == "" {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			Log("[orchestrate.images] decode failed for attachment %d: %v", i, err)
			continue
		}
		out = append(out, b)
	}
	return out
}

// setCurrentMsgID records which assistant bubble the wrap-emit
// helpers should target for tool_call/tool_result SSE events.
// Concurrent-safe so a tool call firing during a stream chunk
// can't race with the streamHandler that sets the id.
func (t *chatTurn) setCurrentMsgID(id string) {
	t.currentMu.Lock()
	t.currentMsgID = id
	t.currentMu.Unlock()
}

// getCurrentMsgID returns the active bubble id (may be empty).
func (t *chatTurn) getCurrentMsgID() string {
	t.currentMu.Lock()
	id := t.currentMsgID
	t.currentMu.Unlock()
	return id
}

// resolveWorkerTools builds the agent's effective tool surface for
// this turn: default pool (Read+Network non-blocked tools + unannotated
// orchestrate-internal tools) intersected with agent.AllowedTools when
// the agent set an explicit allowlist; Private mode then drops
// internet-flagged tools. Returns the resolved AgentToolDefs (handlers
// bound to sess) and the final name list for logging.
//
// Shared by runPlan (orchestrator's inline tool surface) and
// runWorkerStep (worker step execution) so the two pipelines agree on
// what's available without drift.
func (t *chatTurn) resolveWorkerTools(sess *ToolSession) ([]AgentToolDef, []string, error) {
	defaultNames := availableWorkerToolNames()
	var toolNames []string
	switch {
	case isNoToolsSentinel(t.agent.AllowedTools):
		// Sentinel — admin explicitly unchecked every tool in the
		// Tools modal. Effective optional-tool list is empty.
		// Framework always-on tools (plan_set, respond_directly,
		// stay_silent, keep_going) get appended later in the catalog
		// build, so the agent still has the minimum it needs to
		// produce a response — just no read/network/write tools.
		toolNames = nil
	case len(t.agent.AllowedTools) > 0:
		allow := make(map[string]bool, len(t.agent.AllowedTools))
		for _, n := range t.agent.AllowedTools {
			allow[n] = true
		}
		for _, n := range defaultNames {
			if allow[n] {
				toolNames = append(toolNames, n)
			}
		}
	default:
		toolNames = defaultNames
	}
	// Always include `workspace` regardless of cap filtering. Workspace
	// owns the delivery primitive (action="attach") that every producer
	// tool routes through — it's universal infrastructure, not a
	// per-agent capability choice. Without this, agents whose AllowedTools
	// is empty (default pool) miss workspace because its CapWrite +
	// CapExecute actions get it filtered out of the Read+Network default
	// pool. The LLM then sees producer tools telling it to call
	// workspace(attach) but has no workspace tool in its catalog —
	// silent never-attach failure.
	//
	// Skip the auto-include when the no-tools sentinel is set: the
	// admin explicitly wants NO optional tools, and workspace is an
	// optional tool. Without producers there's nothing to deliver
	// either, so workspace would be dead weight.
	if !isNoToolsSentinel(t.agent.AllowedTools) && !slices.Contains(toolNames, "workspace") {
		toolNames = append(toolNames, "workspace")
	}
	// Per-turn Private mode drops tools that contact the internet.
	// Applied AFTER the agent's allowlist so non-network tools the
	// agent allowed (list_agents, calculate, …) keep working.
	// Checks BOTH the legacy IsInternetTool interface AND the modern
	// Caps() declaration — a tool tagged CapNetwork via Caps() but
	// missing IsInternetTool would otherwise slip past this filter
	// (silently breaking Private mode's contract).
	if t.privateMode {
		filtered := toolNames[:0]
		for _, n := range toolNames {
			ct, ok := FindChatTool(n)
			if !ok {
				continue
			}
			if isNetworkTool(ct) {
				continue
			}
			filtered = append(filtered, n)
		}
		toolNames = filtered
	}
	tools, err := GetAgentToolsWithSession(sess, toolNames...)
	if err != nil {
		return nil, nil, err
	}
	// Builder gets the unregistered authoring tools appended here —
	// they don't live in any global registry, so they reach the
	// catalog only via this explicit code path. Identity check
	// against the seed-builder ID prevents the appendage from
	// leaking to other agents.
	if isBuilderAgent(t.agent.ID) {
		extra := builderInternalTools(sess)
		tools = append(tools, extra...)
		for _, td := range extra {
			toolNames = append(toolNames, td.Tool.Name)
		}
	}
	// Active skills can bring extra tools into the catalog. Resolve
	// each one through the same registry path — missing names are
	// silently dropped (e.g. a skill that names create_agent on a
	// non-Builder agent just doesn't add it, preserving exclusivity).
	if skills := t.activeSkillsForTurn(nil); len(skills) > 0 {
		have := make(map[string]bool, len(toolNames))
		for _, n := range toolNames {
			have[n] = true
		}
		addByName := func(name string) {
			if have[name] {
				return
			}
			// Private-mode guard: when the user toggled Private on,
			// the agent's catalog was already stripped of internet-
			// capable tools earlier in this function. Skills must
			// respect that — without this check a skill that lists
			// web_search / fetch_url / etc. in its allowed_tools
			// would silently re-enable internet access mid-turn,
			// silently violating the user's Private-mode contract.
			if t.privateMode {
				if ct, ok := FindChatTool(name); ok {
					if isNetworkTool(ct) {
						Debug("[orchestrate.skills] skipping %q for skill — private mode active", name)
						return
					}
				}
			}
			if td, err := GetAgentToolsWithSession(sess, name); err == nil && len(td) > 0 {
				tools = append(tools, td[0])
				toolNames = append(toolNames, name)
				have[name] = true
			}
		}
		for _, s := range skills {
			for _, name := range s.AllowedTools {
				addByName(name)
			}
		}
	}
	return tools, toolNames, nil
}

// wrapToolsForActivity decorates each tool's handler with:
//   - per-turn cache short-circuit (skip the call if the same
//     (name, args) returned already)
//   - activity-pane cmd / output / error rows (for apps that show
//     the activity pane — orchestrate locks it off, servitor uses it)
//   - inline tool_call / tool_result SSE events attached to the
//     active conversation-pane bubble (chat-app style — the only
//     way users see tool use when the activity pane is hidden)
//   - per-turn tool log recording (for later step prompts)
//
// Lazy-materializes an "orch-…" bubble if a tool fires before any
// text has streamed in this round, so the call has somewhere to land
// agentCanAuthorAgents reports whether the active agent has access to
// the create_agent tool — i.e. whether it's an agent-authoring agent.
// True when AllowedTools is empty (= default pool, which includes
// create_agent) OR explicitly lists "create_agent". Used by
// create_pipeline_tool's handler to gate the user-wide-with-approval
// path: agents that can author other agents almost always mean to
// either bundle inline (case A in the prompt) or attach via for_agent
// (case B). The user-wide approval queue strands the LLM because the
// tool name doesn't resolve until admin review.
func (t *chatTurn) agentCanAuthorAgents() bool {
	if t == nil {
		return false
	}
	if len(t.agent.AllowedTools) == 0 {
		return true
	}
	for _, n := range t.agent.AllowedTools {
		if n == "create_agent" || n == "*" {
			return true
		}
	}
	return false
}

// filterToolAuthoringWithoutFocus prunes overlapping or unfireable
// authoring tools from the catalog. Two distinct surfaces survive:
//
//  - **add_tool** attaches a tool to the agent currently in
//    AuthoringAgentID focus. Refuses without focus, so hide it when
//    no focus is set (otherwise the LLM pattern-matches and burns
//    rounds calling a tool that always errors).
//  - **tool_def** is the standalone session/user-scoped grouped tool
//    builder. Stays visible in BOTH states — when there's no focus
//    it's the only authoring path; when there IS focus the LLM picks
//    between "attach to this agent" (add_tool) and "one-off session
//    tool" (tool_def) based on intent. The Chat prompt teaches the
//    distinction.
//
// The legacy unbundled trio (create_temp_tool / create_api_tool /
// create_pipeline_tool) is always dropped — tool_def is the grouped
// replacement and exposing both shapes guarantees the
// oscillation-between-authoring-tools loop we already saw.
//
// Returns filtered (tools, names) preserving original order.
func filterToolAuthoringWithoutFocus(tools []AgentToolDef, names []string, session *ChatSession) ([]AgentToolDef, []string) {
	hasFocus := session != nil && session.AuthoringAgentID != ""

	// Compute which tools to drop based on the active conditions.
	drop := map[string]bool{
		// Always dropped — superseded by tool_def's grouped action surface.
		"create_pipeline_tool": true,
		"create_temp_tool":     true,
		"create_api_tool":      true,
	}
	if !hasFocus {
		// add_tool refuses without focus — hide it so the LLM doesn't
		// burn a round calling something that's guaranteed to error.
		// tool_def stays visible: it's the standalone authoring path
		// (one-off session/user tool, no agent attachment).
		drop["add_tool"] = true
	}
	if len(drop) == 0 {
		return tools, names
	}

	filteredTools := tools[:0]
	for _, td := range tools {
		if drop[td.Tool.Name] {
			continue
		}
		filteredTools = append(filteredTools, td)
	}
	filteredNames := names[:0]
	for _, n := range names {
		if drop[n] {
			continue
		}
		filteredNames = append(filteredNames, n)
	}
	return filteredTools, filteredNames
}

// explicitOff returns whether the Explicit Memory layer (always-in-
// prompt facts via store_fact) is suppressed for this turn. Gated by
// DisableExplicit only — Explicit Memory is configuration-level,
// either the agent has a facts surface or it doesn't. Callers gate
// store_fact / list_facts / forget_fact registration + the "Saved
// facts" prompt-injection block on this helper.
func (t *chatTurn) explicitOff() bool {
	if t == nil {
		return true
	}
	return t.agent.DisableExplicit
}

// inferredOff returns whether the Reference Memory layer (vector-grown
// derived chunks via memory_save + synthesis auto-ingest) is
// suppressed for this turn. True when EITHER the agent has
// DisableInferred=true OR the per-turn Clean toggle is on. Callers
// gate memory_save / memory_search / memory_forget registration,
// synthesis auto-ingest, derived-chunk recall in auto-inject, and
// the skills classifier (skills emit derived chunks via self-training)
// on this helper.
func (t *chatTurn) inferredOff() bool {
	if t == nil {
		return true
	}
	return t.agent.DisableInferred || t.inferredDisabled
}

// gateAgentCRUDTools wraps the agent-CRUD tool handlers with a build-
// plan auto-advance hook so each successful authoring tool ticks the
// next pending step off the user-visible plan card even when the LLM
// forgets to call mark_step_done in the same response.
//
// The previous two-turn gate (propose-then-act with mandatory
// ask_user in between) was removed — the rhythm is now enforced via
// prompt + plan_set decomposition instead of a runtime block. The
// gate kept producing false-positive blocks on legitimate flows
// (e.g. plan_set worker steps where the confirm phase had already
// happened but the awaiting-confirm flag hadn't propagated by the
// time the worker fired). Trust the prompt's "Phase 3 — CONFIRM" +
// plan_set boundary instead.
func (t *chatTurn) gateAgentCRUDTools(tools []AgentToolDef) {
	autoAdvance := map[string]bool{
		"create_agent": true,
		"update_agent": true,
		"clone_agent":  true,
		"delete_agent": true,
		"add_tool":     true,
	}
	for i := range tools {
		name := tools[i].Tool.Name
		orig := tools[i].Handler
		if orig == nil || !autoAdvance[name] {
			continue
		}
		toolName := name // closure capture
		tools[i].Handler = func(args map[string]any) (string, error) {
			result, err := orig(args)
			if err == nil {
				// Auto-advance the next pending plan step. Summary is
				// the first line of the tool result (typically the
				// directive line like "AGENT_CREATED ok. id=…").
				summary := firstLineSnippet(result, 120)
				if n := t.autoAdvanceBuildPlan(summary); n > 0 {
					Debug("[orchestrate.build_plan] auto-advanced step %d after %s success", n, toolName)
				}
			}
			return result, err
		}
	}
}

// firstLineSnippet returns the first non-empty line of s clipped to
// max chars. Used to derive a one-line summary for auto-advanced
// build-plan steps when the tool result is multi-line.
func firstLineSnippet(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[:nl]
	}
	s = strings.TrimSpace(s)
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

// in the conversation pane. Mutates the slice in place. Returns it
// for chaining.
//
// Optional labelPrefix prepends to every cmd row emitted by the
// wrapped tools — used by sub-agent dispatches to visually nest
// the sub-agent's tool calls under the parent ("↳ [Pickleball
// Coach] knowledge_search(...)"). Empty prefix = no nesting marker
// (the default for top-level / parent wraps).
func (t *chatTurn) wrapToolsForActivity(sess *ToolSession, tools []AgentToolDef, labelPrefix ...string) []AgentToolDef {
	prefix := ""
	if len(labelPrefix) > 0 {
		prefix = labelPrefix[0]
	}
	for i := range tools {
		name := tools[i].Tool.Name
		orig := tools[i].Handler
		if orig == nil {
			continue
		}
		tools[i].Handler = func(args map[string]any) (string, error) {
			// Activity-pane cmd / inline tool_call go in parallel so
			// both views work: apps with activity visible (servitor)
			// see the cmd row; apps with it hidden (orchestrate)
			// see the inline chip.
			//
			// Tools listed in hiddenToolChips don't render either —
			// they have a better-than-chip rendering elsewhere (the
			// reply itself for respond_directly, the status line for
			// send_status) and the chip is redundant noise. We still
			// run the handler, snapshot attachments, and record the
			// call for the toolLogPromptSection — only the user-
			// facing emissions are skipped.
			hidden := hiddenToolChips[name]
			callLabel := prefix + formatToolCall(name, args)
			if cached, ok := t.lookupToolCache(name, args); ok {
				if !hidden {
					t.sse.Send(map[string]any{
						"kind": "activity",
						"type": "cmd",
						"id":   activityCheapID(),
						"text": "♻ " + callLabel + " (cached)",
					})
					if msgID := t.ensureBubbleForTool(); msgID != "" {
						t.emitToolCall(msgID, name, args, " (cached)", prefix)
						t.emitToolResult(msgID, name, cached, nil)
					}
				}
				return cached, nil
			}
			var msgID string
			if !hidden {
				t.sse.Send(map[string]any{
					"kind": "activity",
					"type": "cmd",
					"id":   activityCheapID(),
					"text": callLabel,
				})
				msgID = t.ensureBubbleForTool()
				if msgID != "" {
					t.emitToolCall(msgID, name, args, "", prefix)
				}
			}
			imgN, vidN, fileN := sessAttachmentCounts(sess)
			out, err := orig(args)
			rec := toolCallRecord{Name: name, Args: args, Result: out}
			if err != nil {
				rec.Err = err.Error()
				if !hidden {
					t.sse.Send(map[string]any{
						"kind": "activity",
						"type": "error",
						"id":   activityCheapID(),
						"text": fmt.Sprintf("%s → %v", name, err),
					})
				}
			} else if trimmed := strings.TrimSpace(out); trimmed != "" && !hidden {
				t.sse.Send(map[string]any{
					"kind": "activity",
					"type": "output",
					"id":   activityCheapID(),
					"text": truncate(out, 4000),
				})
			}
			if msgID != "" {
				t.emitToolResult(msgID, name, out, err)
				t.flushNewAttachments(sess, msgID, imgN, vidN, fileN)
			}
			if err == nil && agentMutationTools[name] {
				// Signal the chat page that the dropdown's option list
				// is stale. The page's listener re-fetches /api/agents
				// and rebuilds the select; UI stays in sync without
				// the user having to reload after a Chat-driven
				// create_agent / update_agent / delete_agent / clone.
				t.sse.Send(map[string]any{
					"kind": "event",
					"name": "orchestrate_agents_changed",
				})
			}
			t.recordToolCall(rec)
			return out, err
		}
	}
	return tools
}

// agentMutationTools is the set of agent-CRUD tools whose successful
// invocation invalidates the agent dropdown's option list. Kept here
// (next to the wrapper that reads it) rather than spread across the
// individual tool files because the wrapper is what owns the SSE.
var agentMutationTools = map[string]bool{
	"create_agent": true,
	"update_agent": true,
	"delete_agent": true,
	"clone_agent":  true,
	// add_tool mutates AgentRecord.Tools (the bundled-custom-tools
	// list). The Tools button label reads that count, so include the
	// signal here too — the page-side listener refreshes the count
	// without the user having to reopen the agent dropdown.
	"add_tool": true,
	// tool_def writes to session_temp_tools. The Tools button label
	// also reflects session tools, so its grouped surface gets the
	// same refresh signal. Fires on every tool_def action, including
	// list/help/delete — cheap to refresh, no harm in over-firing.
	"tool_def": true,
}

// hiddenToolChips lists tools whose invocation should NOT render a
// tool_call / tool_result chip in the conversation pane (and should
// not produce activity-pane rows either). These are tools whose
// effect is already visible to the user through a richer surface —
// respond_directly's "result" is the final reply text the user reads;
// send_status's status line shows up via the framework's status
// channel. A chip alongside the better surface is just noise.
//
// The wrapper still runs the handler, drains any attachments the
// tool produced, and records the call into the per-turn tool log so
// follow-up step prompts can reference it. Only the user-facing
// emissions are skipped.
var hiddenToolChips = map[string]bool{
	"respond_directly": true,
	"send_status":      true,
	"plan_set":         true,
	"ask_user_form":    true,
}

// sessAttachmentCounts snapshots the session's attachment slice lengths
// so wrapToolsForActivity can detect new entries the tool appended
// during its run. nil-safe — control tools may pass nil sess when they
// have no attachment surface. Reads are race-free because tools run
// synchronously within the agent loop goroutine (mirrors chat's
// flushNewImages assumption).
func sessAttachmentCounts(sess *ToolSession) (int, int, int) {
	if sess == nil {
		return 0, 0, 0
	}
	return len(sess.Images), len(sess.Videos), len(sess.Files)
}

// flushNewAttachments emits SSE events for any image / video / file
// entries the tool appended to sess past the snapshot lengths. Each
// payload carries msgID so the runtime renders them under the bubble
// the tool fired from. Mirrors chat's flushNewImages pattern but
// targets a specific assistant bubble instead of "the current one".
func (t *chatTurn) flushNewAttachments(sess *ToolSession, msgID string, imgN, vidN, fileN int) {
	if sess == nil {
		return
	}
	// Use the session's claim-API: each call atomically claims an
	// exclusive range of new attachments and advances the per-
	// session flushed marker. This prevents the parallel-goroutine
	// race where two tool-call goroutines each captured len=0
	// before either appended, then each flushed the FULL slice
	// (causing 2× delivery on 2 distinct attach calls, 3× on 3, etc).
	// imgN/vidN/fileN are kept in the signature for backward-compat
	// callers but ignored — the session-level markers are
	// authoritative.
	_ = imgN
	_ = vidN
	_ = fileN
	for _, b64 := range sess.ClaimUnflushedImages() {
		t.sse.Send(map[string]any{
			"kind":   "image",
			"msg_id": msgID,
			"data":   b64,
		})
	}
	for _, b64 := range sess.ClaimUnflushedVideos() {
		t.sse.Send(map[string]any{
			"kind":   "video",
			"msg_id": msgID,
			"data":   b64,
		})
	}
	for _, f := range sess.ClaimUnflushedFiles() {
		t.sse.Send(map[string]any{
			"kind":      "file",
			"msg_id":    msgID,
			"name":      f.Name,
			"mime_type": f.MimeType,
			"data":      f.Data,
			"size":      f.Size,
		})
	}
}

// ensureBubbleForTool returns the active orchestrator bubble id,
// materializing an empty assistant bubble if no text has streamed in
// this round yet. That way a tool-only round still gives the inline
// tool chip somewhere to attach.
func (t *chatTurn) ensureBubbleForTool() string {
	id := t.getCurrentMsgID()
	if id != "" {
		return id
	}
	id = fmt.Sprintf("orch-%d", time.Now().UnixNano())
	t.sse.Send(map[string]any{
		"kind": "message",
		"role": "assistant",
		"id":   id,
		"text": "",
	})
	t.setCurrentMsgID(id)
	return id
}

// emitToolCall emits the inline tool-chip SSE event Agency renders
// in the conversation pane. namePrefix is optional — when set
// (variadic), it prepends to the chip's name field so sub-agent
// dispatches show as "↳ [Target Name] knowledge_search" instead
// of blending in with the parent's own tool calls. The canonical
// tool name passed to the LLM is unaffected — only the display
// chip changes.
func (t *chatTurn) emitToolCall(msgID, name string, args map[string]any, suffix string, namePrefix ...string) {
	displayName := name
	if len(namePrefix) > 0 && namePrefix[0] != "" {
		displayName = namePrefix[0] + name
	}
	payload := map[string]any{
		"kind":   "tool_call",
		"msg_id": msgID,
		"name":   displayName,
		"args":   summarizeToolArgs(args) + suffix,
	}
	// Mark pipeline-mode temp tools so the client can decorate the
	// pill differently (today: an emoji prefix). Detected by walking
	// the session's TempTools list — cheap, the list is small.
	if kind := t.toolKindFor(name); kind != "" {
		payload["tool_kind"] = kind
	}
	t.sse.Send(payload)
}

// toolKindFor returns "pipeline" when name belongs to a pipeline-mode
// TempTool in either the session-defined or persistent pool. Empty
// string for everything else (regular registered tools, shell/api
// temp tools).
func (t *chatTurn) toolKindFor(name string) string {
	if t.session == nil {
		return ""
	}
	// Walk session-resolved TempTools first (faster, hits the common
	// case). Persistent store is consulted only if not found inline.
	// Cheap either way at gohort scale.
	for _, attached := range LoadSessionTempTools(t.udb, t.session.ID) {
		if attached.Name == name && attached.Mode == TempToolModePipeline {
			return "pipeline"
		}
	}
	for _, p := range LoadPersistentTempTools(t.udb, t.user) {
		if p.Tool.Name == name && p.Tool.Mode == TempToolModePipeline {
			return "pipeline"
		}
	}
	return ""
}

func (t *chatTurn) emitToolResult(msgID, name, out string, err error) {
	payload := map[string]any{
		"kind":   "tool_result",
		"msg_id": msgID,
		"name":   name,
	}
	if err != nil {
		payload["result"] = "error: " + err.Error()
	} else {
		payload["result"] = truncate(out, 4000)
	}
	t.sse.Send(payload)
}

// summarizeToolArgs renders the args map into a single compact
// "key=val, key=val" string for the inline tool chip's summary row.
// Drops newlines, clips long values. Empty args produce empty string.
func summarizeToolArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		var s string
		switch vv := args[k].(type) {
		case string:
			s = vv
		case []any:
			s = fmt.Sprintf("[%d]", len(vv))
		case map[string]any:
			s = "{…}"
		case nil:
			s = "null"
		default:
			s = fmt.Sprintf("%v", vv)
		}
		s = strings.ReplaceAll(s, "\n", " ")
		if len(s) > 60 {
			s = s[:60] + "…"
		}
		parts = append(parts, fmt.Sprintf("%s=%q", k, s))
	}
	return strings.Join(parts, ", ")
}

// lastUserContent returns the .Content of the last user-role message
// in the LLM history, or "" when there isn't one. Used by the
// pre-plan knowledge search to query against the current user
// message before kicking off the orchestrator.
func lastUserContent(msgs []Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i].Content
		}
	}
	return ""
}

// priorAssistantContext returns a continuity block for the worker —
// the user's immediately preceding turn (their question + the
// orchestrator's reply) so follow-ups like "tell me more" land with
// the right referent. Empty when this is the first turn or the
// session pointer isn't available.
//
// We pull from t.session.Messages (the in-memory mutable session
// the runner has been writing to), filter out the CURRENT user
// message (already the last entry), and surface the prior user +
// assistant exchange. Older history isn't included — the worker is
// step-focused, not conversation-aware; the orchestrator and the
// synthesis round handle deep history.
func (t *chatTurn) priorAssistantContext() string {
	if t == nil || t.session == nil {
		return ""
	}
	msgs := t.session.Messages
	// Last entry is the user's current message (handleSend appended
	// it). Look at what comes before that.
	if n := len(msgs); n > 0 && msgs[n-1].Role == "user" {
		msgs = msgs[:n-1]
	}
	if len(msgs) == 0 {
		return ""
	}
	var lastUser, lastAssistant string
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" && lastAssistant == "" {
			lastAssistant = strings.TrimSpace(msgs[i].Content)
			continue
		}
		if msgs[i].Role == "user" && lastUser == "" {
			lastUser = strings.TrimSpace(msgs[i].Content)
		}
		if lastUser != "" && lastAssistant != "" {
			break
		}
	}
	if lastAssistant == "" && lastUser == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Prior turn in this conversation\n")
	b.WriteString("Use this to resolve references like \"it\", \"that\", \"the X you mentioned\" in the user's new message.\n\n")
	if lastUser != "" {
		b.WriteString("**User previously asked:** ")
		b.WriteString(snippetOneLine(lastUser, 600))
		b.WriteString("\n\n")
	}
	if lastAssistant != "" {
		b.WriteString("**You replied:** ")
		b.WriteString(snippetOneLine(lastAssistant, 800))
		b.WriteString("\n\n")
	}
	return b.String()
}

// titleAfterFirstTurn fires a background goroutine that asks the
// worker LLM for a short, descriptive title given the first
// user/assistant exchange, then patches it onto the session. No-op
// when this isn't a fresh session or there's no LLM. Failures stay
// local — title generation is an optimization, not part of the
// reply contract.
//
// The goroutine takes its own context (background + timeout) since
// the parent request returns as soon as the reply is delivered;
// keeping the title work on the request's ctx would cancel it the
// moment handleSend returns.
func (t *chatTurn) titleAfterFirstTurn() {
	if t == nil || !t.isNewSession || t.app == nil || t.app.LLM == nil || t.session == nil {
		return
	}
	// Snapshot what the goroutine needs — the request's chatTurn /
	// session pointer don't outlive handleSend.
	llm := t.app.LLM
	udb := t.udb
	agentID := t.agent.ID
	sessID := t.session.ID
	go func() {
		defer func() {
			if r := recover(); r != nil {
				Log("[orchestrate.title] panicked for session=%s: %v", sessID, r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// Re-load so the goroutine sees the persisted messages
		// (the parent already saved the assistant reply before
		// firing this).
		s, ok := loadChatSession(udb, agentID, sessID)
		if !ok {
			return
		}
		title := generateSessionTitle(ctx, llm, s)
		if title == "" || title == s.Title {
			return
		}
		s.Title = title
		_, _ = saveChatSession(udb, s)
		Log("[orchestrate.title] session=%s renamed: %q", sessID, title)
	}()
}

// emitStats pushes a per-message stats payload (tk/s, input/output/
// thinking tokens, elapsed) into the conversation pane so the user can
// see throughput per assistant turn. Mirrors the chat app's
// "12.3 tk/s · 1450 in · 230 out · 187 think · 18.7s" footer.
//
// Nil-safe: tool-only LLM rounds (no content output) get an empty
// payload and the framework skips rendering.
func (t *chatTurn) emitStats(msgID string, resp *Response, start time.Time) {
	if msgID == "" {
		return
	}
	elapsedMs := time.Since(start).Milliseconds()
	payload := map[string]any{
		"kind":       "stats",
		"id":         msgID,
		"elapsed_ms": elapsedMs,
	}
	usage := &ChatMessageUsage{ElapsedMs: elapsedMs}
	if resp != nil {
		payload["input_tokens"] = resp.InputTokens
		payload["output_tokens"] = resp.OutputTokens
		payload["reasoning_tokens"] = resp.ReasoningTokens
		usage.InputTokens = resp.InputTokens
		usage.OutputTokens = resp.OutputTokens
		usage.ReasoningTokens = resp.ReasoningTokens
		// Prefer the backend's per-phase throughput (llama.cpp) when
		// available — matches what the user sees in llama.cpp's own
		// UI. Fall back to a coarse output_tokens / elapsed otherwise.
		if resp.PredictedPerSecond > 0 {
			payload["tokens_per_sec"] = resp.PredictedPerSecond
			payload["prompt_per_sec"] = resp.PromptPerSecond
			usage.TokensPerSec = resp.PredictedPerSecond
			usage.PromptPerSec = resp.PromptPerSecond
		} else if resp.OutputTokens > 0 {
			elapsed := time.Since(start)
			if elapsed > 0 {
				rate := float64(resp.OutputTokens) / elapsed.Seconds()
				payload["tokens_per_sec"] = rate
				usage.TokensPerSec = rate
			}
		}
	}
	t.sse.Send(payload)
	t.lastUsageMu.Lock()
	t.lastUsage = usage
	t.lastUsageMu.Unlock()
}

// drainLastUsage returns the most-recent emitStats usage and clears
// the slot. Called from handleSend right before persisting an
// assistant ChatMessage so the saved record carries the stats footer.
// Returns nil when no stats event has fired (tool-only round, error
// before any LLM call) — callers omit Usage in that case.
func (t *chatTurn) drainLastUsage() *ChatMessageUsage {
	t.lastUsageMu.Lock()
	u := t.lastUsage
	t.lastUsage = nil
	t.lastUsageMu.Unlock()
	return u
}

// emitStatus pushes a phase-narration row into the activity pane.
// Mirrors servitor's status events ("Investigator: synthesizing…",
// "Verifying names…") — high-level signposts distinct from the plan
// block's per-step pips and the per-tool-call cmd/output rows.
//
// No bubble reset here. orchestrate locks the activity pane off, so
// the status text is invisible in this surface anyway; resetting
// currentMsgID would split consecutive tool calls across separate
// bubbles for an invisible event. Visible new cards (intent blocks
// at worker-step boundaries, message_done at round close) own the
// bubble reset explicitly.
func (t *chatTurn) emitStatus(text string) {
	t.sse.Send(map[string]any{
		"kind": "activity",
		"type": "status",
		"id":   activityCheapID(),
		"text": text,
	})
}

// ackTimeout bounds the fast acknowledgment call. Short — the ack
// is only useful if it lands while the user is staring at dead air;
// a slow ack is worse than none.
const ackTimeout = 8 * time.Second

// emitAck fires a fast, no-think worker call that produces a short
// natural acknowledgment ("On it — checking that now.") and streams
// it as a status the moment it returns. Runs as a goroutine launched
// at turn start so it overlaps round-1 planning — the orchestrator's
// first round uses thinking mode, which delays its first visible
// output by seconds; this fills that gap with a contextual ack
// instead of a bare "Thinking…".
//
// The ack call itself decides whether an ack is warranted: greetings,
// thanks, and instantly-answerable questions get "NONE" back and emit
// nothing, so conversational turns don't get a needless "On it!".
func (t *chatTurn) emitAck(ctx context.Context, userMsg string) {
	if t == nil || t.app == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, ackTimeout)
	defer cancel()
	sys := "A user just messaged an assistant that may need tools, web search, or sub-agents to answer. In ONE short, natural sentence, acknowledge you're on it (e.g. \"On it — let me look that up.\" / \"Sure, checking now.\" / \"Give me a sec to dig into that.\"). Do NOT answer the request, do NOT ask questions, do NOT name tools or agents. If the message is a greeting, a thanks, or something you'd answer instantly with no lookup, reply with exactly NONE."
	resp, err := t.app.WorkerChat(cctx,
		[]Message{{Role: "user", Content: userMsg}},
		WithSystemPrompt(sys), WithMaxTokens(30), WithThink(false),
	)
	if err != nil || resp == nil {
		return
	}
	ack := strings.TrimSpace(resp.Content)
	if ack == "" || strings.HasPrefix(strings.ToUpper(ack), "NONE") {
		return
	}
	// Strip wrapping quotes the model sometimes adds.
	ack = strings.Trim(ack, "\"'")
	t.emitStatus(ack)
}

// resolveTopic returns the turn's classified knowledge topic,
// blocking on the concurrent classify (topicCh) the first time a
// caller needs it. Subsequent calls return the cached value. The
// classify is launched in handleSend at turn start so it overlaps
// round-1 planning — by the time a worker step or knowledge tool
// reads the topic, the slug is almost always already in the
// channel, so this resolves instantly. Falls back to generalTopic
// when classification was never started or returned empty.
func (t *chatTurn) resolveTopic() string {
	t.topicOnce.Do(func() {
		if t.topicCh != nil {
			t.topic = <-t.topicCh
		}
		if t.topic == "" {
			t.topic = generalTopic
		}
	})
	return t.topic
}

// appendSearchOrderGuidance inserts a "knowledge before web" stub
// into the system prompt when the agent has both a knowledge corpus
// AND web tools enabled. Without this, most models default to
// web_search for any question they're unsure about, even when the
// agent's own corpus, skill-attached documents, or accumulated
// findings already have the answer. The stub reorders that default.
//
// Skipped when:
//   - the agent has no web tools in its catalog (no choice to reorder)
//   - the agent is Builder (its persona has its own search rhythm)
func (t *chatTurn) appendSearchOrderGuidance(sys string) string {
	if isBuilderAgent(t.agent.ID) {
		return sys
	}
	// Only fire when web tools are actually in the agent's effective
	// catalog — otherwise there's nothing to deprioritize. AllowedTools
	// empty = default pool (web tools likely present); otherwise check
	// the explicit list.
	hasWeb := len(t.agent.AllowedTools) == 0
	if !hasWeb {
		for _, name := range t.agent.AllowedTools {
			if name == "web_search" || name == "fetch_url" || name == "browse_page" {
				hasWeb = true
				break
			}
		}
	}
	if !hasWeb {
		return sys
	}
	return sys + `

## Search order — knowledge first

Before any tool call, ask: "Does this question require information that has CHANGED since my documents/findings were written?"
- **No** → call ` + "`knowledge_search`" + ` (or trust the pre-injected chunks above if they're relevant).
- **Yes** → ` + "`web_search`" + ` / ` + "`fetch_url`" + ` is justified.

Most domain questions fail the freshness test — APIs don't change daily, last year's tax rules haven't moved, our runbooks haven't been edited. Treat web as the exception, not the default.

Examples:
- "How do I check pod scheduling failures?" → knowledge_search (k8s API hasn't changed).
- "What's the latest k8s version?" → web_search (version IS the live data).

If unsure: knowledge_search first. One cheap embedding lookup beats an unnecessary web round.

**Handling multiple documents in your corpus:** knowledge_search results carry a ` + "`source_doc`" + ` field, and the pre-injected block groups chunks under "### From document: <name>" headers. When chunks from DIFFERENT documents disagree on a specific point — different API versions, conflicting policies, an older doc vs. a newer revision — DO NOT silently average or pick one. Surface the conflict explicitly: "Doc 'X' says A but Doc 'Y' says B; both are in the corpus." Let the user decide which is authoritative for their context. The corpus may legitimately contain multiple variants on the same subject; pretending they're one source erases information the user needs.`
}

// emitSkillActivations reports which skills the classifier picked +
// the confidence per skill. One activity row per skill so the user
// can see WHY each one activated — trigger phrase or embedding
// similarity — and decide whether the classifier got it right.
func (t *chatTurn) emitSkillActivations(acts []SkillActivation) {
	if len(acts) == 0 {
		return
	}
	// Short summary line first.
	names := make([]string, 0, len(acts))
	for _, a := range acts {
		names = append(names, a.Skill.Name)
	}
	t.sse.Send(map[string]any{
		"kind": "activity",
		"type": "cmd",
		"id":   activityCheapID(),
		"text": fmt.Sprintf("Skills activated (%d): %s", len(acts), strings.Join(names, ", ")),
	})
	// Per-skill detail with the classifier's reasoning.
	for _, a := range acts {
		var line string
		switch a.Reason {
		case "trigger":
			if a.Score > 0 {
				line = fmt.Sprintf("  [%s] trigger=%q gatekeeper=%.2f", a.Skill.Name, a.Trigger, a.Score)
			} else {
				// L1 trusted without gatekeeper (short message or
				// missing embedding).
				line = fmt.Sprintf("  [%s] trigger=%q (no gatekeeper — embeddings off or msg short)", a.Skill.Name, a.Trigger)
			}
		case "embedding":
			line = fmt.Sprintf("  [%s] embedding=%.2f", a.Skill.Name, a.Score)
		default:
			line = fmt.Sprintf("  [%s] %s score=%.2f", a.Skill.Name, a.Reason, a.Score)
		}
		t.sse.Send(map[string]any{
			"kind": "activity",
			"type": "output",
			"id":   activityCheapID(),
			"text": line,
		})
	}
}

// emitKnowledgeActivity reports what RAG actually pulled into the
// orchestrator prompt this turn. Lands one "cmd" row summarizing the
// chunk count + source breakdown, then one "output" row per chunk
// with a short preview. The user can scan the activity pane and see
// whether the skill's docs were the ones that surfaced.
func (t *chatTurn) emitKnowledgeActivity(hits []SearchHit) {
	if len(hits) == 0 {
		return
	}
	// Group by resolved source label so the summary reads "2 from
	// Kubernetes Helper, 1 from K8s Reference" rather than raw IDs.
	labels := make([]string, 0, len(hits))
	labelCounts := map[string]int{}
	labelOrder := []string{}
	for _, h := range hits {
		lbl := t.humanLabelForSource(h.Source)
		labels = append(labels, lbl)
		if _, seen := labelCounts[lbl]; !seen {
			labelOrder = append(labelOrder, lbl)
		}
		labelCounts[lbl]++
	}
	var parts []string
	for _, lbl := range labelOrder {
		c := labelCounts[lbl]
		if c > 1 {
			parts = append(parts, fmt.Sprintf("%s (×%d)", lbl, c))
		} else {
			parts = append(parts, lbl)
		}
	}
	summary := fmt.Sprintf("Knowledge: %d chunk%s injected — %s",
		len(hits), pluralS(len(hits)), strings.Join(parts, ", "))
	t.sse.Send(map[string]any{
		"kind": "activity",
		"type": "cmd",
		"id":   activityCheapID(),
		"text": summary,
	})
	// Per-chunk detail: source label + section + short preview. Helps
	// when the user wants to verify "did the right paragraph surface."
	for i, h := range hits {
		section := strings.TrimPrefix(h.Section, "## ")
		section = strings.TrimSpace(section)
		preview := strings.TrimSpace(h.Text)
		// Collapse whitespace + truncate. 160 chars is enough to
		// recognize the source paragraph without blowing the pane.
		preview = strings.Join(strings.Fields(preview), " ")
		if len(preview) > 160 {
			preview = preview[:160] + "…"
		}
		line := fmt.Sprintf("  [%s] %s — %s", labels[i], section, preview)
		t.sse.Send(map[string]any{
			"kind": "activity",
			"type": "output",
			"id":   activityCheapID(),
			"text": line,
		})
	}
}

// humanLabelForSource maps a SearchHit.Source string to a human label
// for the activity pane. Resolves skill IDs to skill names, collection
// IDs to collection names; falls back to the bare ID when lookup fails.
func (t *chatTurn) humanLabelForSource(src string) string {
	switch {
	case src == knowledgeSource(t.user, t.agent.ID, ""):
		return "agent corpus"
	case strings.HasPrefix(src, knowledgeSource(t.user, t.agent.ID, "")+":"):
		topic := strings.TrimPrefix(src, knowledgeSource(t.user, t.agent.ID, "")+":")
		return "agent corpus (topic: " + topic + ")"
	case strings.HasPrefix(src, "skill:"):
		id := strings.TrimPrefix(src, "skill:")
		for _, sk := range LoadSkills(t.udb, t.user) {
			if sk.ID == id {
				return "skill: " + sk.Name
			}
		}
		return "skill: " + id
	case strings.HasPrefix(src, "collection:"):
		id := strings.TrimPrefix(src, "collection:")
		if c, ok := loadCollection(t.udb, t.user, id); ok {
			return "collection: " + c.Name
		}
		return "collection: " + id
	}
	return src
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// facts loads the agent's structured key/value Explicit Memory entries
// (the always-in-prompt facts layer). Re-read every call — cheap and
// ensures store_fact mid-turn lands in subsequent worker steps.
// Gated on DisableExplicit; the framing of WHAT goes here is shaped
// by the agent's KnowledgeFraming.
func (t *chatTurn) facts() []MemoryFact {
	if t.agent.DisableExplicit {
		return nil
	}
	return ListMemoryFacts(t.udb, factsNamespace(t.agent.ID))
}

// gatedPersona returns the agent's persona prompt with any
// `<!-- @requires-tools: ... -->` sections stripped when the
// listed tools aren't in the effective tool set. Empty AllowedTools
// = no restriction (nil sentinel → strip nothing). When AllowedTools
// is explicit, the gate set is (framework always-on tools) +
// (agent's allowlist) so framework-provided things like plan_set
// and store_fact stay visible regardless of what the admin trimmed.
func (t *chatTurn) gatedPersona(prompt string) string {
	if len(t.agent.AllowedTools) == 0 {
		return StripPromptSectionsForTools(prompt, nil)
	}
	gate := append([]string{
		// Framework always-on tools — sections that depend on these
		// never get stripped regardless of admin allowlist.
		"plan_set", "plan_get", "plan_update", "plan_clear",
		"ask_user", "ask_user_form", "respond_directly",
		"knowledge_save", "knowledge_search", "get_report",
		"store_fact", "forget_fact", "list_facts",
	}, t.agent.AllowedTools...)
	return StripPromptSectionsForTools(prompt, gate)
}

// activeSkillsForTurn runs the skills classifier against the latest
// user message in this turn and returns the skills to activate.
// Cached on the chatTurn so runPlan + downstream worker steps see
// the same set — skills shouldn't drift mid-turn.
//
// Only fires once per turn. Builder is excluded — its prompt is
// stateless-and-locked, so adding skill addendums on top of its
// curated persona breaks the lockdown invariant.
//
// msgs is optional — passed from runPlan where it's available; the
// worker-step path passes nil and we fall back to t.session.Messages.
func (t *chatTurn) activeSkillsForTurn(msgs []ChatMessage) []SkillRecord {
	if t.skillsResolved {
		return t.skillsActive
	}
	t.skillsResolved = true
	if isBuilderAgent(t.agent.ID) {
		return nil
	}
	// Skill suppression — two gates, both treated as "no skills this turn":
	//   1. Agent-level DisableSkills (config) — for agents whose job is
	//      to faithfully read a specific corpus; auto-activated skills
	//      would smuggle in their own knowledge and contaminate output.
	//   2. inferredOff (per-turn Clean toggle OR DisableInferred) —
	//      skills emit derived chunks via self-training, which is
	//      exactly the kind of material Reference Memory governs.
	//      If Inferred is off, suppress the classifier too.
	if t.agent.DisableSkills || t.inferredOff() {
		return nil
	}
	if msgs == nil && t.session != nil {
		msgs = t.session.Messages
	}
	latestUserMsg := ""
	lastAssistantMsg := ""
	// Walk backward — record the most recent user message AND the most
	// recent assistant message (used as priorContext for the skill
	// classifier so follow-ups like "were you talking about X or Y?"
	// can still activate the skill that was active in the prior turn).
	for i := len(msgs) - 1; i >= 0; i-- {
		if latestUserMsg == "" && msgs[i].Role == "user" {
			latestUserMsg = msgs[i].Content
		}
		if lastAssistantMsg == "" && msgs[i].Role == "assistant" {
			lastAssistantMsg = msgs[i].Content
		}
		if latestUserMsg != "" && lastAssistantMsg != "" {
			break
		}
	}
	if latestUserMsg == "" {
		return nil
	}
	// Attachment filenames land in the message text via
	// FormatAttachmentPreamble's headers, so triggers can match
	// `.pdf` etc. against the text directly — no separate
	// attachment list needed.
	t.skillActivations = ActiveSkillsWithScores(t.udb, t.user, latestUserMsg, lastAssistantMsg, nil, t.agent.AllowedSkills)
	t.skillsActive = make([]SkillRecord, 0, len(t.skillActivations))
	for _, a := range t.skillActivations {
		t.skillsActive = append(t.skillsActive, a.Skill)
	}
	if len(t.skillActivations) > 0 {
		t.emitSkillActivations(t.skillActivations)
	}
	return t.skillsActive
}

// toolCallKey is the dedup key for the per-turn cache. JSON-marshal
// in sorted-key order via encoding/json's deterministic map output so
// semantically equal args collide regardless of how the LLM ordered
// the JSON keys. Argument-less calls still get a stable key.
func toolCallKey(name string, args map[string]any) string {
	if len(args) == 0 {
		return name + "()"
	}
	b, err := json.Marshal(args)
	if err != nil {
		// Hash failure shouldn't crash the call — just skip caching
		// by returning a unique-per-attempt key.
		return name + "?" + fmt.Sprintf("%p", &args)
	}
	return name + "|" + string(b)
}

// recordToolCall appends to the per-turn log and the dedup cache.
// Holds toolMu so concurrent tool calls (parallel workers in some
// agent loops) don't race.
func (t *chatTurn) recordToolCall(rec toolCallRecord) {
	t.toolMu.Lock()
	defer t.toolMu.Unlock()
	t.toolCalls = append(t.toolCalls, rec)
	// Only cache successful results from tools on the cacheableTools
	// allowlist — symmetric with lookupToolCache. Skipping the write
	// for non-cacheable tools also keeps the per-turn cache map
	// small (the log itself still records every call).
	if rec.Err == "" && cacheableTools[rec.Name] {
		if t.toolCache == nil {
			t.toolCache = map[string]string{}
		}
		t.toolCache[toolCallKey(rec.Name, rec.Args)] = rec.Result
	}
}

// persistedToolCalls converts the per-turn tool-call log into the
// shape persisted alongside the assistant message. Called at session
// save time so the export trace has every tool fired during the turn,
// not just the ones the live UI rendered. Returns nil for turns
// where no tools ran (the JSON tag is omitempty so the field
// vanishes in those cases).
func (t *chatTurn) persistedToolCalls() []PersistedToolCall {
	t.toolMu.Lock()
	defer t.toolMu.Unlock()
	if len(t.toolCalls) == 0 {
		return nil
	}
	out := make([]PersistedToolCall, 0, len(t.toolCalls))
	for _, rec := range t.toolCalls {
		out = append(out, PersistedToolCall{
			Name:   rec.Name,
			Args:   rec.Args,
			Result: rec.Result,
			Err:    rec.Err,
		})
	}
	return out
}

// cacheableTools is the opt-in allowlist of tools whose results are
// safe to cache for the rest of a turn. The default is DON'T cache —
// blanket caching silently returns stale data for anything mutating,
// hides intentional retries (probe an endpoint, fix, retry), and
// confuses agents like Builder that iterate against the same args.
//
// Members must be:
//   - read-only (no side effects)
//   - idempotent (same args → same result within a turn's timescale)
//   - expensive enough that the cache hit is worth it
//
// Network-fetched content (web_search, fetch_url, browse_page) hits
// all three: external API call, deterministic for a given query in
// the moment, can be slow. Knowledge search is read-only against an
// embedding index. Add to this map deliberately — when in doubt,
// leave it out.
var cacheableTools = map[string]bool{
	"web_search":       true,
	"fetch_url":        true,
	"browse_page":      true,
	"knowledge_search": true,
}

// lookupToolCache returns a cached result for (name, args) if present.
// Errors are not cached — a previously-failing call gets a second shot.
// Tools not in cacheableTools bypass the lookup so probing/iterating
// flows always run fresh.
func (t *chatTurn) lookupToolCache(name string, args map[string]any) (string, bool) {
	if !cacheableTools[name] {
		return "", false
	}
	t.toolMu.Lock()
	defer t.toolMu.Unlock()
	if t.toolCache == nil {
		return "", false
	}
	v, ok := t.toolCache[toolCallKey(name, args)]
	return v, ok
}

// toolLogPromptSection formats the per-turn log for injection into a
// subsequent step's user prompt. Empty when nothing has run yet.
//
// Each entry is clipped so the prompt doesn't explode — the LLM
// sees a snippet ("X costs $20…"); if it wants the full result it
// can re-call deliberately (the cache will short-circuit).
func (t *chatTurn) toolLogPromptSection() string {
	t.toolMu.Lock()
	defer t.toolMu.Unlock()
	if len(t.toolCalls) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Tool calls already made this turn\n")
	b.WriteString("These calls ran in earlier steps. DO NOT repeat them — the cache short-circuits anyway, but a re-call wastes a round. Use what was already found:\n\n")
	for _, rec := range t.toolCalls {
		b.WriteString("- ")
		b.WriteString(formatToolCall(rec.Name, rec.Args))
		b.WriteString("\n  ")
		if rec.Err != "" {
			b.WriteString("ERROR: ")
			b.WriteString(rec.Err)
		} else {
			b.WriteString("→ ")
			b.WriteString(snippetOneLine(rec.Result, 500))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

// snippetOneLine collapses to a single line + length-clip for the
// tool-log prompt section. Keeps the worker's context manageable.
func snippetOneLine(s string, max int) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > max {
		cut := max
		if sp := strings.LastIndexByte(s[:max], ' '); sp > max/2 {
			cut = sp
		}
		s = s[:cut] + "…"
	}
	return s
}

// handleSend drives one user turn against an agent:
//
//  1. Append the user message to the session.
//  2. Orchestrator round 1: thinking LLM with the plan_set tool.
//     Returns a list of step titles.
//  3. Emit a plan block + one activity row per step.
//  4. For each step: call the worker LLM, stream its output to the
//     activity pane, flip the step status to done.
//  5. Orchestrator synthesis round: thinking LLM composes a final
//     user-facing reply given the original question + all worker
//     outputs. Stream as chunk events into the conversation pane.
//  6. Save the session (messages + plan snapshot) and emit done.
//
// Plans auto-confirm — no operator pause between steps in Phase 1.
func (T *OrchestrateApp) handleSend(w http.ResponseWriter, r *http.Request, udb Database, user string, agent AgentRecord) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Message           string        `json:"message"`
		SessionID         string        `json:"session_id,omitempty"`
		History           []ChatMessage `json:"history,omitempty"`
		PrivateMode       bool          `json:"private_mode,omitempty"`
		InferredDisabled  bool          `json:"inferred_disabled,omitempty"`
		// Hidden marks the user message as already-shown-in-prior-bubble
		// (e.g. an ask_user card's submitted state). LLM still sees the
		// content in history; the chat panel just skips rendering a
		// duplicate user bubble.
		Hidden            bool          `json:"hidden,omitempty"`
		Images            []string      `json:"images,omitempty"` // base64-encoded image data from the chat panel's paperclip
		Documents   []struct {
			Name     string `json:"name"`
			MimeType string `json:"mime_type"`
			Data     string `json:"data"` // base64
		} `json:"documents,omitempty"` // pdf / docx / text attachments — server extracts and prepends to Message
		// IntakeValues, when set, marks this submission as the agent's
		// intake form. Persisted on the user message so re-edit on
		// replayed sessions can rehydrate the form with original values.
		IntakeValues map[string]string `json:"intake_values,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Extract attached documents (PDFs, DOCX, text files) and prepend
	// their plain text to the user message so the LLM sees the
	// content inline. Failures land as a short "[attach failed: ...]"
	// note so the user knows something didn't make it; the turn
	// continues with whatever did parse plus the user's typed text.
	if len(req.Documents) > 0 {
		var preamble strings.Builder
		// Collect extracted attachment text for optional persistence
		// into the agent's knowledge store. Gated by agent.IngestAttachments
		// AND knowledge being enabled overall (DisableKnowledge=false).
		// Skipped when the extracted text is trivially short — a tiny
		// config snippet isn't worth a vector chunk.
		const minIngestChars = 200
		type extracted struct{ name, text string }
		var ingestQueue []extracted
		for _, d := range req.Documents {
			raw, err := base64.StdEncoding.DecodeString(d.Data)
			if err != nil {
				preamble.WriteString(fmt.Sprintf("[attached %s — decode failed: %v]\n\n", d.Name, err))
				continue
			}
			text, err := ExtractDocument(r.Context(), DocumentAttachment{
				Name:     d.Name,
				MimeType: d.MimeType,
				Data:     raw,
			})
			if err != nil {
				preamble.WriteString(fmt.Sprintf("[attached %s — extraction failed: %v]\n\n", d.Name, err))
				continue
			}
			preamble.WriteString(FormatAttachmentPreamble(d.Name, d.MimeType, text))
			if agent.IngestAttachments && len(strings.TrimSpace(text)) >= minIngestChars {
				ingestQueue = append(ingestQueue, extracted{name: d.Name, text: text})
			}
		}
		if preamble.Len() > 0 {
			req.Message = preamble.String() + req.Message
		}
		// Background ingest — runs outside the reply path so the user
		// doesn't wait on embedding latency. Each attachment gets its
		// own chunk (filename as subject) under the agent's knowledge
		// store, topic="attachments". Later turns retrieve them via
		// knowledge_search like any other ingested content.
		if len(ingestQueue) > 0 {
			go func(items []extracted, user, agentID string) {
				for _, it := range items {
					ctx, cancel := context.WithTimeout(context.Background(), knowledgeIngestTimeout)
					ingestAgentKnowledge(ctx, T.DB, user, agentID, "attachments", it.name, it.text)
					cancel()
					Log("[orchestrate.attachments] ingested %q (%d chars) into agent=%s knowledge[attachments]", it.name, len(it.text), agentID)
				}
			}(ingestQueue, user, agent.ID)
		}
	}
	// Validate AFTER extraction so attachment-only sends (e.g. "here's
	// a resume, analyze it" with only a PDF and no typed text) are
	// accepted as long as SOMETHING came in. The message can be empty
	// only when at least one image is attached (vision tier picks it
	// up via Message.Images) — pure documents already wrote their
	// preamble into req.Message above, so an empty message past this
	// point means nothing was attached at all.
	if strings.TrimSpace(req.Message) == "" && len(req.Images) == 0 {
		http.Error(w, "message or attachment required", http.StatusBadRequest)
		return
	}
	// Attachment-only turns get a synthetic message so the LLM has
	// something to react to. For image-only turns the vision LLM
	// sees the images directly; we just need a non-empty content
	// field for the message-role/Content contract.
	if strings.TrimSpace(req.Message) == "" {
		if len(req.Images) > 0 {
			req.Message = "[attached image — please analyze]"
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	if _, ok := w.(http.Flusher); !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	sse := newSSEWriter(w)

	// Resolve session (create on first turn, otherwise load).
	sess, _ := loadChatSession(udb, agent.ID, req.SessionID)
	isNewSession := sess.ID == ""
	if isNewSession {
		sess = ChatSession{
			AgentID: agent.ID,
			Title:      titleFromFirstMessage(req.Message),
			Created:    time.Now(),
		}
		var err error
		sess, err = saveChatSession(udb, sess)
		if err != nil {
			sse.Send(map[string]any{"kind": "error", "text": "save session: " + err.Error()})
			return
		}
	}
	sse.Send(map[string]any{"kind": "session", "id": sess.ID})

	ctx, cancel := context.WithCancel(r.Context())
	inflightCancels.Store(sess.ID, cancel)
	defer func() {
		cancel()
		inflightCancels.Delete(sess.ID)
	}()
	// Surface any panic in the turn pipeline through the SSE stream
	// AND the gohort log. The http server has its own panic recovery
	// that just tears down the connection; without this, a silent
	// panic in runPlan / runWorkerStep / synthesis looks like "no
	// reply" to the user and shows no clue in the log.
	defer func() {
		if rec := recover(); rec != nil {
			Log("[orchestrate /api/send] PANIC recovered: %v", rec)
			sse.Send(map[string]any{"kind": "error", "text": fmt.Sprintf("server panic: %v", rec)})
		}
	}()

	// Append the user message to the persisted thread now; failures
	// downstream still leave the user's input visible on reload.
	userMsg := ChatMessage{Role: "user", Content: req.Message, Created: time.Now(), Hidden: req.Hidden}
	if len(req.IntakeValues) > 0 {
		userMsg.IntakeValues = req.IntakeValues
	}
	sess.Messages = append(sess.Messages, userMsg)
	if saved, err := saveChatSession(udb, sess); err == nil {
		sess = saved
	}

	if T.LLM == nil {
		sse.Send(map[string]any{"kind": "error", "text": "worker LLM not configured"})
		return
	}

	// Register the per-session interjection queue so /api/inject can
	// find one. Released at the end of the turn so a subsequent
	// inject hits "session not interjectable" rather than landing
	// notes nobody will drain.
	//
	// Before release, drain any notes still queued — they arrived
	// after the last in-turn drain point (during synthesis, during
	// the directReply path, etc.) and would otherwise be destroyed.
	// Drained notes are appended as user messages on the session so
	// they survive into the NEXT turn as part of normal history.
	// Defers run LIFO, so this fires before release.
	queue := registerInjectionQueue(sess.ID, user, agent.ID)
	defer releaseInjectionQueue(sess.ID)
	defer func() {
		if queue == nil {
			return
		}
		leftover := queue.Drain()
		if len(leftover) == 0 {
			return
		}
		udb := UserDB(T.DB, user)
		if udb == nil {
			return
		}
		// Reload session so we don't clobber any other writes that
		// landed during the turn (the turn struct's snapshot may be
		// stale by this defer's firing time).
		latest, ok := loadChatSession(udb, agent.ID, sess.ID)
		if !ok {
			return
		}
		now := time.Now()
		for _, n := range leftover {
			latest.Messages = append(latest.Messages, ChatMessage{
				Role: "user", Content: n.Text, Created: now,
			})
		}
		_, _ = saveChatSession(udb, latest)
		Log("[orchestrate.inject] preserved %d leftover note(s) into session %s for next turn", len(leftover), sess.ID)
	}()

	// ForcePrivate on the agent record locks Private mode ON
	// regardless of what the user toggled. Composes with the
	// per-turn user toggle via OR — either source can force the
	// network connector to refuse.
	privateMode := req.PrivateMode || agent.ForcePrivate
	// Build ONE connector per turn and share it across ctx,
	// sess.Network, and the inflight registry. Sharing one
	// instance is what makes the mid-turn cutoff work: when the
	// privacy endpoint flips this connector via SetAllowed, every
	// in-flight tool re-checking Allowed() sees the new state.
	turnConnector := NewNetworkConnector(privateMode)
	ctx = WithNetworkConnector(ctx, turnConnector)
	inflightConnectors.Store(sess.ID, turnConnector)
	defer inflightConnectors.Delete(sess.ID)
	// Also lock ForcePrivate agents so the privacy endpoint can't
	// flip them OFF — the agent-level lock is the source of truth.
	if agent.ForcePrivate {
		// no-op marker; SetAllowed(true) from the endpoint will be
		// reverted by the next Allowed() check if we add an
		// auto-revert guard. Simpler for now: trust the endpoint
		// to refuse turning private OFF when ForcePrivate is set
		// (endpoint reads the agent record).
		_ = agent.ForcePrivate
	}

	turn := &chatTurn{
		app:              T,
		ctx:              ctx,
		sse:              sse,
		udb:              udb,
		user:             user,
		agent:            agent,
		queue:            queue,
		session:          &sess,
		privateMode:      privateMode,
		network:          turnConnector,
		inferredDisabled: req.InferredDisabled,
		isNewSession:     isNewSession,
		userImages:       decodeUserImages(req.Images),
		// Flag the turn as having fresh external content if the user
		// attached any document. The consolidation loop-break gate
		// reads this to decide whether to ingest the synthesis.
		userDocsThisTurn: len(req.Documents) > 0,
	}
	// Reset the session's "awaiting user confirm" flag — the two-turn
	// gate that read this is removed. Kept here to clear stale state
	// from older sessions that may have set it; if THIS turn ends with
	// ask_user again the dispatch path re-sets it as needed.
	sess.AwaitingUserConfirm = false
	// Lift the authoring-in-progress slot from its side-table onto the
	// in-memory ChatSession so chatTurn-bound tool handlers (notably
	// create_pipeline_tool) can read it without their own DB lookup.
	// Side-table because the global create_agent handler can write
	// without knowing the per-agent session-storage shape.
	if a := loadAuthoringInProgress(udb, sess.ID); a != "" {
		sess.AuthoringAgentID = a
	}

	// Note: orchestrate does NOT auto-promote follow-ups into a prior
	// sub-agent. Dispatch (agents(run)) is synchronous and one-shot —
	// its reply is a tool result the orchestrator synthesizes from.
	// Continued conversation with a sub-agent happens the normal way:
	// the user keeps chatting (unlimited turns, full session history)
	// and the LLM re-calls agents(run) when it wants to delegate again
	// — which re-threads the same sub-session by its deterministic ID.
	// Auto-promotion was removed: it routed worker-mode dispatches as
	// conversational replies (producing empty bubbles) and was
	// redundant with just chatting. Injection/promotion remain a
	// phantom concern, where async dispatch makes them meaningful.

	// Classify the turn's topic CONCURRENTLY with round-1 planning
	// rather than blocking before it. The classify is a short worker
	// call (~20 tokens) but on a cold prompt cache it added a visible
	// stall before the user saw any response. Launching it into
	// topicCh lets round 1 start immediately; resolveTopic() blocks
	// on the result only when a worker step or knowledge tool first
	// needs the slug, by which point the classify has almost always
	// finished. Falls back to generalTopic on error.
	turn.topicCh = make(chan string, 1)
	go func(msg string) {
		turn.topicCh <- classifyTopicForQuestion(ctx, T, T.DB, user, agent.ID, msg)
	}(req.Message)

	// Fire a fast acknowledgment concurrently so the user sees a
	// contextual "On it…" while round-1 planning (thinking mode)
	// produces its first real output. Skipped automatically for
	// greetings / trivial asks (the ack call returns NONE). Promotion
	// turns already emit their own "Routing follow-up…" status and
	// returned above, so this only runs on main-LLM turns.
	go turn.emitAck(ctx, req.Message)

	// --- Orchestrator round 1: respond directly, plan, or ask ---
	turn.emitStatus("Thinking…")
	steps, question, directReply, planErr := turn.runPlan(sess.Messages)
	if planErr != nil {
		sse.Send(map[string]any{"kind": "error", "text": "plan: " + planErr.Error()})
		return
	}
	if directReply != "" {
		// runPlan already emitted the bubble (streamed during the
		// agent loop) + message_done + stats. Just persist + run
		// background consolidation + title generation.
		turn.emitStatus("Direct response.")
		sess.Messages = append(sess.Messages, ChatMessage{
			Role: "assistant", Content: directReply,
			Created: time.Now(), Usage: turn.drainLastUsage(),
		})
		_, _ = saveChatSession(udb, sess)
		turn.consolidate(req.Message, nil, directReply)
		turn.titleAfterFirstTurn()
		sse.Send(map[string]any{"kind": "done"})
		return
	}
	if question != "" {
		// runPlan already emitted the question bubble. Just persist
		// + end the turn — the user's next message will re-enter the
		// plan round with the answer in context.
		turn.emitStatus("Orchestrator asked for clarification.")
		sess.Messages = append(sess.Messages, ChatMessage{
			Role: "assistant", Content: question,
			Created: time.Now(), Usage: turn.drainLastUsage(),
		})
		_, _ = saveChatSession(udb, sess)
		turn.titleAfterFirstTurn()
		sse.Send(map[string]any{"kind": "done"})
		return
	}
	if len(steps) == 0 {
		// Degenerate: orchestrator chose neither to plan nor to ask.
		// Run a single pseudo-step "Respond" so the flow still
		// produces a worker output and a synthesis reply.
		turn.emitStatus("No plan needed — direct response.")
		steps = []PlanStep{{ID: 1, Title: "Respond directly", Status: StepPending}}
	} else {
		turn.emitStatus(fmt.Sprintf("Plan committed — %d step%s.", len(steps), plural(len(steps))))
	}

	// Plan and per-step status all render as a single in-chat block;
	// the runner re-emits the same block id on each transition and the
	// dispatcher routes the repeat into the renderer's onUpdate.
	roundIdx := len(sess.Plans)
	blockID := planBlockID(sess.ID, roundIdx)
	emitPlanBlock(sse, blockID, steps)

	// --- Per-step worker execution ---
	for i := range steps {
		select {
		case <-ctx.Done():
			sse.Send(map[string]any{"kind": "error", "text": "cancelled"})
			return
		default:
		}
		steps[i].Status = StepInProgress
		emitPlanBlock(sse, blockID, steps)
		// Servitor-style intent narration block lands in the
		// conversation pane before the worker fires so the user
		// sees "▸ Investigating: Step N: <title> — <intent>"
		// inline. Activity-pane status row stays too for the
		// right-pane audit trail.
		emitIntentBlock(sse, fmt.Sprintf("%s-intent-%d", blockID, steps[i].ID), steps[i])
		turn.emitStatus(fmt.Sprintf("▸ Step %d/%d: %s", steps[i].ID, len(steps), steps[i].Title))

		// Pull anything the user has queued since the prior step (or
		// since the plan was set). drainNotes persists them as user
		// ChatMessages and emits notes_consumed so client bubbles
		// re-style as "agent saw your note".
		stepNotes := turn.drainNotes()
		if n := len(stepNotes); n > 0 {
			turn.emitStatus(fmt.Sprintf("Picked up %d user note%s mid-flight.", n, plural(n)))
		}
		out, err := turn.runWorkerStep(steps[:i], steps[i], req.Message, stepNotes)
		if err != nil {
			steps[i].Status = StepBlocked
			steps[i].BlockedReason = err.Error()
			steps[i].Output = "error: " + err.Error()
			emitPlanBlock(sse, blockID, steps)
			turn.emitStatus(fmt.Sprintf("✗ Step %d/%d failed: %s", steps[i].ID, len(steps), truncate(err.Error(), 120)))
			continue
		}
		steps[i].Status = StepDone
		steps[i].Output = out
		steps[i].Findings = deriveFindings(out)
		emitPlanBlock(sse, blockID, steps)
		// Completion status — show what was actually accomplished
		// instead of leaving the user with the pre-step "▸ Step N:
		// <title>" line. Uses the same derived findings the plan
		// card shows; if findings are empty (worker returned
		// nothing meaningful), fall back to the title.
		summary := steps[i].Findings
		if summary == "" {
			summary = steps[i].Title
		}
		turn.emitStatus(fmt.Sprintf("✓ Step %d/%d: %s", steps[i].ID, len(steps), truncate(summary, 160)))
	}

	// --- Post-plan gap detection (opt-in) ---
	// Research-flavored agents set GapCheck=true so the runner scans
	// the worker outputs for structural failure modes (abstract
	// claims, evidence asymmetry, mechanism gaps) and runs targeted
	// fills before synthesis. Chat-flavored agents leave it off —
	// gap detection is a cost the conversational surface doesn't
	// need to pay every turn.
	if agent.GapCheck && len(steps) > 0 {
		turn.emitStatus("Checking for gaps…")
		gaps := turn.runGapCheck(req.Message, steps, len(steps)+1)
		if len(gaps) > 0 {
			turn.emitStatus(fmt.Sprintf("Found %d gap%s — filling…", len(gaps), plural(len(gaps))))
			// Append the new steps onto the plan and re-emit the
			// block so the user sees them join the existing card.
			steps = append(steps, gaps...)
			emitPlanBlock(sse, blockID, steps)
			for i := len(steps) - len(gaps); i < len(steps); i++ {
				select {
				case <-ctx.Done():
					sse.Send(map[string]any{"kind": "error", "text": "cancelled"})
					return
				default:
				}
				steps[i].Status = StepInProgress
				emitPlanBlock(sse, blockID, steps)
				emitIntentBlock(sse, fmt.Sprintf("%s-intent-%d", blockID, steps[i].ID), steps[i])
				turn.emitStatus(fmt.Sprintf("▸ Gap %d/%d: %s", i-(len(steps)-len(gaps))+1, len(gaps), steps[i].Title))
				stepNotes := turn.drainNotes()
				out, err := turn.runWorkerStep(steps[:i], steps[i], req.Message, stepNotes)
				gapPos := i - (len(steps) - len(gaps)) + 1
				if err != nil {
					steps[i].Status = StepBlocked
					steps[i].BlockedReason = err.Error()
					steps[i].Output = "error: " + err.Error()
					emitPlanBlock(sse, blockID, steps)
					turn.emitStatus(fmt.Sprintf("✗ Gap %d/%d failed: %s", gapPos, len(gaps), truncate(err.Error(), 120)))
					continue
				}
				steps[i].Status = StepDone
				steps[i].Output = out
				steps[i].Findings = deriveFindings(out)
				emitPlanBlock(sse, blockID, steps)
				summary := steps[i].Findings
				if summary == "" {
					summary = steps[i].Title
				}
				turn.emitStatus(fmt.Sprintf("✓ Gap %d/%d: %s", gapPos, len(gaps), truncate(summary, 160)))
			}
		} else {
			turn.emitStatus("No gaps detected.")
		}
	}

	// --- Orchestrator synthesis round ---
	synthNotes := turn.drainNotes()
	if n := len(synthNotes); n > 0 {
		turn.emitStatus(fmt.Sprintf("Picked up %d user note%s before synthesis.", n, plural(n)))
	}
	turn.emitStatus("Composing reply…")
	reply, synthErr := turn.runSynthesis(req.Message, steps, synthNotes)
	if synthErr != nil {
		sse.Send(map[string]any{"kind": "error", "text": "synthesis: " + synthErr.Error()})
		return
	}

	// Persist the orchestrator's final reply + the plan snapshot +
	// the full tool-call trace for this turn (orchestrator rounds +
	// worker steps). The trace makes session export useful for
	// debugging and dataset capture without needing live SSE.
	turnToolCalls := turn.persistedToolCalls()
	sess.Messages = append(sess.Messages, ChatMessage{
		Role: "assistant", Content: reply,
		Created: time.Now(), Usage: turn.drainLastUsage(),
		ToolCalls: turnToolCalls,
	})
	sess.Plans = append(sess.Plans, PlanSnapshot{
		RoundIndex: len(sess.Plans),
		Steps:      steps,
	})
	_, _ = saveChatSession(udb, sess)

	// Hallucinated-authoring detection. The pattern we keep seeing
	// on smaller models: the assistant replies "I've created the
	// agent with tools X, Y, Z…" without ever firing add_tool /
	// create_agent this turn. Catch it by cross-checking three
	// signals — (1) build plan exists with pending steps, (2) zero
	// authoring tools fired this turn, (3) the reply has non-empty
	// text. If all three: surface a visible warning AND inject a
	// corrective note into the session so the next turn's history
	// shows the LLM exactly what it claimed vs what actually fired.
	if injectAuthoringMismatchWarning(&sess, turnToolCalls, reply) {
		_, _ = saveChatSession(udb, sess)
		sse.Send(map[string]any{
			"kind": "activity",
			"type": "error",
			"id":   activityCheapID(),
			"text": "Build plan still has pending steps but no authoring tool fired this turn — the reply may be describing work that didn't actually happen. Next turn will be re-prompted.",
		})
	}

	// Memory consolidation runs in the background so it doesn't
	// extend the user's perceived latency. Failures inside are
	// logged but never bubbled; memory is an optimization, not part
	// of the reply contract.
	turn.consolidate(req.Message, steps, reply)
	turn.titleAfterFirstTurn()

	sse.Send(map[string]any{"kind": "done"})
}

// agentAuthoringToolNames is the set of tools whose successful
// invocation actually advances the build plan. Used by the
// hallucinated-authoring check below to decide whether a turn that
// closed without firing any of these "really" did the work it
// claimed in its text reply.
var agentAuthoringToolNames = map[string]bool{
	"create_agent": true,
	"update_agent": true,
	"clone_agent":  true,
	"delete_agent": true,
	"add_tool":     true,
}

// injectAuthoringMismatchWarning detects the "claimed but didn't fire"
// pattern and, on a match, appends a synthetic user-role message to
// sess.Messages that the next turn's toLLMMessages will surface to
// the LLM as a corrective system note. Returns true when a warning
// was injected so the caller can flush the session save and emit a
// visible SSE warning.
func injectAuthoringMismatchWarning(sess *ChatSession, turnToolCalls []PersistedToolCall, reply string) bool {
	if sess == nil || sess.BuildPlan == nil {
		return false
	}
	pending := 0
	for _, s := range sess.BuildPlan.Steps {
		if s.Status != "done" && s.Status != "blocked" {
			pending++
		}
	}
	if pending == 0 {
		return false
	}
	if strings.TrimSpace(reply) == "" {
		return false
	}
	for _, tc := range turnToolCalls {
		if agentAuthoringToolNames[tc.Name] {
			return false
		}
	}
	note := "FRAMEWORK NOTICE: your previous reply described authoring work but no add_tool / create_agent / update_agent call fired during that turn. The build plan still has " +
		fmt.Sprintf("%d pending step", pending)
	if pending != 1 {
		note += "s"
	}
	note += ". Do not describe work you haven't done. Re-do the next pending step by ACTUALLY calling the right authoring tool with concrete arguments. One tool call per turn — that is the only path that advances the plan."
	sess.Messages = append(sess.Messages, ChatMessage{
		Role:    "user",
		Content: note,
		Created: time.Now(),
		Hidden:  true,
	})
	return true
}

// handleCancel aborts an in-flight runner by session ID. The runner
// goroutine cleans up on ctx.Done.
func (T *OrchestrateApp) handleCancel(w http.ResponseWriter, r *http.Request, agent AgentRecord) {
	var body struct {
		SessionID string `json:"session_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.SessionID != "" {
		if v, ok := inflightCancels.Load(body.SessionID); ok {
			if cancel, ok := v.(context.CancelFunc); ok {
				cancel()
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- LLM rounds -------------------------------------------------------------

// runPlan asks the orchestrator (thinking LLM) to decide its next
// move. The orchestrator picks ONE of three tools:
//
//   - respond_directly(text): reply to the user without spinning up
//     a worker or synthesis call. For chitchat, acknowledgements,
//     and questions the orchestrator can answer from its own
//     knowledge with confidence. One LLM round total.
//   - plan_set(steps): commit to a plan; runner executes each step.
//   - ask_user(question): pause and ask the user for clarification
//     before planning. The runner short-circuits, surfaces the
//     question as an assistant message, and waits for the user's
//     next turn before re-entering the planning round.
//
// Return contract: (steps, question, directReply, err). At most one
// of (steps, question, directReply) is non-empty.
//   - directReply non-empty → emit as the reply, end the round.
//   - steps non-empty → proceed with the plan.
//   - question non-empty → relay to user, end the round.
//   - all empty → orchestrator chose no tool. Caller substitutes a
//     one-step "Respond" plan so the flow still produces a reply.
func (t *chatTurn) runPlan(msgs []ChatMessage) (steps []PlanStep, question, directReply string, err error) {
	// Assembly order matters — LLMs weight more-recent prompt
	// content heavier. We want the agent's persona to be the most
	// authoritative directive, so the universal "How this round
	// works" framework block goes BEFORE the persona, not after.
	// Agents with detailed phased personas (Builder) skip the
	// universal block entirely — their persona governs the rhythm.
	persona := t.gatedPersona(t.agent.OrchestratorPrompt)
	if !isBuilderAgent(t.agent.ID) {
		persona = roundShapePreamble(resolveMaxPlanSteps(t.agent)) + persona
	}
	sys := prependAgentContext(persona, t.agent, t.facts())
	if g := strings.TrimSpace(t.agent.PlanGuidance); g != "" {
		sys += "\n\n## Plan guidance\n" + g
	}
	// Activate any skills the user's message triggers. Skills append
	// their instructions to the system prompt and union their
	// declared tools into the catalog for this turn only. Capped at
	// 3 active skills via core's classifier.
	sys = t.appendActiveSkills(sys, msgs)
	// "Available skills" block — lists skills the user has authored
	// that didn't auto-activate this turn, so the LLM can recognize a
	// context-implied match and call activate_skill(name) to pull
	// one in. No-op when DisableSkills is set.
	sys += t.renderAvailableSkillsBlock()
	// "Available agents" block — lists the user's OTHER agents so
	// the host LLM knows what specialists exist and can dispatch to
	// them via agents(action="run", agent=..., message=...). Without
	// this block the LLM has to call agents(action="list") to
	// discover them, which it almost never does speculatively.
	sys += t.renderAvailableAgentsBlock()
	// ("Available knowledge collections" block unmounted alongside
	// dispatch_to_worker — there's no LLM-facing tool that
	// references collections by ID right now, so advertising them
	// would only confuse the LLM. Collections still surface via
	// agent.AttachedCollections → knowledge_search and skill.AttachedCollections → knowledge_search.)
	// Append a "search order" stub when the agent has both a knowledge
	// store AND web tools in its catalog. Without it, many models
	// default to web_search for any question they're unsure about,
	// even when the agent's own corpus has the answer. The stub
	// reorders that default: knowledge first, web only as fallback.
	sys = t.appendSearchOrderGuidance(sys)
	// (Authoring-in-progress banner removed. It referenced legacy
	// tool names (create_pipeline_tool / create_temp_tool /
	// create_api_tool) replaced by tool_def + add_tool, and the
	// underlying focus slot is meaningful only inside Builder's
	// dispatched sub-sessions where the persona's Phase 4 plan_set
	// rhythm already manages the focus implicitly. Non-Builder
	// agents can't author anyway. Dead state — kept the
	// AuthoringAgentID DB field for now in case a future surface
	// wants it; just don't surface it as prompt content.)
	maxSteps := resolveMaxPlanSteps(t.agent)
	stepBudget := fmt.Sprintf("up to %d step%s", maxSteps, plural(maxSteps))

	// Control tools — terminal. Each handler captures its args
	// onto local closure state and cancels the orchestrator's
	// child context to stop the agent loop after the current
	// round. The captured state is dispatched after the loop
	// returns.
	var (
		capturedSteps     []PlanStep
		capturedQuest     string
		capturedOptions   []string
		capturedMulti     bool
		capturedFormSteps []map[string]any
		capturedReply     string
	)
	orchCtx, cancelOrch := context.WithCancel(t.ctx)
	defer cancelOrch()

	planTool := AgentToolDef{
		Tool: Tool{
			Name:        "plan_set",
			Description: fmt.Sprintf("Commit to a multi-step plan for this turn. Each step is {title, intent, worker_brief}; the framework spins up a fresh focused worker LLM per step. Use ONLY when the turn genuinely needs decomposition (research with multiple branches, layered analysis, complex multi-tool workflows). For single-tool turns CALL THE TOOL DIRECTLY (you have web_search, fetch_url, calculate, etc.) instead of going through plan_set — it's much faster. **MINIMUM 2 STEPS** — a 1-step plan is wasteful (planner + worker + synthesis for what would be one inline call); the handler rejects it. Budget: %s.", stepBudget),
			Parameters: map[string]ToolParam{
				"steps": {
					Type:        "array",
					Description: fmt.Sprintf("Ordered list of steps, %s. Each step is an object with title, intent, and worker_brief. The brief is the worker LLM's system prompt for that step.", stepBudget),
					Items: &ToolParam{
						Type: "object",
						Properties: map[string]ToolParam{
							"title":        {Type: "string", Description: "Short step name (1 line)."},
							"intent":       {Type: "string", Description: "One-sentence statement of what this step is looking for or aims to produce. Shown to the user BEFORE the worker runs."},
							"worker_brief": {Type: "string", Description: "The SYSTEM PROMPT the worker LLM gets for this step. 2-5 sentences. Be specific about what to produce, output format, and what to avoid. The framework auto-injects available tools + agent rules; you don't need to repeat them. **Always end the brief with: \"Lead your final response with ONE concrete sentence summarizing what you accomplished — that line surfaces on the plan card as the step's outcome. Then write the details underneath.\"** Workers that lead with process narration (\"I searched for X...\") leave the user with a misleading status line; leading with the outcome (\"Found 3 candidate Foundation cast updates from Apple TV's October press release.\") makes the plan card honest. NOT visible to the user."},
							"tools": {
								Type:        "array",
								Description: "Explicit tool surface for THIS step's worker — tight list of tool names the worker actually needs to complete the brief. Pick from the tools currently in YOUR catalog (the worker can't access tools you don't see). Be tight: 2-5 names is typical; one tool is common when the step is a focused lookup. Framework essentials (knowledge_search, memory_*, agents, plan_set, ask_user, respond_directly, find_tools) are always available to the worker — don't list them. If you're unsure which tools the worker needs, list none and the worker gets the agent's default pool (broader catalog, more LLM cognitive load).",
								Items:       &ToolParam{Type: "string"},
							},
						},
						Required: []string{"title", "worker_brief"},
					},
				},
			},
			Required: []string{"steps"},
		},
		Handler: func(args map[string]any) (string, error) {
			steps := parsePlanSteps(args["steps"], maxSteps)
			// Reject single-step plans — they're wasteful (full
			// plan→worker→synthesis pipeline for what should be
			// one inline tool call + reply). Returning an error
			// hands feedback to the LLM; the agent loop continues
			// and the model gets another chance to either issue
			// the inline tool call directly or author a real
			// multi-step plan.
			if len(steps) < 2 {
				return "", fmt.Errorf("plan_set requires at least 2 steps (got %d). For a single tool call, invoke the tool directly inline — plan_set's planner+worker+synthesis overhead is only worth it for genuinely multi-step work", len(steps))
			}
			// Reject vacuous plans — steps whose intent is "just
			// respond" or "summarize and reply" with no actual work.
			// These show up when the LLM wraps a respond_directly in
			// plan_set ceremony instead of calling respond_directly
			// inline. The whole point of plan_set is to gather info
			// across multiple workers; if every step is a synthesis
			// step, the plan adds no value.
			if vacuous := looksLikeVacuousPlan(steps); vacuous != "" {
				return "", fmt.Errorf("plan_set rejected: %s. For a turn that needs no tool work, call respond_directly inline with the answer text. plan_set is for genuinely multi-step research / decomposition", vacuous)
			}
			capturedSteps = steps
			cancelOrch()
			return fmt.Sprintf("Plan committed (%d step%s); the worker pipeline will now execute it.", len(capturedSteps), plural(len(capturedSteps))), nil
		},
	}
	askTool := AgentToolDef{
		Tool: Tool{
			Name:        "ask_user",
			Description: "Pause and ask the user a clarifying question. Use whenever GUESSING is the alternative — not when SEARCHING is. Call ask_user when: (a) a tool returned 2+ plausible matches and you'd be picking arbitrarily (\"there are 3 users named Sam — which one?\"), (b) the user must choose between meaningfully different approaches (\"PDF or HTML?\"), (c) they must supply personal info (which appliance, which file, their preference), or (d) the request is genuinely ambiguous beyond what a search could resolve. DON'T ask for things you could just look up — call the right tool instead. When the answer has known choices (the matches you found, output formats, scope, mode), pass them as `options` so the user gets a checkbox / radio UI plus a free-text field; otherwise leave options empty for a plain text reply. For multi-step builds (Builder agent), pass `plan` to paint a visible checklist card above the question — each item flips to ✓ as later turns mark steps done.",
			Parameters: map[string]ToolParam{
				"question": {
					Type:        "string",
					Description: "Singular. The question to ask the user, in plain text. Field name is 'question' (NOT 'questions'). If you need to ask multiple things, you can include several questions in this one string, but multi-step clarifications work better via ask_user_form.",
				},
				"options": {
					Type:        "array",
					Description: "Optional pre-filled choices the user can pick from. MUST be an array of STRINGS, each being one option label. Concrete example: options=[\"yes\", \"edit\", \"no\"] or options=[\"PDF\", \"HTML\", \"Markdown\"]. NOT a count, NOT a number, NOT a JSON-encoded string. When provided, the UI renders checkboxes (or radios when multi=false) plus a free-text field. Keep labels short (1-4 words each), 8 max.",
					Items:       &ToolParam{Type: "string"},
				},
				"multi": {
					Type:        "boolean",
					Description: "When true (and options is non-empty), the user can select multiple options. When false, single-select (radios). Default false. Ignored when options is empty.",
				},
				"plan": {
					Type:        "array",
					Description: "Optional ordered list of build-plan steps rendered as a visible checklist above the question. MUST be an array of OBJECTS, each with a 'title' (one-line summary) and optional 'detail' (tool + args summary). NOT a count, NOT a list of numbers, NOT a string. Concrete example: plan=[{\"title\": \"Create agent shell\", \"detail\": \"create_agent\"}, {\"title\": \"Add search tool\", \"detail\": \"add_tool(mode=api)\"}, {\"title\": \"Verify by dispatch\", \"detail\": \"test call\"}, {\"title\": \"Report completion\"}]. When this is set, the framework paints an orchestrate_plan card; subsequent authoring-tool calls auto-update individual rows to ✓.",
					Items: &ToolParam{
						Type: "object",
						Properties: map[string]ToolParam{
							"title":  {Type: "string", Description: "Required. One-line step title shown in the checklist."},
							"detail": {Type: "string", Description: "Optional one-line detail under the title (tool name + key args)."},
						},
						Required: []string{"title"},
					},
				},
			},
			Required: []string{"question"},
		},
		Handler: func(args map[string]any) (string, error) {
			capturedQuest = strings.TrimSpace(stringArg(args, "question"))
			// Defensive: smaller LLMs occasionally typo "questions"
			// (plural) for "question". Accept either so the call
			// doesn't lose its primary content.
			if capturedQuest == "" {
				capturedQuest = strings.TrimSpace(stringArg(args, "questions"))
			}
			capturedOptions = stringSliceFromArgs(args, "options")
			if v, ok := args["multi"].(bool); ok {
				capturedMulti = v
			}
			// If plan is provided, set up the build-plan state + emit
			// the orchestrate_plan SSE block before the ask card lands.
			// Lets Builder make ONE tool call at Phase 2 end instead of
			// two (present_build_plan + ask_user) — the failure mode
			// where Builder skipped present_build_plan and then
			// mark_step_done called against nothing.
			if raw, ok := args["plan"]; ok && raw != nil {
				if planSteps := buildPlanStepsFromArg(raw); len(planSteps) > 0 && t.session != nil {
					t.session.BuildPlan = &BuildPlanState{
						ID:    "build-plan-" + t.session.ID,
						Steps: planSteps,
					}
					emitBuildPlanBlock(t.sse, t.session.BuildPlan)
				}
			}
			cancelOrch()
			return "Question relayed to the user; the framework will wait for their reply.", nil
		},
	}
	replyTool := AgentToolDef{
		Tool: Tool{
			Name:        "respond_directly",
			Description: "Reply to the user with the given text. The text you pass IS the final reply — equivalent to just stopping and writing the reply as your message content. Provided as a tool so you can be explicit when you want to terminate the turn with a specific text.",
			Parameters: map[string]ToolParam{
				"text": {
					Type:        "string",
					Description: "The final reply to the user, in plain markdown. This is what the user will see verbatim.",
				},
			},
			Required: []string{"text"},
		},
		Handler: func(args map[string]any) (string, error) {
			capturedReply = strings.TrimSpace(stringArg(args, "text"))
			cancelOrch()
			return "Reply relayed to the user.", nil
		},
	}
	formTool := AgentToolDef{
		Tool: Tool{
			Name:        "ask_user_form",
			Description: "Pause and ask the user a SEQUENCE of clarifying questions, one at a time, with clickable answers. Use when you need multiple pieces of info to proceed and each has its own discrete choices — e.g. language + deployment target + timeline. PREFER this over a single ask_user with a numbered list inside the question text; the form gives the user clickable step-through UI instead of forcing them to type \"1. Go 2. AWS 3. one week\". For a single question, use ask_user instead.",
			Parameters: map[string]ToolParam{
				"steps": {
					Type:        "array",
					Description: "Ordered list of questions to walk the user through. Each step is {question, options?, multi?}. Keep questions short; 2-5 steps total is the sweet spot.",
					Items: &ToolParam{
						Type: "object",
						Properties: map[string]ToolParam{
							"question": {Type: "string", Description: "The question text shown for this step."},
							"options": {
								Type:        "array",
								Description: "Optional clickable choices for this step. When provided the user sees checkboxes (multi) or radios (single); always paired with a free-text input for custom answers.",
								Items:       &ToolParam{Type: "string"},
							},
							"multi": {Type: "boolean", Description: "When true (and options is non-empty), multiple options can be picked for this step. Default false."},
						},
						Required: []string{"question"},
					},
				},
			},
			Required: []string{"steps"},
		},
		Handler: func(args map[string]any) (string, error) {
			raw, _ := args["steps"]
			var steps []map[string]any
			switch v := raw.(type) {
			case []any:
				for _, x := range v {
					if m, ok := x.(map[string]any); ok {
						steps = append(steps, normalizeFormStep(m))
					}
				}
			case []map[string]any:
				for _, m := range v {
					steps = append(steps, normalizeFormStep(m))
				}
			}
			capturedFormSteps = steps
			cancelOrch()
			return fmt.Sprintf("Form relayed to the user (%d step%s); the framework will wait for their reply.", len(steps), plural(len(steps))), nil
		},
	}

	// Worker tools — same surface the worker step gets. Lets the
	// orchestrator handle single-tool turns inline (call web_search,
	// see result, reply) without spinning up plan_set + a worker
	// round + synthesis for what should be one round.
	Debug("[orchestrate.orch] runPlan: building tool session")
	sess := t.newToolSession()
	Debug("[orchestrate.orch] runPlan: resolving worker tools")
	workerTools, workerNames, err := t.resolveWorkerTools(sess)
	if err != nil {
		Debug("[orchestrate.orch] runPlan: resolveWorkerTools error: %v", err)
		return nil, "", "", fmt.Errorf("resolve tools: %w", err)
	}
	Debug("[orchestrate.orch] runPlan: resolved %d worker tools", len(workerTools))
	// Classifier-driven trim. When the agent has no explicit
	// AllowedTools (default pool), shrink the catalog to the top-K
	// most-relevant tools for the user's latest message so the LLM
	// isn't drowning in 40+ options. Skipped for agents with an
	// explicit allowlist (those tools were picked on purpose — all
	// of them stay) and for noTools-sentinel agents (nothing to
	// trim). The framework essentials (control / knowledge / memory
	// tools added below as knowTools) bypass this filter entirely;
	// they're always-on regardless of message content.
	// Classifier-trim fires whenever the worker pool is above the
	// per-turn budget, regardless of whether the agent has an
	// explicit AllowedTools list or uses the default pool. The
	// agent's curation defines the UNIVERSE of tools available;
	// the classifier picks which subset to surface per turn based
	// on relevance to the user's latest message. The noTools
	// sentinel still short-circuits (nothing to trim).
	if !isNoToolsSentinel(t.agent.AllowedTools) && len(workerTools) > classifierKeepBudget {
		before := len(workerTools)
		workerTools, workerNames = classifierTrimTools(t.ctx, msgs, workerTools, workerNames)
		Log("[orchestrate.orch] classifier-trim: %d worker tools → %d (agent=%s)", before, len(workerTools), t.agent.ID)
	}
	workerTools, workerNames = filterToolAuthoringWithoutFocus(workerTools, workerNames, t.session)
	t.gateAgentCRUDTools(workerTools)
	t.wrapToolsForActivity(sess, workerTools)

	// Wrap control tools too so they emit cmd rows in the activity
	// pane (transparency: user sees "plan_set was called" / "ask_user
	// was called" alongside the rest of the orchestrator's tool use).
	// Control tools have no attachment surface — pass nil sess.
	controlTools := []AgentToolDef{planTool, askTool, formTool, replyTool}
	t.wrapToolsForActivity(nil, controlTools)
	// Three orthogonal layers; each gates its own tool group:
	//
	//   - Knowledge (uploaded files, read-only): knowledge_search —
	//     always available; the layer is harmless when the corpus is
	//     empty.
	//
	//   - Explicit Memory (always-in-prompt facts): store_fact +
	//     forget_fact → gated by explicitOff (DisableExplicit). The
	//     framing of WHAT goes in here is shaped by MemoryMode
	//     (agent = lessons; chatbot = user-personalization + notes).
	//
	//   - Reference Memory (vector-grown derived chunks): memory_save +
	//     memory_search + memory_forget → gated by inferredOff
	//     (DisableInferred OR per-turn Clean toggle).
	knowTools := []AgentToolDef{t.searchKnowledgeToolDef()}
	// find_tools — always-on classifier-fallback. The LLM uses this
	// to discover tools that didn't surface from the per-turn vector
	// pre-selection (mid-conversation pivot, unusual phrasing,
	// specialty tools the classifier missed). Tagged IsFrameworkTool
	// at the source, which excludes it from the worker pool builder;
	// explicit knowTools injection is the right path for always-on
	// framework infrastructure.
	if ct, ok := LookupChatTool("find_tools"); ok {
		knowTools = append(knowTools, ChatToolToAgentToolDef(ct))
	}
	// activate_skill — manual override for the classifier when context
	// implies a skill match that triggers/embedding missed. Gated on
	// DisableSkills (no tool when the agent has skills off).
	if !t.agent.DisableSkills {
		knowTools = append(knowTools, t.activateSkillToolDef())
	}
	// (dispatch_to_worker temporarily unmounted — the LLM wasn't
	// reaching for it reliably and the surface area was diluting
	// agent dispatch. Skills still auto-activate inline via the
	// classifier; cross-agent work uses agents(action="run", ...).
	// Re-mount here when the dispatch path's discoverability is
	// figured out.)
	if !t.inferredOff() {
		knowTools = append(knowTools,
			t.saveMemoryToolDef(),
			t.searchMemoryToolDef(),
			t.forgetMemoryToolDef())
	}
	if !t.explicitOff() {
		knowTools = append(knowTools,
			t.storeFactToolDef(), t.forgetFactToolDef(),
		)
	}
	// Build-plan UI — present_build_plan paints the visible
	// checklist when Builder reaches end of Phase 2; mark_step_done
	// updates it as each Phase 4 tool call completes. Builder-only
	// — these tools only make sense in the structured authoring
	// rhythm Builder follows. Other agents (Chat, Research, etc.)
	// don't need a build-plan card. Reduces their tool surface
	// without touching Builder's authoring flow.
	if isBuilderAgent(t.agent.ID) {
		knowTools = append(knowTools,
			t.presentBuildPlanToolDef(),
			t.markStepDoneToolDef(),
		)
	}
	knowTools = append(knowTools,
		// agents (list / get / run) — single entry point for agent
		// operations. Replaces the legacy trio (list_agents,
		// get_agent, dispatch_to_agent) for new code. The legacy
		// tools stay registered for backward compat with agent
		// records that explicitly name them in AllowedTools.
		t.agentsGroupedToolDef(),
		// Recurring tasks — background work that posts back into this
		// session at a fixed interval. Mirror of chat's schedule_chat_update
		// flow, scoped per-(user, agent).
		t.scheduleRecurringToolDef(), t.listRecurringToolDef(), t.cancelRecurringToolDef(),
		// Explorer mode — LLM-triggered round-budget lift for
		// API-mapping tasks. No-op unless AllowExplorer is set on
		// the agent.
		t.enterExplorerModeToolDef(),
	)
	// create_pipeline_tool is NOT added to the catalog — add_tool with
	// mode="pipeline" covers the same use case via a unified surface.
	// Having both visible caused pattern-match loops (LLM oscillated
	// between the two). The handler stays in the codebase for
	// backward-compat with any future agent that explicitly opts in
	// via AllowedTools, but the closure-bound default registration is
	// removed.
	t.wrapToolsForActivity(sess, knowTools)
	// Persistent temp tools also flow into the static set so the
	// rewriter can collapse them when they're members of an admin-
	// curated group. Otherwise vapi-style user-defined tools sit at
	// the top of the catalog despite being grouped, and the LLM
	// picks them at random instead of going through the toolbox's
	// expand_tool_group path. Snapshot the names so DynamicTools
	// below knows to skip them (and only surface NEW temp tools
	// created mid-turn).
	Debug("[orchestrate.orch] runPlan: building temp tool defs")
	staticTempTools := temptool.BuildAgentToolDefs(sess)
	Debug("[orchestrate.orch] runPlan: wrapping %d temp tools", len(staticTempTools))
	t.wrapToolsForActivity(sess, staticTempTools)
	t.staticTempToolNames = make(map[string]bool, len(staticTempTools))
	for _, td := range staticTempTools {
		t.staticTempToolNames[td.Tool.Name] = true
	}
	allTools := append(controlTools, knowTools...)
	allTools = append(allTools, workerTools...)
	allTools = append(allTools, staticTempTools...)
	// Private-mode backstop for dynamically built AgentToolDefs (the
	// `agents` grouped tool, temp tools, source-hooked tools, etc.).
	// resolveWorkerTools only filters tools that exist in the global
	// ChatTool registry; per-turn AgentToolDefs are constructed here
	// and never registered, so they slip past that filter even when
	// they declare CapNetwork in Tool.Caps. Without this pass, a
	// Private turn could still dispatch into a sub-agent (via `agents`)
	// whose own tools call the network — leaking the turn. Drop any
	// AgentToolDef whose declared Caps include CapNetwork.
	if t.privateMode {
		filtered := allTools[:0]
		dropped := []string{}
		for _, td := range allTools {
			hasNet := false
			for _, c := range td.Tool.Caps {
				if c == CapNetwork {
					hasNet = true
					break
				}
			}
			if hasNet {
				dropped = append(dropped, td.Tool.Name)
				continue
			}
			filtered = append(filtered, td)
		}
		allTools = filtered
		if len(dropped) > 0 {
			Log("[orchestrate.orch] private mode dropped %d network-capable dynamic tool(s): %v", len(dropped), dropped)
		}
	}
	Debug("[orchestrate.orch] runPlan: rewriting catalog over %d tools", len(allTools))

	// Collapse admin-curated tool groups: any member tool in the
	// current set gets replaced by a synthetic group entry the LLM
	// (Runtime tool-group rewriting retired. Vector pre-selection
	// in classifierTrimTools already shrinks the worker-tool surface
	// by relevance per turn; the static-catalog group placeholders
	// and the expand_tool_group meta-tool were adding a parallel
	// concept without earning their keep. find_tools is the LLM's
	// explicit-search fallback when the classifier missed.
	// ToolGroup records still exist as admin-side organizational
	// metadata — they just don't gate the runtime catalog anymore.)

	// Full-surface log every turn — confirms what the LLM actually
	// receives. If a follow-up turn lacks a tool the first turn had,
	// this line will show the regression. Includes both control and
	// worker tools so a bug that drops one but not the other is
	// visible immediately.
	allNames := make([]string, 0, len(allTools))
	for _, td := range allTools {
		allNames = append(allNames, td.Tool.Name)
	}
	sessID := ""
	if t.session != nil {
		sessID = t.session.ID
	}
	Log("[orchestrate.orch] session=%s msgs=%d tools_to_llm[%d]=%v (worker_subset=%v private=%v)",
		sessID, len(msgs), len(allTools), allNames, workerNames, t.privateMode)

	if len(workerTools) > 0 {
		sys += "\n\n" + buildToolUseDirective(workerTools)
	}
	// Append per-tool prompt fragments (opt-in via AgentToolDef.Prompt).
	// Lands between the framework directive and any subsequent
	// dynamic additions — close to the catalog so the model reads
	// per-tool usage notes alongside the tool list.
	if frag := RenderToolPromptFragments(allTools); frag != "" {
		sys += "\n\n" + frag
	}

	stopKeepalive := startKeepalive(t.sse)
	think := true
	if p := RouteThink("app.orchestrate.orchestrator"); p != nil {
		think = *p
	}

	// Per-round bubbles: each agent-loop round that streams text gets
	// its own assistant bubble, finalized at the round boundary by
	// the OnStep callback. Subsequent rounds create fresh bubbles —
	// the chat-app pattern where the user sees discrete narration
	// chunks rather than one accumulating wall of text.
	//
	// Tool-only rounds (no streamed content) don't materialize a
	// bubble. We track whether the most-recently-finalized round
	// actually had streamed content so the post-loop captured-text
	// dispatch can avoid emitting a duplicate when the LLM streamed
	// the same text it then passed to respond_directly/ask_user.
	var streamMsgID string
	var streamedBuf strings.Builder
	var lastFinalizedID string
	var lastRoundHadContent bool
	streamHandler := func(chunk string) {
		if chunk == "" {
			return
		}
		// If a tool fired this round before any text streamed, it
		// already lazy-materialized a bubble via ensureBubbleForTool.
		// Adopt that bubble for the streamed text rather than
		// creating a second one.
		if streamMsgID == "" {
			if existing := t.getCurrentMsgID(); existing != "" {
				streamMsgID = existing
			} else {
				streamMsgID = fmt.Sprintf("orch-%d", time.Now().UnixNano())
				t.sse.Send(map[string]any{
					"kind": "message",
					"role": "assistant",
					"id":   streamMsgID,
					"text": "",
				})
				t.setCurrentMsgID(streamMsgID)
			}
		}
		streamedBuf.WriteString(chunk)
		t.sse.Send(map[string]any{
			"kind": "chunk",
			"id":   streamMsgID,
			"text": chunk,
		})
	}
	onStepHandler := func(info StepInfo) {
		// Tool-only round with no text and no lazy-bubble: nothing
		// to finalize, nothing visible. (Tool calls in that round
		// already created their own bubble via ensureBubbleForTool;
		// streamMsgID picks that up via getCurrentMsgID below.)
		id := streamMsgID
		if id == "" {
			id = t.getCurrentMsgID()
		}
		if id == "" {
			lastRoundHadContent = false
			return
		}
		// Models on llama.cpp sometimes emit <tool_call><function=…>
		// XML as TEXT instead of native tool_calls — the agent loop
		// catches it and dispatches via ParseTextToolCall, but the
		// markup already streamed to the user's bubble. Strip on
		// round close so what they're left looking at is clean
		// narration, not raw XML.
		raw := streamedBuf.String()
		cleaned := strings.TrimSpace(StripToolCallMarkup(raw))
		if cleaned != strings.TrimSpace(raw) {
			t.sse.Send(map[string]any{
				"kind": "chunk_replace",
				"id":   id,
				"text": cleaned,
			})
		}
		// Tool-only round (no streamed text): keep the bubble open
		// so subsequent rounds' tool calls fold into the same pill
		// instead of materializing a fresh bubble per round. Once a
		// round produces actual text, finalize as usual — that text
		// is the orchestrator's narration and deserves its own card.
		if cleaned == "" {
			lastRoundHadContent = false
			streamedBuf.Reset()
			return
		}
		t.sse.Send(map[string]any{"kind": "message_done", "id": id})
		lastFinalizedID = id
		lastRoundHadContent = true
		streamMsgID = ""
		streamedBuf.Reset()
		t.setCurrentMsgID("")
	}

	// Budget injection — surface the round counter to the LLM so it
	// can pace itself instead of calling tools until it hits the cap
	// blind. OnRoundStart fires AFTER history is appended but BEFORE
	// the LLM call, so each round sees the same brief note appended
	// to history.
	maxR := resolveMaxWorkerRounds(t.agent)
	roundCounter := 0
	onRoundStartHandler := func() []Message {
		roundCounter++
		remaining := maxR - roundCounter
		if remaining <= 0 {
			return []Message{{
				Role: "user",
				Content: fmt.Sprintf(
					"[Round %d/%d — FINAL round. No more tool calls. Produce your final answer NOW from what you have so far.]",
					roundCounter, maxR,
				),
			}}
		}
		return []Message{{
			Role: "user",
			Content: fmt.Sprintf(
				"[Round %d/%d — %d round%s left before this turn ends. Pace accordingly: if the answer needs more searches than that, use plan_set instead of iterating inline.]",
				roundCounter, maxR, remaining, plural(remaining),
			),
		}}
	}

	// Attach any image attachments to the most recent user message
	// so vision-capable LLMs see them. Images are ephemeral — only
	// passed for this turn; not persisted in session history (the
	// raw bytes would balloon the DB).
	llmMsgs := toLLMMessages(msgs)
	if len(t.userImages) > 0 {
		for i := len(llmMsgs) - 1; i >= 0; i-- {
			if llmMsgs[i].Role == "user" {
				llmMsgs[i].Images = t.userImages
				break
			}
		}
	}

	// Pre-plan retrieval — search the Knowledge layer (uploaded files)
	// AND, unless Inferred is off, the Reference Memory layer (derived
	// chunks) against the current user message. Knowledge is always
	// on; only Inferred is gated. Empty corpus returns empty hits and
	// the inject block is omitted.
	{
		if userMsg := lastUserContent(llmMsgs); userMsg != "" {
			searchCtx, cancelSearch := context.WithTimeout(t.ctx, knowledgeIngestTimeout)
			// Wider net when skills are active — they bring extra
			// sources (self-training + attached collections) and
			// surfacing more chunks raises the chance the LLM sees
			// its answer in-context instead of reaching for web.
			autoInjectK := 4
			if len(t.skillsActive) > 0 {
				autoInjectK = 6
			}
			// Auto-inject pulls CURATED chunks only (uploads,
			// attached collections, deployment-knowledge). Derived
			// chunks from Reference Memory are NOT auto-injected
			// anymore — that was the session-bleed source ("thank
			// you" surfacing prior Kiteworks findings, etc.). The
			// LLM can still recall its own prior findings by
			// explicitly calling memory_search; pull-only beats
			// push-injected for fuzzy/derived material.
			scope := ChunkScopeCuratedOnly
			hits := searchAgentKnowledge(searchCtx, t.app.DB, t.user, t.agent.ID, t.resolveTopic(), userMsg, autoInjectK, t.skillsActive, t.agent.AttachedCollections, scope)
			cancelSearch()
			// Min-similarity floor — vector search returns top-K
			// regardless of score, so even a weak match (a chunk from
			// a prior session that's only tangentially related) gets
			// surfaced. That's the "the LLM thought I was running a
			// query from an earlier query" failure mode: low-score
			// chunks pull the LLM toward old context. Filter to
			// chunks scoring above autoInjectMinScore so weak matches
			// stay out of the prompt.
			filtered := hits[:0]
			for _, h := range hits {
				if h.Score >= autoInjectMinScore {
					filtered = append(filtered, h)
				}
			}
			hits = filtered
			if section := renderKnowledgePromptSection(hits); section != "" {
				for i := len(llmMsgs) - 1; i >= 0; i-- {
					if llmMsgs[i].Role == "user" {
						llmMsgs[i].Content = section + llmMsgs[i].Content
						break
					}
				}
				Log("[orchestrate.knowledge] injected %d hit(s) into orchestrator prompt for agent=%s",
					len(hits), t.agent.ID)
				// Activity-pane observability: tell the user WHAT got
				// pulled into RAG this turn so "did the skill use the
				// docs?" is answerable from the UI, not just the logs.
				t.emitKnowledgeActivity(hits)
			}
		}
	}

	orchStart := time.Now()
	Debug("[orchestrate.orch] entering RunAgentLoop (msgs=%d tools=%d sys_chars=%d)", len(llmMsgs), len(allTools), len(sys))
	resp, _, loopErr := t.app.RunAgentLoop(orchCtx, llmMsgs, AgentLoopConfig{
		SystemPrompt: sys,
		Tools:        allTools,
		DynamicTools: t.dynamicNewTempTools(sess),
		Stream:       streamHandler,
		OnStep:       onStepHandler,
		OnRoundStart: onRoundStartHandler,
		// Cap orchestrator iterations at the agent's worker-rounds
		// budget. Most chat-style turns need 1-3 rounds; deep
		// research bumps this naturally via the agent config.
		MaxRounds: maxR,
		// Auto-confirm any NeedsConfirm tool (delete_agent) so the
		// orchestrator can act without a stdin prompt hanging the
		// stream. Higher-level approval gates would live at the
		// app layer, not in the loop.
		Confirm: func(name, args string) bool { return true },
		// Control tools end the round immediately. If the LLM bundles
		// ask_user with create_agent in the same response, only ask_user
		// fires and the turn pauses for the user's actual answer.
		RoundAbortTools: []string{"ask_user", "ask_user_form", "respond_directly", "plan_set"},
		// (No SingleFireGroups for image/video producers anymore.
		// Under the old auto-attach architecture, multiple find_image
		// calls in one batch produced multiple unintended attachments
		// — the group was the structural fix. With the new write-
		// to-workspace + explicit workspace(attach) flow, multi-fire
		// of producers is intentional: the user CAN ask for "three
		// ducks" and the LLM produces three workspace files + three
		// attach calls. The architecture itself enforces deliberate
		// per-file delivery now.)
		ChatOptions: []ChatOption{
			WithRouteKey("app.orchestrate.orchestrator"),
			WithThink(think),
		},
	})
	stopKeepalive()
	{
		respLen := 0
		if resp != nil {
			respLen = len(strings.TrimSpace(resp.Content))
		}
		errStr := ""
		if loopErr != nil {
			errStr = loopErr.Error()
		}
		Debug("[orchestrate.orch] RunAgentLoop returned (elapsed=%s, resp.content=%dch, ctx.err=%v, orchCtx.err=%v, loopErr=%q, capturedSteps=%d, capturedReply=%dch, capturedQuest=%dch, capturedForm=%d)",
			time.Since(orchStart),
			respLen,
			t.ctx.Err(), orchCtx.Err(), errStr,
			len(capturedSteps),
			len(capturedReply),
			len(capturedQuest),
			len(capturedFormSteps),
		)
	}

	// Catch a final round whose OnStep didn't fire (rare — happens
	// when the loop terminates between content stream and the OnStep
	// dispatch). Finalize and treat it as the last finalized bubble.
	finalID := streamMsgID
	if finalID == "" {
		finalID = t.getCurrentMsgID()
	}
	if finalID != "" {
		t.sse.Send(map[string]any{"kind": "message_done", "id": finalID})
		lastFinalizedID = finalID
		lastRoundHadContent = streamedBuf.Len() > 0
		streamMsgID = ""
		t.setCurrentMsgID("")
	}
	// Stats land on the last bubble we finalized; RunAgentLoop only
	// returns the last round's resp, so per-round stats aren't
	// available without backend changes.
	if lastFinalizedID != "" {
		t.emitStats(lastFinalizedID, resp, orchStart)
	}

	// emitCapturedAsBubble produces a new bubble for captured
	// control-tool text (respond_directly's text, ask_user's
	// question). Skips when the last round already streamed
	// content — assumes the LLM's streamed text matches the tool
	// arg (the common case) so we don't double up. When the last
	// round was tool-only with no narration, this is the only
	// bubble the user gets.
	emitCapturedAsBubble := func(text string) {
		if text == "" || lastRoundHadContent {
			return
		}
		id := fmt.Sprintf("orch-%d", time.Now().UnixNano())
		t.sse.Send(map[string]any{
			"kind": "message",
			"role": "assistant",
			"id":   id,
			"text": text,
		})
		t.sse.Send(map[string]any{"kind": "message_done", "id": id})
		t.emitStats(id, resp, orchStart)
	}

	// Loop end conditions:
	//   1. A control tool fired → orchCtx is canceled (but t.ctx
	//      is still live). Captured state below drives dispatch.
	//   2. Loop hit MaxRounds → resp may carry content; treat as
	//      implicit respond_directly.
	//   3. Loop ended naturally (no tool call this round) → resp.Content
	//      is the implicit reply.
	//   4. Real error (network, LLM, etc.) → return it.
	if loopErr != nil && t.ctx.Err() == nil && orchCtx.Err() == nil {
		return nil, "", "", loopErr
	}

	if len(capturedFormSteps) > 0 {
		// Multi-step form — render as a single block with all the
		// steps; client walks the user through them one at a time
		// and submits a compiled answer at the end. Return a brief
		// placeholder as the captured question so the caller's
		// session-persistence logic has something to record.
		t.sse.Send(map[string]any{
			"kind":  "block",
			"type":  "orchestrate_ask_form",
			"id":    fmt.Sprintf("askform-%d", time.Now().UnixNano()),
			"steps": capturedFormSteps,
		})
		if t.session != nil {
			t.session.AwaitingUserConfirm = true
		}
		return nil, fmt.Sprintf("(form with %d question%s)", len(capturedFormSteps), plural(len(capturedFormSteps))), "", nil
	}
	if capturedQuest != "" {
		// ask_user ALWAYS renders as a form card — question + (optional
		// option group) + textarea + Submit. The renderer handles the
		// no-options case by just showing the textarea. Always-form
		// gives the user a consistent affordance to reply explicitly,
		// instead of having to click into the chat input on some
		// questions and an inline form on others.
		t.sse.Send(map[string]any{
			"kind":     "block",
			"type":     "orchestrate_ask",
			"id":       fmt.Sprintf("ask-%d", time.Now().UnixNano()),
			"question": capturedQuest,
			"options":  capturedOptions,
			"multi":    capturedMulti,
		})
		// Mark this session as awaiting user confirmation. Gated tools
		// (agent CRUD) will fire on the NEXT user turn after this
		// pause — without this flag, those tools refuse to run.
		if t.session != nil {
			t.session.AwaitingUserConfirm = true
		}
		return nil, capturedQuest, "", nil
	}
	if len(capturedSteps) > 0 {
		// Plan_set's pre-amble narration already streamed; the plan
		// card will render below. Nothing further to emit here.
		return capturedSteps, "", "", nil
	}
	if capturedReply != "" {
		emitCapturedAsBubble(capturedReply)
		return nil, "", capturedReply, nil
	}
	if resp != nil && strings.TrimSpace(resp.Content) != "" {
		// Implicit respond_directly path — the LLM streamed text in
		// its final round but didn't call any control tool. The
		// per-round finalizer already finalized that bubble; we
		// just need a clean copy for the persisted history.
		clean := strings.TrimSpace(StripToolCallMarkup(resp.Content))
		return nil, "", clean, nil
	}
	return nil, "", "", nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// runWorkerStep dispatches one plan step to the worker (no-think) LLM.
// Prior step outputs are included as context so later steps can build
// on earlier ones. The worker's tool surface is the default pool (every
// non-blocked chat tool with Read or Network capability) intersected
// with agent.AllowedTools when the agent set an explicit allowlist;
// empty AllowedTools means "default pool".
//
// Each tool call the worker makes surfaces as an activity row in the
// AgentLoopPanel pane so the user can see what the agent is doing
// live, without waiting for the step to finish.
func (t *chatTurn) runWorkerStep(prior []PlanStep, cur PlanStep, userMsg string, notes []injectionNote) (string, error) {
	// Reset the active-bubble id at each worker-step boundary so the
	// first tool call of THIS step materializes its own card under
	// the just-emitted intent block, instead of attaching to the
	// previous step's bubble. (currentMsgID lingers from the
	// orchestrator's last round / prior step otherwise.)
	t.setCurrentMsgID("")

	// Build the worker's per-call session — same helper the
	// orchestrator uses, so persistent temp tools land on both paths.
	sess := t.newToolSession()

	tools, toolNames, err := t.resolveWorkerTools(sess)
	if err != nil {
		return "", fmt.Errorf("resolve tools: %w", err)
	}
	// Orchestrator-curated per-step tool surface — when the plan
	// step's Tools field is set, intersect the agent's resolved pool
	// down to JUST those names. The worker sees exactly what the
	// orchestrator picked, no surplus, no classifier-trim noise.
	// Empty Tools falls back to the full resolved pool (the
	// orchestrator chose not to specify; degraded behavior).
	// Intersection (not raw substitution) keeps the agent's allowlist
	// + cap filters honored — the orchestrator can't grant a tool
	// the agent doesn't have.
	if len(cur.Tools) > 0 {
		want := make(map[string]bool, len(cur.Tools))
		for _, n := range cur.Tools {
			want[strings.TrimSpace(n)] = true
		}
		keptTools := tools[:0]
		keptNames := toolNames[:0]
		for i, t := range tools {
			if want[t.Tool.Name] {
				keptTools = append(keptTools, t)
				if i < len(toolNames) {
					keptNames = append(keptNames, toolNames[i])
				}
			}
		}
		Log("[orchestrate.worker] step %d (%q): orchestrator picked %d tools → %d resolved",
			cur.ID, cur.Title, len(cur.Tools), len(keptTools))
		tools = keptTools
		toolNames = keptNames
	}
	tools, toolNames = filterToolAuthoringWithoutFocus(tools, toolNames, t.session)
	t.gateAgentCRUDTools(tools)
	// Three layers, each gated by its own helper — same split as
	// the orchestrator catalog in runPlan. Workers are the ones
	// actually researching, so they get write access to the Inferred
	// Memory layer when it's enabled. Knowledge is always available.
	tools = append(tools, t.searchKnowledgeToolDef())
	toolNames = append(toolNames, "knowledge_search")
	if !t.agent.DisableSkills {
		tools = append(tools, t.activateSkillToolDef())
		toolNames = append(toolNames, "activate_skill")
	}
	// (dispatch_to_worker temporarily unmounted on the worker step
	// too — same reason as the orchestrator catalog: discoverability
	// problem, not a wiring problem.)
	if !t.inferredOff() {
		tools = append(tools,
			t.saveMemoryToolDef(),
			t.searchMemoryToolDef(),
			t.forgetMemoryToolDef())
		toolNames = append(toolNames, "memory_save", "memory_search", "memory_forget")
	}
	if !t.explicitOff() {
		tools = append(tools, t.storeFactToolDef(), t.forgetFactToolDef())
	}
	// create_pipeline_tool intentionally NOT added — add_tool with
	// mode="pipeline" is the single pipeline-authoring surface.
	// See the matching note in runPlan above.
	tools = append(tools, t.agentsGroupedToolDef())
	tools = append(tools, t.scheduleRecurringToolDef(), t.listRecurringToolDef(), t.cancelRecurringToolDef())
	tools = append(tools, t.enterExplorerModeToolDef())
	// Include persistent temp tools in the static set so the group
	// rewriter (called below) can collapse them when they're members
	// of an admin-curated group. Without this, temp tools come in
	// via DynamicTools after the rewriter has already run and stay
	// visible at the top level even when grouped — the LLM then sees
	// both the toolbox AND the individual temp tools and picks
	// non-deterministically. Matches the runPlan fix.
	staticTempTools := temptool.BuildAgentToolDefs(sess)
	tools = append(tools, staticTempTools...)
	if t.staticTempToolNames == nil {
		t.staticTempToolNames = map[string]bool{}
	}
	for _, td := range staticTempTools {
		toolNames = append(toolNames, td.Tool.Name)
		t.staticTempToolNames[td.Tool.Name] = true
	}
	// Private-mode backstop for worker-step catalog — same pattern as
	// runPlan. Drops any AgentToolDef that declares CapNetwork in its
	// Tool.Caps (the `agents` grouped tool, any temp/source-hook tool
	// appended above with a network cap). Without this, a Private
	// orchestrator turn could plan a step, then the worker step gets
	// `agents` + dispatch its way to a network-capable sub-agent.
	if t.privateMode {
		filtered := tools[:0]
		filteredNames := toolNames[:0]
		nameSet := map[string]bool{}
		dropped := []string{}
		for _, td := range tools {
			hasNet := false
			for _, c := range td.Tool.Caps {
				if c == CapNetwork {
					hasNet = true
					break
				}
			}
			if hasNet {
				dropped = append(dropped, td.Tool.Name)
				continue
			}
			filtered = append(filtered, td)
			nameSet[td.Tool.Name] = true
		}
		for _, n := range toolNames {
			if nameSet[n] {
				filteredNames = append(filteredNames, n)
			}
		}
		tools = filtered
		toolNames = filteredNames
		if len(dropped) > 0 {
			Log("[orchestrate.tools] step %d private mode dropped %d network-capable tool(s): %v",
				cur.ID, len(dropped), dropped)
		}
	}
	Log("[orchestrate.tools] step %d resolved %d tools: %v",
		cur.ID, len(tools), toolNames)

	var ctxBlock strings.Builder
	if priorTurn := t.priorAssistantContext(); priorTurn != "" {
		ctxBlock.WriteString(priorTurn)
	}
	if len(prior) > 0 {
		ctxBlock.WriteString("## Earlier steps in this plan\n\n")
		for _, p := range prior {
			fmt.Fprintf(&ctxBlock, "### Step %d: %s\n%s\n\n", p.ID, p.Title, strings.TrimSpace(p.Output))
		}
	}
	stepUser := fmt.Sprintf(
		"## Original user request\n%s\n\n%s%s%s## Your task (step %d)\n%s",
		userMsg,
		notesContextBlock(notes),
		t.toolLogPromptSection(),
		ctxBlock.String(),
		cur.ID,
		cur.Title,
	)

	// Stream the worker's content into nothing visible — the runner
	// emits the final step output via the plan block in the chat
	// pane. Capturing chunks here gives a non-empty fallback when
	// the LLM responds in reasoning-only.
	var fullOut strings.Builder
	stream := func(chunk string) { fullOut.WriteString(chunk) }

	t.wrapToolsForActivity(sess, tools)

	// Collapse admin-curated tool groups. Workers may have a totally
	// different tool surface than the orchestrator (each step
	// resolves its own AllowedTools), so each step needs its own
	// (Runtime tool-group rewriting retired — see the matching note
	// in runPlan. Vector pre-selection on the orchestrator's side
	// handles catalog-saturation; worker steps run on whatever
	// surface the orchestrator picked. find_tools is still the
	// LLM's explicit-search fallback.)

	// Compose the worker's system prompt:
	//   1. The orchestrator's per-step brief (cur.WorkerBrief), or a
	//      minimal fallback derived from title + intent when the
	//      orchestrator didn't author one.
	//   2. Rules + memory prepended via the standard agent context
	//      helper (so the worker honors the agent's policy + prior
	//      learning even though it has no user-authored persona).
	//   3. The framework-side tool-use directive when tools are
	//      present, so the worker prefers tool calls over fabricating
	//      from training for time-sensitive or specific information.
	brief := strings.TrimSpace(cur.WorkerBrief)
	if brief == "" {
		brief = fmt.Sprintf(
			"Execute this step: %s. %s\n\nBe direct, factual, no preamble.",
			cur.Title,
			strings.TrimSpace(cur.Intent),
		)
	}
	sysPrompt := prependAgentContext(t.gatedPersona(brief), t.agent, t.facts())
	if len(tools) > 0 {
		sysPrompt += "\n\n" + buildToolUseDirective(tools)
	}
	if frag := RenderToolPromptFragments(tools); frag != "" {
		sysPrompt += "\n\n" + frag
	}

	stopKeepalive := startKeepalive(t.sse)
	f := false
	// Reset explorer state per worker step so a step starts at the
	// soft cap; the LLM must opt back into explorer mode if it
	// still needs more rounds.
	t.explorerMode = false
	t.explorerReason = ""
	softCap := resolveMaxWorkerRounds(t.agent)
	hardCap := softCap
	if t.agent.AllowExplorer && softCap < explorerHardCap {
		hardCap = explorerHardCap
	}
	roundsUsed := 0
	resp, _, err := t.app.RunAgentLoop(t.ctx, []Message{{Role: "user", Content: stepUser}}, AgentLoopConfig{
		SystemPrompt: sysPrompt,
		Tools:        tools,
		DynamicTools: t.dynamicNewTempTools(sess),
		MaxRounds:    hardCap,
		Stream:       stream,
		// Soft-cap enforcement for explorer-mode agents: pass hardCap
		// as MaxRounds upfront, then stop early at softCap UNLESS the
		// LLM has flipped explorerMode via enter_explorer_mode. For
		// non-explorer agents softCap == hardCap so StopRound never
		// fires until both are exhausted.
		StopRound: func() bool {
			roundsUsed++
			if t.explorerMode {
				return false
			}
			return roundsUsed > softCap
		},
		// Auto-confirm any NeedsConfirm tool that survives the cap
		// filter. Without this RunAgentLoop falls back to a stdin
		// y/n prompt that hangs the HTTP request indefinitely —
		// gohort runs as a service, nobody is reading stdin.
		Confirm: func(name, args string) bool { return true },
		// (No SingleFireGroups for image/video producers — same
		// rationale as the orchestrator round above. Multi-fire is
		// intentional under the write-to-workspace + workspace(attach)
		// architecture.)
		ChatOptions: []ChatOption{
			WithRouteKey("app.orchestrate.worker"),
			WithThink(f),
		},
	})
	stopKeepalive()
	if err != nil {
		return "", err
	}
	out := fullOut.String()
	if out == "" && resp != nil {
		out = resp.Content
	}
	// Defensive markup strip — models sometimes emit prompt-style
	// <tool_call><function=...>...</function></tool_call> markup IN
	// their text content (especially when emitting multiple calls in
	// one response). The agent loop only dispatches the first parsed
	// call and strips its markup from history, but a parse failure
	// or extra unparsed blocks leak markup into the step output.
	// Always sweep before returning so the user never sees raw XML.
	out = StripToolCallMarkup(out)
	out = strings.TrimSpace(out)
	if out == "" {
		out = "(no output)"
	}
	// Finalize the per-step bubble (if a tool created one via
	// ensureBubbleForTool) so it sheds the streaming class and any
	// app-side decorators fire. Idempotent — no-op when no bubble
	// was created this step.
	if id := t.getCurrentMsgID(); id != "" {
		t.sse.Send(map[string]any{"kind": "message_done", "id": id})
		t.setCurrentMsgID("")
	}
	return out, nil
}

// runSynthesis has the orchestrator compose the final user-facing
// reply given the original message + every worker step output. Streamed
// to the user via SSE chunk events as it generates.
func (t *chatTurn) runSynthesis(userMsg string, steps []PlanStep, notes []injectionNote) (string, error) {
	// Build the LLM message array as full conversation history with
	// the synthesis-flavored content REPLACING the latest user turn
	// (handleSend already appended the user's current message; we
	// strip it and re-add a beefed-up version that folds in worker
	// findings + mid-flight notes). This gives the synthesizer the
	// same multi-turn continuity the orchestrator gets, so follow-
	// ups like "tell me more about that" land with the right
	// referent instead of being synthesized in isolation.
	var msgs []Message
	if t.session != nil {
		hist := t.session.Messages
		if n := len(hist); n > 0 && hist[n-1].Role == "user" {
			hist = hist[:n-1]
		}
		msgs = toLLMMessages(hist)
	}

	var body strings.Builder
	body.WriteString(userMsg)
	body.WriteString("\n\n")
	if nb := notesContextBlock(notes); nb != "" {
		body.WriteString(nb)
	}
	body.WriteString("## Worker findings for this turn (internal context — the user can't see this)\n\n")
	for _, s := range steps {
		fmt.Fprintf(&body, "### Step %d: %s\n%s\n\n", s.ID, s.Title, strings.TrimSpace(s.Output))
	}
	body.WriteString("\n## Your task\nCompose the final reply to the user's message above. Use the worker findings as your source material; integrate them naturally with the prior conversation context. Don't restate the plan unless it helps explain the result, and don't repeat content already established earlier in the conversation.")
	msgs = append(msgs, Message{Role: "user", Content: body.String()})

	// Allocate a stable message id so SSE chunk events know which
	// bubble in the conversation pane to stream into.
	msgID := fmt.Sprintf("synth-%d", time.Now().UnixNano())
	t.sse.Send(map[string]any{
		"kind": "message",
		"role": "assistant",
		"id":   msgID,
		"text": "",
	})

	var full strings.Builder
	handler := func(chunk string) {
		full.WriteString(chunk)
		t.sse.Send(map[string]any{
			"kind": "chunk",
			"id":   msgID,
			"text": chunk,
		})
	}
	stopKeepalive := startKeepalive(t.sse)
	// Worker tier (Private: true route stage) — same routing as the
	// plan round; honors the "worker (thinking)" preference.
	think := true
	if p := RouteThink("app.orchestrate.orchestrator"); p != nil {
		think = *p
	}
	synthStart := time.Now()
	// Same skill injection the orchestrator round saw — synthesis
	// IS the user-facing voice, so it should carry whichever skills
	// activated this turn (style, instructions, relevant corpus
	// chunks). Without this, a skill that prescribed a tone or
	// reference convention would govern the planning but not the
	// final reply. Builder turns no-op (activeSkillsForTurn returns
	// nil for Builder).
	synthSys := prependAgentContext(t.gatedPersona(t.agent.OrchestratorPrompt), t.agent, t.facts())
	synthSys = t.appendActiveSkills(synthSys, nil)
	resp, err := t.app.LLM.ChatStream(t.ctx,
		msgs,
		handler,
		WithSystemPrompt(synthSys),
		WithRouteKey("app.orchestrate.orchestrator"),
		WithThink(think),
	)
	stopKeepalive()
	if err != nil {
		return "", err
	}
	reply := full.String()
	// Thinking models sometimes emit their entire response in the
	// reasoning channel — the content stream stays silent and resp.Content
	// is only populated after agent_loop promotes reasoning. Catch that
	// case and flush as a single chunk so the bubble isn't empty.
	if reply == "" && resp != nil && strings.TrimSpace(resp.Content) != "" {
		reply = resp.Content
		t.sse.Send(map[string]any{
			"kind": "chunk",
			"id":   msgID,
			"text": reply,
		})
	}
	t.sse.Send(map[string]any{"kind": "message_done", "id": msgID})
	t.emitStats(msgID, resp, synthStart)
	return strings.TrimSpace(reply), nil
}

// --- helpers ---------------------------------------------------------------

// toLLMMessages converts the on-disk ChatMessage slice to the LLM
// Message slice the Chat call expects. Identity-shaped — the framework's
// Message type uses Role + Content.
func toLLMMessages(msgs []ChatMessage) []Message {
	out := make([]Message, 0, len(msgs))
	for mi, m := range msgs {
		base := Message{Role: m.Role, Content: m.Content}
		// Preserve tool calls + results across turns. ChatMessage.ToolCalls
		// (set on the assistant turn that fired them) gets expanded into
		// the LLM-protocol shape: the assistant message carries ToolCalls,
		// followed by a synthetic user message carrying ToolResults.
		// Without this, the LLM only sees the bare text content from
		// prior turns and has to re-derive what actually happened —
		// which manifests as "looping on what we're even talking about"
		// and re-asking questions whose answers came from tool returns.
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			calls := make([]ToolCall, 0, len(m.ToolCalls))
			results := make([]ToolResult, 0, len(m.ToolCalls))
			for ti, tc := range m.ToolCalls {
				// Stable per-(message, tool-index) IDs so the
				// assistant's ToolCall.ID matches the corresponding
				// ToolResult.ID within this conversion.
				id := fmt.Sprintf("hist-%d-%d", mi, ti)
				calls = append(calls, ToolCall{
					ID:   id,
					Name: tc.Name,
					Args: tc.Args,
				})
				content := tc.Result
				isErr := false
				if tc.Err != "" {
					content = "Error: " + tc.Err
					isErr = true
				}
				results = append(results, ToolResult{
					ID:      id,
					Content: content,
					IsError: isErr,
				})
			}
			base.ToolCalls = calls
			out = append(out, base)
			out = append(out, Message{
				Role:        "user",
				ToolResults: results,
			})
			continue
		}
		out = append(out, base)
	}
	return out
}

// parsePlanSteps coerces the orchestrator's plan_set "steps" arg into
// PlanStep records. Accepts multiple shapes (LLMs occasionally regress
// to older ones even after the schema upgrade):
//
//   - []any of map[string]any with title + optional intent + optional worker_brief
//   - []any of strings (legacy: title-only)
//
// Empty / whitespace titles are dropped silently. Length is clipped
// to maxSteps with a log when the orchestrator overshoots its budget.
// normalizeFormStep coerces one entry from the ask_user_form tool's
// `steps` array into the shape the client renderer expects:
// {question, options:[...], multi:bool}. Defensive against the LLM
// varying types (string options vs []any, missing fields).
func normalizeFormStep(m map[string]any) map[string]any {
	out := map[string]any{
		"question": strings.TrimSpace(stringArg(m, "question")),
	}
	if opts := stringSliceFromArgs(m, "options"); len(opts) > 0 {
		out["options"] = opts
	}
	if v, ok := m["multi"].(bool); ok && v {
		out["multi"] = true
	}
	return out
}

// looksLikeVacuousPlan returns a non-empty reason string when the plan
// consists entirely of no-op / "just respond" steps. Returns "" when
// the plan looks like real decomposition. Matches case-insensitively
// against title+intent+worker_brief for known empty-step phrasings.
//
// Triggers seen in real traces:
//   - title: "Respond to user", intent: "respond directly", brief: "..."
//   - "Acknowledge the message", "Reply to the user", "Compose the answer"
//   - briefs that name respond_directly as the only tool to use
//
// We reject when EVERY step matches at least one of these patterns —
// a single ack step at the end of a real plan is fine.
func looksLikeVacuousPlan(steps []PlanStep) string {
	if len(steps) == 0 {
		return ""
	}
	emptyPatterns := []string{
		"respond_directly",
		"respond to the user",
		"respond to user",
		"reply to the user",
		"reply to user",
		"compose the reply",
		"compose the response",
		"compose the answer",
		"acknowledge the message",
		"acknowledge the user",
		"answer directly",
		"just respond",
		"summarize and reply",
		"summarize and respond",
		"final reply",
		"final response",
	}
	matchesEmpty := func(s string) bool {
		low := strings.ToLower(s)
		for _, p := range emptyPatterns {
			if strings.Contains(low, p) {
				return true
			}
		}
		return false
	}
	for _, st := range steps {
		blob := st.Title + " || " + st.Intent + " || " + st.WorkerBrief
		if !matchesEmpty(blob) {
			return ""
		}
	}
	return "every step is a 'just respond' / no-op step"
}

func parsePlanSteps(raw any, maxSteps int) []PlanStep {
	var out []PlanStep
	add := func(title, intent, brief string, tools []string) {
		title = strings.TrimSpace(title)
		if title == "" {
			return
		}
		out = append(out, PlanStep{
			ID:          len(out) + 1,
			Title:       title,
			Intent:      strings.TrimSpace(intent),
			WorkerBrief: strings.TrimSpace(brief),
			Tools:       tools,
			Status:      StepPending,
		})
	}
	switch v := raw.(type) {
	case []any:
		for _, x := range v {
			switch s := x.(type) {
			case string:
				add(s, "", "", nil)
			case map[string]any:
				add(stringArg(s, "title"), stringArg(s, "intent"), stringArg(s, "worker_brief"), stringSliceFromArgs(s, "tools"))
			}
		}
	case []string:
		for _, s := range v {
			add(s, "", "", nil)
		}
	case []map[string]any:
		for _, m := range v {
			add(stringArg(m, "title"), stringArg(m, "intent"), stringArg(m, "worker_brief"), stringSliceFromArgs(m, "tools"))
		}
	}
	if len(out) > maxSteps {
		Log("[orchestrate.plan] LLM returned %d steps; clipping to MaxPlanSteps=%d",
			len(out), maxSteps)
		out = out[:maxSteps]
	}
	return out
}

// activityCheapID returns a monotonic short id for an activity row.
// Each activity event needs a unique id so future updates (truncation
// hints, status flips) can target it; we don't have one from the
// agent loop, so we generate here. Atomic counter to keep concurrent
// wrapped-handler calls from racing.
var activityIDCounter uint64

func activityCheapID() string {
	n := atomic.AddUint64(&activityIDCounter, 1)
	return fmt.Sprintf("a-%d-%d", time.Now().UnixNano(), n)
}

// formatToolCall renders a tool invocation as a single-line label
// for the activity pane. Keys sorted for stable output across runs.
// Values are stringified and clipped to keep the row scannable —
// long args (page bodies, base64 blobs) ruin the at-a-glance read.
func formatToolCall(name string, args map[string]any) string {
	if len(args) == 0 {
		return "🔧 " + name + "()"
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		var s string
		switch vv := args[k].(type) {
		case string:
			s = vv
		case []any:
			s = fmt.Sprintf("[%d]", len(vv))
		case map[string]any:
			s = "{…}"
		case nil:
			s = "null"
		default:
			s = fmt.Sprintf("%v", vv)
		}
		s = strings.ReplaceAll(s, "\n", " ")
		if len(s) > 80 {
			s = s[:80] + "…"
		}
		parts = append(parts, fmt.Sprintf("%s=%q", k, s))
	}
	return "🔧 " + name + "(" + strings.Join(parts, ", ") + ")"
}

// deriveFindings extracts a short summary from worker output for the
// plan card's always-visible "findings" preview. Takes the first
// non-empty paragraph (split on blank line), strips markdown noise
// that reads badly without rendering, and clips to a length that
// fits a few lines in the plan card. The full output still lives in
// the collapsible — this is just the headline.
func deriveFindings(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "(no output)" {
		return ""
	}
	// First paragraph = everything up to the first blank line.
	if i := strings.Index(s, "\n\n"); i > 0 {
		s = s[:i]
	}
	// Collapse internal newlines so the snippet renders as one
	// flowing line; the user can expand the full output if they
	// want the structure.
	s = strings.ReplaceAll(s, "\n", " ")
	// Strip a leading markdown heading marker — headings in the
	// findings slot land as bare text and look broken.
	s = strings.TrimLeft(s, "# ")
	const cap = 280
	if len(s) > cap {
		// Cut at the last space before cap to avoid mid-word slices.
		cut := cap
		if sp := strings.LastIndexByte(s[:cap], ' '); sp > cap/2 {
			cut = sp
		}
		s = s[:cut] + "…"
	}
	return strings.TrimSpace(s)
}

// truncate clips long tool-output text for activity rendering. The
// worker's full output still rides through the agent loop into the
// step's persisted output, so nothing is lost — only the live
// activity row gets cut down.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n…[truncated]"
}

// planBlockID is the stable id for one plan's in-chat block. Each
// chat round (i.e. each user turn that produces a new plan) gets its
// own block via the round-index suffix so prior plans in the same
// session stay visible — re-emitting the same id triggers onUpdate on
// the existing block instead of creating a new one.
func planBlockID(sessionID string, roundIdx int) string {
	return fmt.Sprintf("plan-%s-%d", sessionID, roundIdx)
}

// emitPlanBlock sends the plan as a single `block` SSE event into the
// conversation pane. Called once when the plan is first set and again
// on every step status / output transition; the framework's block
// dispatcher routes the repeat into the renderer's onUpdate so the
// same DOM node refreshes in place.
//
// Shape matches servitor_plan's payload: title + what_to_find +
// findings + blocked_reason. Intent maps to what_to_find (same
// semantic — "what this step is looking for"). Full per-step output
// is NOT shipped to the renderer — findings is the visible result;
// drill-down lives in the activity pane.
func emitPlanBlock(sse *sseWriter, blockID string, steps []PlanStep) {
	items := make([]map[string]any, 0, len(steps))
	for _, s := range steps {
		item := map[string]any{
			"id":     s.ID,
			"title":  s.Title,
			"status": string(s.Status),
		}
		if strings.TrimSpace(s.Intent) != "" {
			item["what_to_find"] = s.Intent
		}
		if strings.TrimSpace(s.Findings) != "" {
			item["findings"] = s.Findings
		}
		if strings.TrimSpace(s.BlockedReason) != "" {
			item["blocked_reason"] = s.BlockedReason
		}
		items = append(items, item)
	}
	sse.Send(map[string]any{
		"kind": "block",
		"type": "orchestrate_plan",
		"id":   blockID,
		"plan": items,
	})
}

// emitIntentBlock emits a servitor_intent-style block before each
// worker step starts. The conversation pane renders an accent-
// bordered card with "▸ Investigating" + the step's title + intent
// as italic reason, so the user sees what the agent is about to do
// in real time. One block per step; unique id so they accumulate
// chronologically rather than overwriting.
func emitIntentBlock(sse *sseWriter, blockID string, step PlanStep) {
	payload := map[string]any{
		"kind": "block",
		"type": "orchestrate_intent",
		"id":   blockID,
		"text": fmt.Sprintf("Step %d: %s", step.ID, step.Title),
	}
	if r := strings.TrimSpace(step.Intent); r != "" {
		payload["reason"] = r
	}
	sse.Send(payload)
}

// buildToolUseDirective produces a framework-side append to the worker's
// system prompt that nudges the model toward calling the supplied tools
// rather than answering from training. Without this push, the worker
// routinely fabricates answers to questions like "what's the weather in
// SF?" or "latest news on X" — both well-served by web_search/fetch_url
// but invisible to the model unless it's reminded.
//
// Lists each tool name + first-line description so the model knows what
// it has. The "prefer tools over guessing" language is the load-bearing
// nudge — models are good at picking the right tool when told to look
// before answering.
func buildToolUseDirective(tools []AgentToolDef) string {
	var b strings.Builder
	b.WriteString("## Tools available\n\n")
	b.WriteString("You have these tools. PREFER calling a tool over answering from training whenever the question involves:\n")
	b.WriteString("- live information (weather, news, prices, sports scores, current events)\n")
	b.WriteString("- specific facts that could have changed since your training (versions, URLs, hours, contact info)\n")
	b.WriteString("- anything the user could verify externally and catch you fabricating\n\n")
	b.WriteString("If in doubt, call a tool. A wrong answer from training reads as you making things up; a tool result reads as research.\n\n")
	for _, t := range tools {
		desc := strings.SplitN(t.Tool.Description, "\n", 2)[0]
		if len(desc) > 200 {
			desc = desc[:200] + "…"
		}
		fmt.Fprintf(&b, "- **%s** — %s\n", t.Tool.Name, desc)
	}
	return b.String()
}
