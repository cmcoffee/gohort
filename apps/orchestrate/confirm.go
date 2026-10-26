// Tool-call escalation — the two-tier send-allowance model.
//
// Every service credential carries an admin-owned "Require confirm
// before each call" toggle (Admin > APIs). Tier 1 (toggle OFF —
// e.g. an agent's own social account): calls through the credential
// dispatch silently, no human in the loop. Tier 2 (toggle ON — e.g.
// a messaging surface that reaches real people): each call escalates
// to the session owner as a confirm card in the chat (Allow once /
// Deny) and the agent loop parks until they answer.
//
// The mechanism was already plumbed end-to-end — core's agent loop
// calls cfg.Confirm for every NeedsConfirm tool, and the chat panel
// renders {kind:"confirm"} SSE events as approval cards POSTing back
// to ConfirmURL — but orchestrate's Confirm hook was a stub that
// auto-approved everything, which made the credential toggle dead
// weight. This file is the real hook.
//
// Security posture:
//   - The LLM can trigger an escalation but can never resolve one:
//     resolution arrives only via the owner's browser POST (cookie-
//     authenticated, owner-checked) to /api/confirm.
//   - Headless contexts (channel wakes, external dispatches — no SSE
//     viewer attached) FAIL CLOSED for flagged credentials: nobody is
//     there to approve, so the call is denied rather than allowed.
//   - Unflagged tools keep the previous always-allow behavior, so
//     nothing that worked yesterday starts prompting today.
//
// There are now TWO reasons a call escalates, sharing one mechanism:
//
//   - the credential toggle above, and
//   - a host-app tool that declared AgentToolDef.ConfirmPrompt, which is
//     how an app whose tools change files or run commands puts a real
//     question in front of the user. It is opt-in per tool and carries
//     its own sentence; see the field's comment in core/agent_loop.go.
//
// Both fail closed on a run with no viewer, for the same reason: an
// approval nobody can give is not an approval.

package orchestrate

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/textutil"
)

// toolConfirmTimeout is how long an escalated call waits for the owner's
// Allow/Deny before the turn stops waiting. Long enough to read the card
// and think; short enough that an abandoned session doesn't pin a goroutine
// forever. A call with a conversation is kept for later rather than denied
// (approval_resume.go). A var so a test can wait less.
var toolConfirmTimeout = 5 * time.Minute

// pendingToolConfirm is one in-flight escalation: an agent-loop
// goroutine parked on ch until the session owner clicks Allow once /
// Deny on the confirm card (or the timeout fires).
type pendingToolConfirm struct {
	user string
	ch   chan bool
	// answer carries WHICH allow was clicked back to the parked goroutine.
	// The channel alone cannot: "allow once" and "always allow" are both
	// true, and only the waiter is in a position to persist the grant (it
	// holds the turn's store and the grant it offered).
	answer chan string
	// allows names the button values that mean yes, for a card whose buttons
	// are not the allow/always pair. Nil means that pair, which is every card
	// but the ones askInChat raises.
	allows map[string]bool
}

// toolConfirms holds the in-flight escalations by card id. Package-
// level (not per-turn): the resolving POST arrives on a different
// request than the one running the agent loop.
var toolConfirms sync.Map // id -> *pendingToolConfirm

// credentialForToolCall resolves which SecureAPI credential a tool
// call dispatches through, or "" when the tool is not credential-
// backed. Two shapes: api/toolbox temp tools carry the credential on
// their record; the auto-generated per-credential tools carry it in
// the name (call_<cred> / fetch_url_<cred>).
// toolRecordFor finds the custom-tool record behind a call, or nil for a name
// that is not one.
//
// From the SESSION's copy, which is the set that actually resolved for this
// turn, so a name matches the definition that ran rather than a same-named
// tool in somebody else's pool.
func toolRecordFor(sess *ToolSession, name string) *TempTool {
	if sess == nil {
		return nil
	}
	for _, tt := range sess.CopyTempTools() {
		if tt.Name == name {
			return tt
		}
	}
	return nil
}

