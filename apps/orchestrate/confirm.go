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
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// toolConfirmTimeout is how long an escalated call waits for the
// owner's Allow/Deny before denying. Long enough to read the card and
// think; short enough that an abandoned session doesn't pin a
// goroutine forever.
const toolConfirmTimeout = 5 * time.Minute

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
func credentialForToolCall(sess *ToolSession, name string) string {
	if sess != nil {
		for _, tt := range sess.CopyTempTools() {
			if tt.Name == name {
				return strings.TrimSpace(tt.Credential)
			}
		}
	}
	if rest := strings.TrimPrefix(name, bridgeCredToolPrefix); rest != name {
		return rest
	}
	if rest := strings.TrimPrefix(name, "fetch_url_"); rest != name {
		return rest
	}
	return ""
}

// confirmFuncFor builds the AgentLoopConfig.Confirm hook for this
// turn's loops (orchestrator and workers share it). Policy: escalate
// ONLY when the tool dispatches through a credential whose admin
// toggle demands it; everything else auto-approves as before.
func (t *chatTurn) confirmFuncFor(sess *ToolSession) func(name, args string) bool {
	return func(name, args string) bool {
		// A host-app tool that asked to be confirmed is asked about first:
		// it declared its own sentence, which is more specific than
		// anything the credential path would generate for the same call.
		if spec := t.appToolConfirmation(name); spec.Asks() {
			return t.confirmAppToolCall(name, spec, args)
		}
		cred := credentialForToolCall(sess, name)
		if cred == "" {
			return true
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
	return nil
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
}

// escalateToolConfirm renders the approval card and parks until the
// session owner answers. Returns false (deny) when no interactive
// viewer is attached, on timeout, or on an explicit Deny.
func (t *chatTurn) escalateToolConfirm(req toolConfirmRequest) bool {
	if t == nil || t.sse == nil {
		Log("[orchestrate.confirm] %s stopped: %s, and this run has no interactive viewer — denied (fail closed)", req.tool, req.because)
		if t != nil {
			t.turnDiag("tool-denied", fmt.Sprintf("%s was not run: %s, and this run had no interactive viewer to ask — denied fail-closed.", req.tool, req.because))
		}
		return false
	}
	detail := req.detail
	if len(detail) > 600 {
		detail = detail[:600] + "…"
	}
	id := "toolconfirm-" + UUIDv4()[:8]
	p := &pendingToolConfirm{user: t.user, ch: make(chan bool, 1), answer: make(chan string, 1)}
	toolConfirms.Store(id, p)
	defer toolConfirms.Delete(id)

	actions := []map[string]any{{"label": "Allow once", "value": "allow"}}
	// The standing-grant button sits between allow-once and deny, and says
	// what it would allow rather than "always" on its own — the user is
	// agreeing to a specific future, so the button has to name it.
	if req.grant != nil {
		actions = append(actions, map[string]any{"label": req.grantLabel, "value": "always"})
	}
	actions = append(actions, map[string]any{"label": "Deny", "value": "deny", "variant": "danger"})

	t.sse.Send(map[string]any{
		"kind":    "confirm",
		"id":      id,
		"prompt":  req.prompt,
		"detail":  detail,
		"actions": actions,
	})
	select {
	case v := <-p.ch:
		if !v {
			t.turnDiag("tool-denied", fmt.Sprintf("You denied the %s call (%s).", req.tool, req.because))
			t.sendConfirmResolved(id, false)
			return false
		}
		// Persist the standing grant only when the user picked THAT button.
		// Reading the answer non-blockingly: the resolver always writes it
		// before signalling, but a nil-answer pending (an older in-flight
		// card across a rebuild) must not park the loop forever.
		var picked string
		select {
		case picked = <-p.answer:
		default:
		}
		if picked == "always" && req.grant != nil {
			saved := saveToolGrant(t.udb, *req.grant)
			t.sendConfirmResolvedLabel(id, "allow", req.grantLabel)
			t.turnDiag("tool-grant", fmt.Sprintf("You allowed %s from now on in this scope. Revoke it under Permissions.", firstNonBlank(saved.Label, req.tool)))
			return true
		}
		t.sendConfirmResolved(id, true)
		return true
	case <-time.After(toolConfirmTimeout):
		Log("[orchestrate.confirm] approval for %s (%s) timed out after %s — denied", req.tool, req.because, toolConfirmTimeout)
		// Breadcrumb + a persistent in-conversation note. The silent version
		// of this deny is exactly the "said 'go for it', got one sentence,
		// then five minutes of dead air" incident: the approval card sat
		// unanswered, the timeout killed the call, and nothing on screen
		// said why. The user should never have to ask "what happened?".
		t.turnDiag("tool-denied", fmt.Sprintf("Approval for %s (%s) timed out after %s — the call was denied. Re-ask to retry; the approval card must be answered within the window.", req.tool, req.because, toolConfirmTimeout))
		t.sse.Send(map[string]any{"kind": "status_note",
			"text": fmt.Sprintf("⏱ Approval for %s timed out after %s — the call was denied.", req.tool, toolConfirmTimeout)})
		t.sendConfirmResolvedLabel(id, "deny", "Timed out")
		return false
	}
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

// resolveToolConfirm is the /api/confirm POST body's landing: the
// chat panel's confirm card submits {id, value}. Owner-checked — only
// the user whose turn parked the escalation can resolve it. Unknown
// ids answer 204 silently (plan-card confirms and stale cards POST
// here too; they have nothing to resolve).
func (T *OrchestrateApp) resolveToolConfirm(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var req struct {
		ID    string `json:"id"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	v, found := toolConfirms.Load(strings.TrimSpace(req.ID))
	if !found {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	p := v.(*pendingToolConfirm)
	if p.user != user {
		http.Error(w, "not your approval", http.StatusForbidden)
		return
	}
	// Delete before signalling so a double-click can't send twice
	// (both channels are buffered 1; the waiter also deletes on its way out).
	toolConfirms.Delete(strings.TrimSpace(req.ID))
	value := strings.TrimSpace(req.Value)
	// The answer goes FIRST: the waiter reads it non-blockingly the instant
	// ch releases it, so writing them the other way round would race a
	// standing grant into being dropped as an allow-once.
	if p.answer != nil {
		p.answer <- value
	}
	p.ch <- (value == "allow" || value == "always")
	w.WriteHeader(http.StatusNoContent)
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
	T.resolveToolConfirm(w, r)
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
