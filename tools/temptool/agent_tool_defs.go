package temptool

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// BuildAgentToolDefs converts a session's temp tools into AgentToolDefs
// suitable for AgentLoopConfig.DynamicTools. Each tool's handler
// substitutes the caller's args into the command template (shell-quoted
// to prevent injection) and runs through RunSandboxedShell.
func BuildAgentToolDefs(sess *ToolSession) []AgentToolDef {
	if sess == nil {
		Debug("[temptool] BuildAgentToolDefs called with nil session")
		return nil
	}
	tools := sess.CopyTempTools()
	if len(tools) == 0 {
		Debug("[temptool] BuildAgentToolDefs: sess.TempTools is empty (no dynamic temp tools to expose to the LLM this round)")
		return nil
	}
	names := make([]string, 0, len(tools))
	for _, tt := range tools {
		names = append(names, tt.Name)
	}
	Debug("[temptool] BuildAgentToolDefs: producing %d AgentToolDef(s), %v", len(tools), names)
	out := make([]AgentToolDef, 0, len(tools))
	for _, tt := range tools {
		out = append(out, agentToolDefsFromTemp(sess, tt)...)
	}
	return out
}

// agentToolDefsFromTemp renders one temp tool into the AgentToolDefs it
// contributes to the catalog. Almost every tool yields exactly one def;
// an EXPANDED toolbox (tt.Expand) yields one `<toolbox>_<action>` def per
// live action instead of a single collapsed action-dispatch entry. The
// single-entity boundary is untouched — one record, one credential, one
// artifact; expansion is purely presentation, decided here at build time.
func agentToolDefsFromTemp(sess *ToolSession, tt *TempTool) []AgentToolDef {
	// Drop a temp tool that collides with a dynamic built-in (e.g. a stale
	// send_message authored before the create-time guard existed). Leaving it in
	// would shadow the real, delivering tool with a stub. Dropping it here lets
	// the built-in (assembled separately at dispatch) take the name back.
	if IsReservedToolName(tt.Name) {
		return nil
	}
	var defs []AgentToolDef
	if tt.Mode == TempToolModeToolbox && tt.Expand {
		defs = expandedToolboxDefs(sess, tt)
	} else {
		defs = []AgentToolDef{agentToolFromTemp(sess, tt)}
	}
	return markNotReady(sess, tt, defs)
}

// markNotReady leads a tool's description with why it cannot work yet (its
// credential is turned off or has no key), so the agent tells the user what is
// missing instead of calling it every conversation and reporting the same
// failure. The tool stays in the catalog: a capability the agent believes in
// but cannot find sends it improvising another way in.
func markNotReady(sess *ToolSession, tt *TempTool, defs []AgentToolDef) []AgentToolDef {
	cred := strings.TrimSpace(tt.Credential)
	if cred == "" || (tt.Mode != TempToolModeAPI && tt.Mode != TempToolModeToolbox) {
		return defs
	}
	why := Secure().NotReady(sessUser(sess), cred)
	if why == "" {
		return defs
	}
	for i := range defs {
		defs[i].Tool.Description = "NOT READY: " + why + ". Do not call it; tell the user what is missing. " + defs[i].Tool.Description
	}
	return defs
}

// expandedToolboxDefs surfaces each non-disabled toolbox action as its own
// top-level tool named `<toolbox>_<action>`, with the action's own params
// as its schema (no action="<sub>" indirection). Each handler pins the
// action and reuses the shared toolbox dispatcher, so credential, allow-
// list, audit, and response_pipe behavior are identical to the collapsed
// path. Disabled actions are quarantined — dropped from the catalog.
func expandedToolboxDefs(sess *ToolSession, tt *TempTool) []AgentToolDef {
	out := make([]AgentToolDef, 0, len(tt.Actions))
	for i := range tt.Actions {
		if tt.Actions[i].Disabled {
			continue
		}
		out = append(out, perActionToolDef(sess, tt, tt.Actions[i]))
	}
	return out
}