// credentialsForToolCall is every credential a call can spend: an api tool's
// own, and a script tool's fetch_via:<name> and secret:<name> hooks, or the
// credential a bridge poll or fetch_url_<name> tool is named for. It used to
// read only an api tool's Credential, so a script reaching the same key
// through a hook never met the consent the credential asks for, in chat or
// unattended.
func credentialsForToolCall(sess *ToolSession, name string) []string {
	if tt, _ := toolForCall(sess, name); tt != nil {
		return toolCredentials(*tt)
	}
	if rest := strings.TrimPrefix(name, bridgeCredToolPrefix); rest != name {
		return []string{rest}
	}
	if rest := strings.TrimPrefix(name, "fetch_url_"); rest != name {
		return []string{rest}
	}
	return nil
}

// toolForCall finds the tool record a call runs: by name, or an expanded
// toolbox's "<toolbox>_<action>" (returning the action). An expanded action
// matched no record by name, so it met none of its credential's consent.
func toolForCall(sess *ToolSession, name string) (*TempTool, string) {
	if sess == nil {
		return nil, ""
	}
	tools := sess.CopyTempTools()
	for _, tt := range tools {
		if tt.Name == name {
			return tt, ""
		}
	}
	for _, tt := range tools {
		if tt.Mode == TempToolModeToolbox && tt.Expand && strings.HasPrefix(name, tt.Name+"_") {
			return tt, strings.TrimPrefix(name, tt.Name+"_")
		}
	}
	return nil, ""
}

// callMayWrite reports whether THIS call can change something, for a
// credential that asks before writes: fetch_url_<cred> or a poll by its
// method argument, an api tool by its method, a toolbox by the action called,
// and a script always (its method is not known before it runs).
func callMayWrite(tt *TempTool, action, name, args string) bool {
	if tt == nil {
		if strings.HasPrefix(name, "fetch_url_") || strings.HasPrefix(name, bridgeCredToolPrefix) {
			return IsWriteMethod(methodOrGET(argFromPreview(args, "method")))
		}
		return true
	}
	switch tt.Mode {
	case TempToolModeAPI:
		return IsWriteMethod(methodOrGET(tt.Method))
	case TempToolModeToolbox:
		if action == "" {
			action = argFromPreview(args, "action")
		}
		for _, a := range tt.Actions {
			if a.Name == action {
				return IsWriteMethod(methodOrGET(a.Method))
			}
		}
	}
	return true
}

// methodOrGET is a method, GET when none is given (dispatch's default).
func methodOrGET(m string) string {
	if strings.TrimSpace(m) == "" {
		return "GET"
	}
	return m
}

// toolCredentials is the credentials one tool record reaches.
func toolCredentials(tt TempTool) []string {
	var out []string
	if c := strings.TrimSpace(tt.Credential); c != "" {
		out = append(out, c)
	}
	for _, capName := range tt.HookCapabilities {
		for _, prefix := range []string{"fetch_via:", "secret:"} {
			if rest := strings.TrimPrefix(capName, prefix); rest != capName {
				if rest = strings.TrimSpace(rest); rest != "" {
					out = append(out, rest)
				}
			}
		}
	}
	return out
}

// runConfirm is the Confirm for a run this turn starts on agentID's behalf (a
// delegated agent, a pipeline stage). With someone watching this turn it asks
// them, as the turn's own calls are asked; with nobody watching it is the
// owner's unattended gate, which queues the call for approval. These runs
// used to approve every call, so a credential that asks first, "confirm
// writes" and the owner's ask-before marks had no effect on them.
func (t *chatTurn) runConfirm(agentID string, sess *ToolSession, asks ...func(string) bool) func(name, args string) bool {
	if t != nil && t.sse != nil {
		return t.confirmFuncForAsks(sess, asks...)
	}
	if t == nil || t.app == nil {
		// No app to queue with: refuse rather than approve.
		return func(string, string) bool { return false }
	}
	_, owner := t.ownerView()
	if owner == "" {
		owner = t.user
	}
	return t.app.newAutonomousGate(owner, agentID, sess).confirm
}

// confirmFuncFor builds the AgentLoopConfig.Confirm hook for this
// turn's loops (orchestrator and workers share it). Policy: escalate
// ONLY when the tool dispatches through a credential whose admin
// toggle demands it; everything else auto-approves as before.
func (t *chatTurn) confirmFuncFor(sess *ToolSession) func(name, args string) bool {
	return t.confirmFuncForAsks(sess)
}

// confirmFuncForAsks is confirmFuncFor with the ask-before-every-call answer
// supplied: a run this turn started for another agent asks about THAT agent's
// marks (and its delegators', see asksFirst), not this turn's. None given is
// this turn's own.
func (t *chatTurn) confirmFuncForAsks(sess *ToolSession, asks ...func(string) bool) func(name, args string) bool {
	asksFirst := t.asksFirst
	if len(asks) > 0 && asks[0] != nil {
		asksFirst = asks[0]
	}
	return func(name, args string) bool {
		// The call its owner approved on a kept card, made by the turn that
		// answer woke: already asked (approval_resume.go).
		if takeResumePass(t.user, t.agent.ID, name, args) {
			return true
		}
		// A host-app tool that asked to be confirmed is asked about first:
		// it declared its own sentence, which is more specific than
		// anything the credential path would generate for the same call.
		if spec := t.appToolConfirmation(name); spec.Asks() && confirmationCoversCall(spec, args) {
			return t.confirmAppToolCall(name, spec, args)
		}
		// The TOOL's own declaration, before anything inferred from what it
		// dispatches through. This is the path for a tool with no credential
		// at all, which could not be confirmed by any route before: the
		// credential toggle was the only way in, so a shell tool that deletes
		// files had no way to ask and an api tool sharing a key with a benign
		// one could not differ from it.
		// By NAME, so this reaches the framework's own tools too. It used to
		// read a flag on the tool RECORD, which meant only a tool somebody had
		// authored could be marked - and the consequential ones, the searches
		// and the browsing and the fetches, carry no record. The tools most
		// worth stopping on were the only ones that could not be.
		// The OWNER's marks, not the runtime user's. A shared agent runs for
		// somebody else, and whether a tool stops to ask is the owner's
		// decision about their own tool, not a setting the visitor holds.
		// Two sources, either of which stops the call. The owner's marks
		// (asksFirst: this agent's, the fleet-wide one, and those of any agent
		// that handed this run over) are keyed by NAME and reach any tool, the
		// framework's included. The record's own flag is a tool DECLARING that
		// it asks, which tool_def can set on a draft that lives only in this
		// session and so never reaches the owner's store at all.
		tt := toolRecordFor(sess, name)
		if (tt != nil && tt.ConfirmInChat) || asksFirst(name) {
			// Already allowed for the rest of this conversation.
			key := t.sessionAllowKey(name)
			if sessionAllowed(key) {
				return true
			}
			return t.escalateToolConfirm(toolConfirmRequest{
				sessionKey: key,
				tool:       name,
				// What it would DO, not just its name. The card is asking
				// somebody to judge one call, and a name does not say whether
				// the tool reaches outside the deployment while a raw JSON
				// blob buries the one argument that decides it.
				prompt:  fmt.Sprintf("Allow %s to run?", name),
				detail:  confirmCallSummary(sess, name, args),
				because: "you set this tool to ask before every call",
				args:    args,
			})
		}
		tt, action := toolForCall(sess, name)
		writes := callMayWrite(tt, action, name, args)
		var cred string
		for _, cn := range credentialsForToolCall(sess, name) {
			if c, ok := Secure().Resolve(cn, t.user); ok && (c.RequiresConfirm || (c.ConfirmWrites && writes)) {
				cred = cn
				break
			}
		}
		if cred == "" {
			if why := reachPastHook(tt); why != "" {
				return t.confirmReach(sess, name, args, why)
			}
			return true
		}
		if c, ok := Secure().Resolve(cred, t.user); ok && !c.RequiresConfirm {
			// Asks before writes only, and this call writes.
			return t.escalateToolConfirm(toolConfirmRequest{
				tool:    name,
				prompt:  fmt.Sprintf("Allow %s to make a change through %q?", name, cred),
				detail:  args,
				args:    args,
				because: fmt.Sprintf("credential %q asks before any call that changes something", cred),
			})
		}
		// Resolve, not Load — the user's OWN credential shadows a global one and is
		// invisible to the global-namespace Load, which silently skipped the toggle
		// on every user-owned credential. Note this path stays OPEN on an
		// unresolvable name where the unattended gate fails CLOSED: a person is
		// watching here, and can deny.
		c, ok := Secure().Resolve(cred, t.user)
		if !ok || !c.RequiresConfirm {
			return true
		}
		return t.escalateToolConfirm(toolConfirmRequest{
			tool:    name,
			prompt:  fmt.Sprintf("Allow %s? Service %q requires approval for each call.", name, cred),
			detail:  args,
			args:    args,
			because: fmt.Sprintf("credential %q requires approval for each call", cred),
		})
	}
}