// catalogNamesOf returns every catalog name one temp-tool record contributes.
// Usually just the record's own name — but a TOOLBOX also mints
// "<toolbox>_<action>" per action (see perActionToolDef), and those synthesized
// names share one namespace with ordinary tools.
//
// Actions count whether or not Expand is currently set: the flag is a display
// choice that can be toggled later, and a name that becomes a collision the
// moment someone flips a switch is a collision now.
func catalogNamesOf(tt *TempTool) []string {
	if tt == nil {
		return nil
	}
	out := []string{tt.Name}
	if tt.Mode == TempToolModeToolbox {
		for _, a := range tt.Actions {
			if n := strings.TrimSpace(a.Name); n != "" {
				out = append(out, tt.Name+"_"+n)
			}
		}
	}
	return out
}

// CheckCatalogNameCollision rejects an authoring call whose resulting catalog
// names would collide with a DIFFERENT record already registered on the
// session. AppendTempTool only compares record names, which misses the whole
// class: a toolbox named "x" with action "y" and a standalone tool named "x_y"
// have different record names and identical catalog names. The loser is then
// shadowed silently at dispatch, so the author sees two successful creates and
// one tool that behaves like the other.
//
// A record with the SAME name is skipped — re-authoring over an existing name
// is the canonical iteration path (it overwrites), not a collision.
//
// Scope is the WHOLE USER, not just this session: the session's loaded catalog,
// the user-wide pool, the pending queue, AND every tool bundled to any of the
// user's other agents. That makes the tool namespace unique per user — one
// agent can't author a name another agent already holds — which is the
// invariant that guarantees an in-place edit targets the one true tool by that
// name (see updateGrouped / finalizeAuthoredTool). Without the cross-agent
// scan, agent A and agent B could each hold a differently-defined "moltbook"
// and an edit would ambiguously pick one.
func CheckCatalogNameCollision(sess *ToolSession, name string, actions []TempToolAction) error {
	if sess == nil || name == "" {
		return nil
	}
	proposed := map[string]bool{name: true}
	for _, a := range actions {
		if n := strings.TrimSpace(a.Name); n != "" {
			proposed[name+"_"+n] = true
		}
	}
	inSession := make(map[string]bool)
	// Check 1 — catalog-name SHADOWING within this session (the original purpose:
	// a toolbox "x"/action "y" vs a standalone "x_y" have distinct record names
	// but identical catalog names). A same-NAME record is the iteration/overwrite
	// path, skipped.
	for _, tt := range sess.CopyTempTools() {
		if tt == nil {
			continue
		}
		inSession[tt.Name] = true
		if tt.Name == name {
			continue
		}
		for _, existing := range catalogNamesOf(tt) {
			if !proposed[existing] {
				continue
			}
			if existing == tt.Name {
				return fmt.Errorf("name %q is already taken by an existing tool: pick another name, or use action=\"update\" to change that tool in place", existing)
			}
			return fmt.Errorf("name %q collides with toolbox %q, whose action %q already publishes that exact name: two tools cannot share one catalog name (one would silently shadow the other). Either pick a different name, or use action=\"update\" on toolbox %q to change the action itself",
				existing, tt.Name, strings.TrimPrefix(existing, tt.Name+"_"), tt.Name)
		}
	}
	// Check 2 — GLOBAL top-level-name uniqueness across the user. The proposed
	// name must not already belong to a DIFFERENT home (user-wide pool, pending
	// queue, or another of the user's agents) that this authoring call won't
	// overwrite. inSession is the overwrite set — a name this session already
	// loads/owns is legitimate iteration, not a duplicate. Skipped when
	// BundleAuthoredToolTo is set: that's an in-place edit of an existing tool,
	// which is SUPPOSED to write over the same name. This is the invariant that
	// keeps the namespace unique per user, so a name resolves to exactly one tool.
	if sess.BundleAuthoredToolTo == "" && sess.DB != nil && sess.Username != "" && !inSession[name] {
		holds := func(tt TempTool) bool {
			for _, existing := range catalogNamesOf(&tt) {
				if proposed[existing] {
					return true
				}
			}
			return false
		}
		for _, p := range LoadPersistentTempTools(sess.DB, sess.Username) {
			if holds(p.Tool) {
				return fmt.Errorf("name %q is already taken by a tool in your user-wide pool: names are unique across all your agents; pick another name, or use action=\"update\" to change that tool", name)
			}
		}
		for _, p := range LoadPendingTempTools(sess.DB, sess.Username) {
			if holds(p.Tool) {
				return fmt.Errorf("name %q is already taken by a pending tool of yours: pick another name, or use action=\"update\"", name)
			}
		}
		if ListUserAgentTools != nil {
			for _, at := range ListUserAgentTools(sess.DB, sess.Username) {
				if holds(at) {
					return fmt.Errorf("name %q is already taken by another of your agents' tools: names are unique across all your agents; pick another name, or use action=\"update\" to edit that tool in place", name)
				}
			}
		}
	}
	return nil
}