// appToolConfirmation returns the confirmation a host-app tool declared for
// this turn, or nil when the name is not one of them (or is one that did not
// ask to be confirmed).
//
// Scoped to t.appTools deliberately. These are the tools the dispatching app
// built for THIS turn, so a name resolves to the definition that actually ran
// — there is no chance of matching a same-named tool from somebody else's
// pool and prompting, or not prompting, on the strength of a collision.
func (t *chatTurn) appToolConfirmation(name string) *ToolConfirmation {
	if t == nil {
		return nil
	}
	for _, td := range t.appTools {
		if td.Tool.Name == name {
			return td.Confirmation
		}
	}
	return frameworkToolConfirmations[name]
}

// frameworkToolConfirmations are the questions the framework's OWN tools ask
// before they run. The agent loop escalates any call whose definition carries
// a confirmation, but the web hook only looked the question up among the
// host app's tools, so one declared on a framework tool was never put in
// front of anybody and the call simply ran. Matching by name is safe here
// where it would not be for app tools: every name in this map is reserved,
// so no custom tool can take it. Written at init, read-only after.
var frameworkToolConfirmations = map[string]*ToolConfirmation{}

// tool_def's delete is not in it. It asks through the change approval
// (ToolSession.ApproveChange), with what the delete costs and which agents
// lose the tool; asked here as well, every delete took two clicks.

// confirmationCoversCall reports whether a confirmation scoped to one action
// of a grouped tool applies to this call. Unscoped, it covers every call.
func confirmationCoversCall(spec *ToolConfirmation, args string) bool {
	if spec == nil || spec.Action == "" {
		return true
	}
	return strings.EqualFold(argFromPreview(args, "action"), spec.Action)
}

// confirmAppToolCall is the whole policy for one host-app tool call: a
// standing grant answers it silently, otherwise the user is asked, and their
// answer may become the next call's standing grant.
func (t *chatTurn) confirmAppToolCall(name string, spec *ToolConfirmation, args string) bool {
	argValue := argFromPreview(args, spec.GrantArg)

	// A grant the user already gave answers without interrupting them. This
	// is the entire point of the mechanism: the second `go build` in an
	// edit-build-fix loop must not stop for the same question the first one
	// already asked.
	if g, ok := findToolGrant(t.udb, spec.Scope, name, argValue); ok {
		t.noteGrantUsed(name, g)
		return true
	}

	req := toolConfirmRequest{
		tool:    name,
		prompt:  spec.Prompt,
		detail:  args,
		because: "the " + name + " tool requires approval for each call",
	}
	// Offer a standing grant only when the tool allows one AND the call is
	// one that can be described narrowly. A shell line the guard refuses is
	// asked about every time, which is the correct answer for a command that
	// does more than one thing.
	if spec.CanRemember() {
		if spec.GrantArg == "" {
			req.grant = &ToolGrant{Scope: spec.Scope, Tool: name}
			req.grantLabel = "Always allow " + name
		} else if prefix := GrantPrefixFor(argValue); prefix != "" {
			req.grant = &ToolGrant{Scope: spec.Scope, Tool: name, Prefix: prefix}
			req.grantLabel = "Always allow " + prefix + "…"
		}
	}
	if req.grant != nil {
		req.grant.Label = req.grantLabel
	}
	return t.escalateToolConfirm(req)
}

// noteGrantUsed says in the conversation that a call went through on a
// standing approval rather than silently.
//
// A grant that works invisibly is indistinguishable from a gate that stopped
// working, and the difference matters the first time somebody wonders why
// they were not asked. One quiet line, not a card.
func (t *chatTurn) noteGrantUsed(name string, g ToolGrant) {
	if t == nil || t.sse == nil {
		return
	}
	what := name
	if g.Prefix != "" {
		what = g.Prefix
	}
	t.sse.Send(map[string]any{"kind": "status_note",
		"text": "✓ " + what + " ran on a standing approval you gave. Manage it under Permissions."})
}

// argFromPreview pulls one argument's value out of the formatted argument
// string the loop hands the confirm hook.
//
// The hook receives arguments already rendered for display, not the map, so
// this reads the value back out of that rendering. It is best-effort by
// nature: a value it cannot find yields "", which makes the call ungrantable
// and therefore asked about — the safe direction for a parse that failed.
func argFromPreview(preview, argName string) string {
	if argName == "" {
		return ""
	}
	for _, line := range strings.Split(preview, "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, argName+":")
		if !ok {
			rest, ok = strings.CutPrefix(line, argName+"=")
		}
		if ok {
			return strings.TrimSpace(strings.Trim(strings.TrimSpace(rest), "\"'"))
		}
	}
	return ""
}

// toolConfirmRequest is one escalation's copy: what is being asked about, the
// question the user reads, the arguments shown beneath it, and the phrase that
// explains the stop in a log line or a turn diagnostic.
//
// A struct rather than four positional strings because three of the four are
// prose and swapping two of them produces a card that still renders — a bug
// nobody would see until a user was staring at the wrong question.
type toolConfirmRequest struct {
	tool    string
	prompt  string
	detail  string
	because string
	// grant, when set, is the standing approval this card OFFERS as its
	// third button. Nil means the card is once-or-deny, either because the
	// tool withheld the option or because the call was too broad to
	// describe narrowly.
	grant      *ToolGrant
	grantLabel string
	// sessionKey, when set, offers "Allow for this session": a yes that
	// covers this tool for the rest of the conversation (ask_first.go).
	sessionKey string
	// args is the call's own arguments (JSON), kept with the answer in the
	// saved conversation (recordApproval). Empty for a question that is not
	// about one call.
	args string
}

// escalateToolConfirm renders the approval card and parks until the
// session owner answers. Returns false (deny) when no interactive
// viewer is attached, on timeout, or on an explicit Deny.
func (t *chatTurn) escalateToolConfirm(req toolConfirmRequest) bool {
	actions := []map[string]any{{"label": "Allow once", "value": "allow"}}
	if req.sessionKey != "" {
		actions = append(actions, map[string]any{"label": "Allow for this session", "value": "session"})
	}
	// The standing-grant button sits between allow-once and deny, and says
	// what it would allow rather than "always" on its own — the user is
	// agreeing to a specific future, so the button has to name it.
	if req.grant != nil {
		actions = append(actions, map[string]any{"label": req.grantLabel, "value": "always"})
	}
	actions = append(actions, map[string]any{"label": "Deny", "value": "deny", "variant": "danger"})
	picked, ok := t.parkConfirm(req, actions, map[string]bool{"allow": true, "always": true, "session": true})
	if !ok {
		return false
	}
	if picked == "session" && req.sessionKey != "" {
		allowForSession(req.sessionKey)
		t.turnDiag("tool-grant", fmt.Sprintf("You allowed %s for the rest of this session.", req.tool))
	}
	// Persist the standing grant only when the user picked THAT button.
	if picked == "always" && req.grant != nil {
		saved := saveToolGrant(t.udb, *req.grant)
		t.turnDiag("tool-grant", fmt.Sprintf("You allowed %s from now on in this scope. Revoke it under Permissions.", firstNonBlank(saved.Label, req.tool)))
	}
	return true
}