// perActionToolDef builds the standalone AgentToolDef for one expanded
// toolbox action. Mirrors the collapsed group's per-action handler (pin
// action, hand off to dispatchTempTool → dispatchToolboxModeTempTool) but
// promotes the action to a first-class catalog name with its own schema.
func perActionToolDef(sess *ToolSession, tt *TempTool, act TempToolAction) AgentToolDef {
	writes := isMutatingMethod(act.Method)
	kind := "read"
	if writes {
		kind = "write"
	}
	desc := act.Description + fmt.Sprintf(" (%s action of toolbox %q, credential %q; defined via tool_def)", kind, tt.Name, tt.Credential)
	return AgentToolDef{
		Tool: Tool{
			Name:        tt.Name + "_" + act.Name,
			Description: desc,
			Parameters:  act.Params,
			Required:    act.Required,
			Caps:        []Capability{CapNetwork, CapExecute},
			Category:    tt.Category, // expanded actions inherit the toolbox's claimed category
		},
		// Same tier resolution as the collapsed group: the credential's
		// Require-confirm toggle decides, not a blanket true — an expanded
		// action must not be stricter than the toolbox it came from.
		NeedsConfirm: tempToolNeedsConfirm(tt, sessUser(sess)),
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			a2 := make(map[string]any, len(args)+1)
			for k, v := range args {
				a2[k] = v
			}
			a2["action"] = act.Name
			// Resolve against the live record so a mid-turn edit to this
			// action (url/body/params) dispatches the current version, not
			// the turn-start snapshot (same staleness fix as the collapsed
			// path — agent-owned toolboxes are pinned static and skip the
			// dynamic refresh feed).
			live := sess.LookupTempTool(tt.Name)
			if live == nil {
				live = tt
			}
			return dispatchTempTool(sess, live, a2)
		},
	}
}

// isMutatingMethod reports whether an HTTP method has side effects — used
// to classify a toolbox action as read vs write for the LLM description
// and the admin audit view. Empty defaults to GET (read).
func isMutatingMethod(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case "", "GET", "HEAD", "OPTIONS":
		return false
	default:
		return true
	}
}

// newToolboxGroupedTool builds the framework GroupedTool for a toolbox-mode
// TempTool: one catalog name, action="<sub>" dispatch, disabled actions left
// out (quarantined). Each action handler synthesizes a single-endpoint api-
// mode TempTool at dispatch time via dispatchTempTool. Built from whatever
// record is passed — the caller passes the LIVE session record so a group
// rebuilt mid-call reflects mutations (see agentToolFromTemp's live-resolve
// handler).
func newToolboxGroupedTool(tt *TempTool) *GroupedTool {
	live := 0
	for i := range tt.Actions {
		if !tt.Actions[i].Disabled {
			live++
		}
	}
	// Provenance-neutral suffix: this builder serves EVERY toolbox — the
	// user's persistent/scoped kit included — so the description must not
	// claim "defined this session" (it taught the model, and anyone reading
	// a session export, a false provenance for long-lived tools).
	gtDesc := tt.Description + fmt.Sprintf(" (toolbox: wraps credential %q with %d action(s); manage via tool_def)", tt.Credential, live)
	gt := NewGroupedTool(tt.Name, gtDesc)
	for i := range tt.Actions {
		if tt.Actions[i].Disabled {
			continue // quarantined — not offered
		}
		act := tt.Actions[i] // capture by value for the closure
		gt.AddAction(act.Name, &GroupedToolAction{
			Description: act.Description,
			Params:      act.Params,
			Required:    liveRequired(act),
			Caps:        []Capability{CapNetwork, CapExecute}, // api-mode + response_pipe
			Handler: func(args map[string]any, s *ToolSession) (string, error) {
				// Re-attach the action key so the toolbox dispatcher
				// finds its routing handle. The framework's grouped
				// tool stripped it during routing; we put it back
				// because dispatchToolboxModeTempTool reads
				// args["action"] to look up the sub-action.
				a2 := make(map[string]any, len(args)+1)
				for k, v := range args {
					a2[k] = v
				}
				a2["action"] = act.Name
				return dispatchTempTool(s, tt, a2)
			},
		})
	}
	return gt
}