// askInChat puts a question card in the conversation and parks until the
// person answers: one button per entry in yes, then Deny. Returns the label of
// the yes they picked, or "" for Deny, a timeout, or no one watching, so ""
// always means "do not". The generic form of escalateToolConfirm, for a tool
// whose question is not "may this call run" (ToolSession.AskInChat).
func (t *chatTurn) askInChat(prompt, detail string, yes []string) string {
	actions := make([]map[string]any, 0, len(yes)+1)
	allows := make(map[string]bool, len(yes))
	for i, label := range yes {
		v := fmt.Sprintf("yes%d", i)
		actions = append(actions, map[string]any{"label": label, "value": v})
		allows[v] = true
	}
	actions = append(actions, map[string]any{"label": "Deny", "value": "deny", "variant": "danger"})
	picked, ok := t.parkConfirm(toolConfirmRequest{tool: "a question", prompt: prompt, detail: detail, because: prompt}, actions, allows)
	if !ok {
		return ""
	}
	for i, label := range yes {
		if picked == fmt.Sprintf("yes%d", i) {
			return label
		}
	}
	return ""
}

// parkConfirm renders a card with actions and parks until it is answered.
// Returns the picked value and whether it is a yes (allows, or allow/always
// when nil). False with no viewer attached, on timeout, or on a no; each of
// those leaves a breadcrumb and settles the card in the event stream.
func (t *chatTurn) parkConfirm(req toolConfirmRequest, actions []map[string]any, allows map[string]bool) (string, bool) {
	// A sub-run with no viewer of its own, started from a chat somebody is
	// watching (agents(run) inline): the question goes to that chat, as it
	// would if the person had asked for this directly, instead of being
	// refused because nobody watches the sub-run.
	if t != nil && t.sse == nil && t.askVia != nil {
		return t.parkConfirmVia(req, actions, allows)
	}
	if t == nil || t.sse == nil {
		Log("[orchestrate.confirm] %s stopped: %s, and this run has no interactive viewer, denied (fail closed)", req.tool, req.because)
		if t != nil {
			t.turnDiag("tool-denied", fmt.Sprintf("%s was not run: %s, and this run had no interactive viewer to ask, denied fail-closed.", req.tool, req.because))
			t.recordApproval(req, "Denied: nobody was watching to ask")
		}
		return "", false
	}
	detail := req.detail
	detail = textutil.Clip(detail, 600)
	id := "toolconfirm-" + UUIDv4()[:8]
	p := &pendingToolConfirm{user: t.user, ch: make(chan bool, 1), answer: make(chan string, 1), allows: allows}
	toolConfirms.Store(id, p)
	defer toolConfirms.Delete(id)

	t.sse.Send(map[string]any{
		"kind":    "confirm",
		"id":      id,
		"prompt":  req.prompt,
		"detail":  detail,
		"actions": actions,
	})
	// The card lands below the bubble this round has open. A round that only
	// called a tool leaves that bubble open, and the next round's text and
	// tool calls were written into it, ABOVE the card they came after. Closed
	// here, so what follows opens a fresh bubble below the card. A round that
	// streamed text keeps its own id and is finalized as usual.
	t.setCurrentMsgID("")
	select {
	case v := <-p.ch:
		if !v {
			t.turnDiag("tool-denied", fmt.Sprintf("You denied the %s call (%s).", req.tool, req.because))
			t.sendConfirmResolved(id, false)
			t.recordApproval(req, "Denied")
			return "", false
		}
		// Reading the answer non-blockingly: the resolver always writes it
		// before signalling, but a nil-answer pending (an older in-flight
		// card across a rebuild) must not park the loop forever.
		var picked string
		select {
		case picked = <-p.answer:
		default:
		}
		// The stamp is the button's own label, except a plain allow, which
		// has always stamped "Allowed".
		label := "Allowed"
		for _, a := range actions {
			if a["value"] == picked && picked != "allow" {
				label = fmt.Sprint(a["label"])
			}
		}
		t.sendConfirmResolvedLabel(id, "allow", label)
		t.recordApproval(req, label)
		return picked, true
	case <-time.After(toolConfirmTimeout):
		// A call with a conversation to keep a card in waits for its answer
		// instead of being denied (approval_resume.go).
		if t.keepForLater(req, id) {
			Log("[orchestrate.confirm] approval for %s (%s) not answered in %s: kept for later", req.tool, req.because, toolConfirmTimeout)
			return "", false
		}
		Log("[orchestrate.confirm] approval for %s (%s) timed out after %s: denied", req.tool, req.because, toolConfirmTimeout)
		// Breadcrumb + a persistent in-conversation note. The silent version
		// of this deny is exactly the "said 'go for it', got one sentence,
		// then five minutes of dead air" incident: the approval card sat
		// unanswered, the timeout killed the call, and nothing on screen
		// said why. The user should never have to ask "what happened?".
		t.turnDiag("tool-denied", fmt.Sprintf("Approval for %s (%s) timed out after %s: the call was denied. Re-ask to retry; the approval card must be answered within the window.", req.tool, req.because, toolConfirmTimeout))
		t.sse.Send(map[string]any{"kind": "status_note",
			"text": fmt.Sprintf("⏱ Approval for %s timed out after %s, so the call was denied.", req.tool, toolConfirmTimeout)})
		t.sendConfirmResolvedLabel(id, "deny", "Timed out")
		t.recordApproval(req, "Timed out: denied")
		return "", false
	}
}

// recordApproval files an approval card's answer with the turn's tool runs, so
// the saved conversation still says what was asked, with what, and how it was
// answered once the card is gone: the card lives in the run's event stream,
// which ends with the run or a restart. A framework record, never shown to the
// model as a call it made.
func (t *chatTurn) recordApproval(req toolConfirmRequest, outcome string) {
	if t == nil {
		return
	}
	args := parseCallArgs(req.args)
	if len(args) == 0 && strings.TrimSpace(req.detail) != "" {
		args = map[string]any{"detail": truncateObs(req.detail, 600)}
	}
	prompt := strings.TrimSpace(req.prompt)
	if prompt == "" {
		prompt = "Allow " + req.tool + "?"
	}
	t.recordToolCall(toolCallRecord{Name: "approval", Label: "Approval: " + prompt, Args: args, Result: outcome, Framework: true})
}

// sendConfirmResolved records the answer IN THE EVENT STREAM, which is the
// only place a replay can learn it.
//
// The card's own settling is DOM-deep: the browser stamps ✓/✕ and moves on.
// But the frames of a live run are buffered for reconnect, and the resolving
// POST lands on a different request that never touches that buffer — so a
// reload mid-run replayed the escalation card with its buttons armed again,
// over a call the user had already allowed. Clicking it a second time then
// answered an id nothing was waiting on. Emitting the outcome as its own
// frame keeps the buffer a truthful record of the turn: whoever replays it
// sees the question AND the answer.
func (t *chatTurn) sendConfirmResolved(id string, allowed bool) {
	label := "Denied"
	value := "deny"
	if allowed {
		label, value = "Allowed", "allow"
	}
	t.sendConfirmResolvedLabel(id, value, label)
}

// sendConfirmResolvedLabel is sendConfirmResolved with the stamp spelled out —
// for outcomes the user didn't choose (a timeout denies without a click, and
// saying "Denied" for it would misattribute the decision).
func (t *chatTurn) sendConfirmResolvedLabel(id, value, label string) {
	if t == nil || t.sse == nil || id == "" {
		return
	}
	t.sse.Send(map[string]any{"kind": "confirm_resolved", "id": id, "value": value, "label": label})
}

// confirmAnswer is a confirm card's answer: which card, which button.
type confirmAnswer struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

// answerToolConfirm is the /api/confirm POST body's landing: the
// chat panel's confirm card submits {id, value}. Owner-checked — only
// the user whose turn parked the escalation can resolve it. Unknown
// ids answer 204 silently (plan-card confirms and stale cards POST
// here too; they have nothing to resolve).
func (T *OrchestrateApp) answerToolConfirm(c *jsonapi.Call, in confirmAnswer) (any, error) {
	id := strings.TrimSpace(in.ID)
	v, found := toolConfirms.Load(id)
	if !found {
		return jsonapi.Empty(http.StatusNoContent), nil
	}
	p := v.(*pendingToolConfirm)
	if p.user != c.User {
		return nil, jsonapi.Forbidden("not your approval")
	}
	// Delete before signalling so a double-click can't send twice
	// (both channels are buffered 1; the waiter also deletes on its way out).
	toolConfirms.Delete(id)
	p.resolve(strings.TrimSpace(in.Value))
	return jsonapi.Empty(http.StatusNoContent), nil
}