// tempToolCaps returns the capability tier a non-toolbox temp tool needs at
// dispatch. API mode needs CapNetwork (HTTP via the stored credential), plus
// CapExecute when it carries a response_pipe (sandboxed shell over the
// response). Shell / pipeline / everything else runs sandboxed shell →
// CapExecute. Shared by the def builder (declared caps, cap-gated by the
// loop) and the live-resolve guard (so a mid-turn edit can't dispatch a
// version that needs more than the loop gated on).
func tempToolCaps(tt *TempTool) []Capability {
	if tt.Mode == TempToolModeAPI {
		if tt.ResponsePipe != "" {
			return []Capability{CapNetwork, CapExecute}
		}
		return []Capability{CapNetwork}
	}
	return []Capability{CapExecute}
}

// capsSubset reports whether every capability in want is present in have —
// i.e. want does not exceed the granted set.
func capsSubset(want, have []Capability) bool {
	for _, w := range want {
		found := false
		for _, h := range have {
			if h == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// tempToolNeedsConfirm reports whether a temp tool is consequential enough to
// require per-call approval. RawNetwork leaves the sandbox; a hook capability
// outside the read-only set (secret:<name>, fetch_via:<name>, …) grants more
// than a benign fetch; a credential-less api tool is a raw endpoint with no
// admin-declared tier. A plain shell tool that at most does an audited
// read-only fetch/log/browse is not consequential and runs freely.
//
// A CREDENTIAL-backed tool (api / toolbox / shell-with-credential) defers to
// the credential's own "Require confirm before each call" toggle — the same
// contract the auto-generated call_<cred> / fetch_url_<cred> bridge tools
// already honor (secure_api sets their NeedsConfirm to c.RequiresConfirm).
// Blanket-marking every credentialed temp tool made the SAME credential behave
// two ways: its bridge tool ran unattended while its toolbox was refused on
// every scheduled/standing fire ("tools enabled on the agent but the scheduler
// can't call them") — interactive turns hid the split because their confirm
// hook only escalates RequiresConfirm credentials anyway. Unknown credential
// fails closed.
// NeedsConfirm is the exported form of the same judgement, for callers outside
// this package that must show or reason about a stored tool's tier BEFORE it
// runs — the inline privileges card names which of an agent's tools will stop
// and ask on an unattended fire, and it has to agree with the gate exactly or
// it teaches the user something false.
func NeedsConfirm(tt *TempTool, user ...string) bool { return tempToolNeedsConfirm(tt, user...) }

// tempToolNeedsConfirm is the rule described above. user, when given, is the session's user, whose OWN credentials the tool's
// credential is resolved among first. Only global credentials were ever
// looked at, so a tool on a user's own credential could not be found, failed
// closed, and asked before every call whatever the credential said.
func tempToolNeedsConfirm(tt *TempTool, user ...string) bool {
	if tt == nil {
		return true
	}
	// The tool's own word, ahead of everything inferred below it. An owner who
	// asked to be consulted about this tool is not overridden by the fact that
	// its credential happens not to require it.
	if tt.ConfirmInChat {
		return true
	}
	// Raw network, and a persistent shell (which always has it: see
	// NewSandboxedShellCmd): both reach past the audited fetch hook.
	if tt.RawNetwork || tt.Mode == TempToolModePersistent {
		return true
	}
	owner := ""
	if len(user) > 0 {
		owner = user[0]
	}
	for _, c := range tt.HookCapabilities {
		switch {
		case c == "fetch" || c == "log" || c == "browse_page":
			// read-only audited hooks — benign
		case strings.HasPrefix(c, "fetch_via:"):
			// A call through a credential: allowlisted, audited, the key kept
			// server-side. It takes that credential's own tier, as an api tool
			// on it does. Blanket-true made a shell tool on a quiet credential
			// ask before every call, so test would never run it, while an api
			// tool on the same credential ran unattended.
			cred := strings.TrimSpace(strings.TrimPrefix(c, "fetch_via:"))
			if cr, ok := Secure().Resolve(cred, owner); !ok || cr.RequiresConfirm {
				return true // unresolvable fails closed
			}
		default:
			return true // secret:<name> hands the raw key to the script, or anything else
		}
	}
	if cred := strings.TrimSpace(tt.Credential); cred != "" {
		if c, ok := Secure().Resolve(cred, owner); ok {
			return c.RequiresConfirm
		}
		return true // credential named but not resolvable — fail closed
	}
	if tt.Mode == TempToolModeAPI {
		return true // api mode with no credential: raw endpoint, no declared tier
	}
	return false
}

// currentTempTool is the record a call should run: the session's live copy,
// unless the user's stored row of that tool has changed since the session
// loaded it. The session reads the store once, at turn start, and an edit made
// by ANOTHER session (a delegated Builder fixing a tool the calling agent
// holds) only reaches that other session's copy. Observed: Builder rewrote a
// failing script, and the agent that asked for the fix ran the old script
// again, the same traceback from the same file, until its turn ended.
//
// Only a row this agent would load itself is taken: unscoped or scoped to it,
// and not turned off, which is the rule the turn-start load applies. Callers
// keep their capability guard, so a stored edit that needs more than the
// catalog gated on still waits for the next turn.
func currentTempTool(sess *ToolSession, name string) *TempTool {
	live := sess.LookupTempTool(name)
	if sess == nil || sess.DB == nil || sess.Username == "" {
		return live
	}
	p, ok := UserToolByName(sess.DB, sess.Username, name)
	if !ok || p.Tool.Disabled || p.Tool.BuilderOnly || p.Tool.BoundOnly {
		return live
	}
	if len(p.ScopeAgents) > 0 && !p.ScopedToAgent(sess.AgentID) {
		return live
	}
	if live != nil && (live.Mode != p.Tool.Mode || reflect.DeepEqual(*live, p.Tool)) {
		return live
	}
	stored := p.Tool
	return &stored
}

func agentToolFromTemp(sess *ToolSession, tt *TempTool) AgentToolDef {
	// Toolbox mode is structurally a GroupedTool — bundle of action-
	// dispatched sub-endpoints. The LLM-facing schema is identical to
	// built-in grouped tools (tool_def / agents / workspace): one tool
	// name in the catalog, action="<sub>" routes to the right endpoint.
	if tt.Mode == TempToolModeToolbox {
		snapshot := newToolboxGroupedTool(tt)
		def := ChatToolToAgentToolDefWithSession(snapshot, sess)
		// Live-resolve on dispatch. An agent-OWNED toolbox is pinned into
		// the static catalog (staticTempToolNames) and the per-round
		// dynamic-tool feed deliberately skips static names, so the group
		// built at turn start is re-registered every round and never
		// refreshed. Without this, a mid-turn tool_def update / delete /
		// recreate mutates the session record but dispatch keeps hitting
		// the frozen snapshot — a renamed action 404s as "unknown action"
		// and help lists stale names (observed: a whole moltbook rename
		// that never took effect). Rebuilding the group from the LIVE
		// record on each call makes mutations take effect immediately.
		// The schema shown to the LLM still reflects the snapshot until the
		// next turn (cosmetic — the model calls the name it just authored,
		// and both dispatch and the help action resolve against live).
		def.Handler = func(ctx context.Context, args map[string]any) (string, error) {
			live := currentTempTool(sess, tt.Name)
			if live == nil || live.Mode != TempToolModeToolbox {
				out, err := snapshot.RunWithSession(args, sess)
				return adviseOnFailure(sess, tt, out, err)
			}
			out, err := newToolboxGroupedTool(live).RunWithSession(args, sess)
			return adviseOnFailure(sess, live, out, err)
		}
		def.Tool.Category = tt.Category // claimed grouping label rides onto the toolbox def too
		return def
	}

	// Caps depend on execution mode (see tempToolCaps). The AllowedCaps
	// filter then hides the tool from sessions that don't grant the tier.
	caps := tempToolCaps(tt)
	needsConfirm := tempToolNeedsConfirm(tt, sessUser(sess))
	if tt.Mode == TempToolModePipeline {
		inner, confirm := pipelineInnerProfile(sess, tt, 0)
		for _, c := range inner {
			if !capsSubset([]Capability{c}, caps) {
				caps = append(caps, c)
			}
		}
		needsConfirm = needsConfirm || confirm
	}
	// Same provenance-neutral rule as the toolbox suffix above: persistent
	// pool tools flow through here too, so no "defined this session" claim.
	descSuffix := " (custom shell tool: manage via tool_def)"
	if tt.Mode == TempToolModeAPI {
		descSuffix = fmt.Sprintf(" (custom api tool: wraps credential %q; manage via tool_def)", tt.Credential)
	}
	return AgentToolDef{
		Tool: Tool{
			Name:        tt.Name,
			Description: tt.Description + descSuffix,
			Parameters:  tt.Params,
			Required:    tt.Required,
			Caps:        caps,
			Category:    tt.Category, // the claimed grouping label rides onto the runtime def
			// A script fetching through the hooks returns outside content,
			// which is fenced like a network tool's (see FetchesExternal).
			FetchesExternal: hooksFetch(tt.HookCapabilities),
		},
		// Confirm only for CONSEQUENTIAL temp tools — ones that reach a real
		// endpoint (api mode / a credential), leave the sandbox (RawNetwork),
		// or hold a capability beyond the read-only audited hooks. A cred-less
		// shell tool whose only external effect is a fetch/log/browse_page hook
		// is read-only + low-consequence, so it runs WITHOUT an approval prompt
		// — matching how the interactive orchestrate path already treats it
		// (its confirm hook only gates credentialed tools), so an unattended
		// scheduled fire doesn't queue an approval for every benign tool call.
		NeedsConfirm: needsConfirm,
		// Live-resolve on dispatch — same staleness fix as the toolbox path
		// above, generalized to api/shell/pipeline tools. An agent-OWNED
		// temp tool is pinned into the static catalog and skipped by the
		// per-round dynamic-tool refresh, so a mid-turn tool_def/add_tool
		// update would otherwise keep dispatching the frozen turn-start
		// snapshot (an edited url_template / body_template / command /
		// script_body never takes effect). Dispatch the LIVE record instead.
		// GUARD: only when the live tool needs no MORE capabilities than the
		// snapshot the loop already cap-gated on. A mid-turn edit that GROWS
		// the cap profile (e.g. an api tool gaining a response_pipe →
		// CapExecute) must wait for the next turn's fresh build + gate, so it
		// can't run an un-gated shell pipe this turn; until then the snapshot
		// dispatches. Non-cap edits — the common case — apply immediately.
		Handler: jobDetach(sess, tt, func(ctx context.Context, args map[string]any) (string, error) {
			run := tt
			if live := currentTempTool(sess, tt.Name); live != nil && capsSubset(tempToolCaps(live), caps) {
				run = live
			}
			out, err := dispatchTempTool(sess, run, args)
			recordCleanRun(sess, run, out, err)
			return adviseOnFailure(sess, run, out, err)
		}),
	}
}

// jobDetach lets a job tool that usually runs long (its job's expect_secs)
// go to the background instead of holding the turn, the way the image
// generators do: the framework decides from the expected time, the job runs
// against a session that outlives the turn, and its result (and the file it
// delivers) arrives when it is done. A tool with no job, or a quick one, runs
// inline as before.
func jobDetach(sess *ToolSession, tt *TempTool, inline ToolHandlerFunc) ToolHandlerFunc {
	if sess == nil || tt == nil || tt.Job == nil || tt.Job.ExpectSecs <= 0 {
		return inline
	}
	expect := time.Duration(tt.Job.ExpectSecs) * time.Second
	return WrapDetachable(DetachPolicy{
		Tool:     tt.Name,
		Label:    func(map[string]any) string { return tt.Name },
		Expected: func(map[string]any, *ToolSession) time.Duration { return expect },
		Typical:  func(map[string]any, *ToolSession) time.Duration { return expect },
		Detached: func(args map[string]any, detached *ToolSession) (string, error) {
			run := tt
			if live := currentTempTool(detached, tt.Name); live != nil {
				run = live
			}
			return dispatchTempTool(detached, run, args)
		},
	}, sess, inline)
}

// sessUser is the session's user, "" without a session.
func sessUser(sess *ToolSession) string {
	if sess == nil {
		return ""
	}
	return sess.Username
}

// recordCleanRun counts a direct call that ran clean as verification: the same
// bar test sets, exit 0 for a shell tool and a 2xx for an api tool. The test
// report tells an author to call a tool that asks before each call directly,
// once, and that call never counted: a tool run clean seven times stayed
// "unverified" until it was deleted and recreated. A failed run changes
// nothing; a bad argument from the caller is not the tool's verdict. Nor does
// a run that printed nothing or was cut short: test fails those.
func recordCleanRun(sess *ToolSession, tt *TempTool, out string, err error) {
	if err != nil || sess == nil || tt == nil {
		return
	}
	switch effectiveTempToolMode(*tt) {
	case TempToolModeShell:
		if shellRunFailed(out) || shellRunHollow(out) != "" {
			return
		}
	case TempToolModeAPI:
		// A pipe or an extract replaces the status line, so the result
		// cannot say whether the call succeeded.
		if tt.ResponsePipe != "" || tt.ResponseExtract != nil {
			return
		}
		if status, _ := splitStatusLine(out); !isStatus2xx(status) {
			return
		}
	default:
		return
	}
	RecordToolVerification(sess, tt.Name, true, "")
}

// adviseOnFailure adds the host's advice (ToolSession.ToolFailureAdvice) to a
// custom tool's failed result: an error, a script that exited non-zero or
// timed out, or an HTTP error status.
func adviseOnFailure(sess *ToolSession, tt *TempTool, out string, err error) (string, error) {
	if sess == nil || sess.ToolFailureAdvice == nil || tt == nil {
		return out, err
	}
	failed, why := toolRunFailure(out, err)
	if !failed {
		return out, err
	}
	advice := sess.ToolFailureAdvice(tt.Name, why)
	if advice == "" {
		return out, err
	}
	if err != nil {
		return out, fmt.Errorf("%w\n\n%s", err, advice)
	}
	return out + "\n\n" + advice, nil
}

// Why a failure is the tool's and not the caller's arguments.
const (
	failureBroke = "it crashed, timed out or the service failed"
	failureCut   = "its output was cut off at the size limit, so what came back is incomplete"
)

// toolRunFailure reads a custom tool's result for failure, and for why, when
// it is one the caller's arguments could not have caused: a crash, a timeout,
// a server error, output cut off at the size limit. A non-zero exit with no
// traceback, or an HTTP 4xx, can be a bad argument, so it is a failure with no
// why.
//
// A cut result is the one that looks like a success. Observed: a music tool
// whose response carried the audio inline came back cut at the cap every
// time, the agent decoded what it had, and handed the user a fragment as the
// full track, then wrote its own extractors to dig for the rest.
func toolRunFailure(out string, err error) (failed bool, why string) {
	// A provider refusing the content (the phrase the secure API puts on such
	// a reply) is the request, not the tool: rephrasing fixes it and Builder
	// cannot, so it is not a failure to advise on.
	if strings.Contains(out, "REFUSED THE CONTENT") || (err != nil && strings.Contains(err.Error(), "REFUSED THE CONTENT")) {
		return false, ""
	}
	if err != nil {
		e := strings.ToLower(err.Error())
		// Refused before it ran for what the CALL left out: the tool never
		// ran, so it cannot be the tool that is broken, and the error already
		// says what to pass. Counted, two empty calls told an agent the tool
		// "looks broken" and to offer the user a Builder fix for its own
		// missing argument.
		if strings.Contains(e, "missing required arg") || (strings.Contains(e, "required arg") && strings.Contains(e, "is empty")) {
			return false, ""
		}
		if strings.Contains(e, "timeout") || strings.Contains(e, "did not respond") || strings.Contains(e, "timed out") {
			return true, failureBroke
		}
		return true, ""
	}
	switch {
	case strings.Contains(out, "[TIMED OUT"):
		return true, failureBroke
	case strings.Contains(out, "Traceback (most recent call last)") && strings.Contains(out, "[exit: "):
		return true, failureBroke
	case strings.Contains(out, "\n... [TRUNCATED"):
		return true, failureCut
	case strings.Contains(out, "[exit: "):
		return true, ""
	}
	if status, _ := splitStatusLine(out); status != "" && !isStatus2xx(status) {
		if strings.HasPrefix(status, "HTTP 5") {
			return true, failureBroke
		}
		return true, ""
	}
	return false, ""
}

// hooksFetch reports whether hook capabilities let a script bring outside
// content back: a fetch, a credentialed fetch, or a rendered page.
func hooksFetch(caps []string) bool {
	for _, c := range caps {
		if c == "fetch" || c == "browse_page" || strings.HasPrefix(c, "fetch_via:") {
			return true
		}
	}
	return false
}