// resolve hands the parked waiter the button that was clicked.
func (p *pendingToolConfirm) resolve(value string) {
	// The answer goes FIRST: the waiter reads it non-blockingly the instant
	// ch releases it, so writing them the other way round would race a
	// standing grant into being dropped as an allow-once.
	if p.answer != nil {
		p.answer <- value
	}
	allowed := value == "allow" || value == "always"
	if p.allows != nil {
		allowed = p.allows[value]
	}
	p.ch <- allowed
}

// PublicHandleConfirm is the landing an app routes its AgentLoopPanel's
// ConfirmURL to, so an approval card raised inside an app's own chat can be
// answered there.
//
// Safe to expose without an app-side check of its own: the resolution is
// owner-checked against the user who parked the escalation, and a card id
// nobody is waiting on answers 204. So the worst an unrelated caller can do
// is resolve nothing.
func (T *OrchestrateApp) PublicHandleConfirm(w http.ResponseWriter, r *http.Request) {
	jsonapi.Post(T.DB, T.answerToolConfirm)(w, r)
}

// firstNonBlank returns the first argument that isn't empty after trimming.
func firstNonBlank(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// parkConfirmVia asks a confirmation through the dispatching chat's question
// card (chatTurn.askVia): one button per action but Deny, mapped back to the
// action's value. Same answers as parkConfirm: the value picked, and whether
// it is a yes.
func (t *chatTurn) parkConfirmVia(req toolConfirmRequest, actions []map[string]any, allows map[string]bool) (string, bool) {
	var labels []string
	values := map[string]string{}
	for _, a := range actions {
		v, _ := a["value"].(string)
		l, _ := a["label"].(string)
		if v == "" || v == "deny" || l == "" {
			continue
		}
		labels = append(labels, l)
		values[l] = v
	}
	prompt := req.prompt
	if prompt == "" {
		prompt = "Allow " + req.tool + "?"
	}
	picked := values[t.askVia(prompt, req.detail, labels)]
	if picked == "" {
		t.turnDiag("tool-denied", fmt.Sprintf("%s was not run: %s, and the person watching said no (or did not answer).", req.tool, req.because))
		return "", false
	}
	if allows != nil {
		return picked, allows[picked]
	}
	return picked, picked == "allow" || picked == "always"
}

// reachPastHook says what a tool can do outside the audited hook, or "" when
// nothing: raw network (a persistent shell always has it) and a key handed to
// its script through secret:<name>. Such a tool can send what it holds
// anywhere its network reaches, so in a chat it asks the person watching
// before its first call. A credential that asks on every call keeps its own
// card, which is checked first; unattended runs are the autonomous gate's.
func reachPastHook(tt *TempTool) string {
	if tt == nil {
		return ""
	}
	var keys []string
	for _, c := range tt.HookCapabilities {
		if rest := strings.TrimPrefix(c, "secret:"); rest != c && strings.TrimSpace(rest) != "" {
			keys = append(keys, strings.TrimSpace(rest))
		}
	}
	handed := "is handed the key " + quotedList(keys)
	if len(keys) > 1 {
		handed = "is handed the keys " + quotedList(keys)
	}
	network := tt.RawNetwork || tt.Mode == TempToolModePersistent
	switch {
	case network && len(keys) > 0:
		return "has raw network access and " + handed
	case network:
		return "has raw network access"
	case len(keys) > 0:
		return handed
	}
	return ""
}

// quotedList is names quoted and joined: "a", "a" and "b", "a", "b" and "c".
func quotedList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = fmt.Sprintf("%q", n)
	}
	if len(q) < 2 {
		return strings.Join(q, "")
	}
	return strings.Join(q[:len(q)-1], ", ") + " and " + q[len(q)-1]
}

// confirmReach asks about a tool that reaches past the hook, once per
// conversation: "Allow for this session" covers its later calls.
func (t *chatTurn) confirmReach(sess *ToolSession, name, args, why string) bool {
	key := t.sessionAllowKey(name)
	if sessionAllowed(key) {
		return true
	}
	return t.escalateToolConfirm(toolConfirmRequest{
		sessionKey: key,
		tool:       name,
		prompt:     fmt.Sprintf("Allow %s? It %s.", name, why),
		detail:     confirmCallSummary(sess, name, args),
		because:    "it " + why,
		args:       args,
	})
}
