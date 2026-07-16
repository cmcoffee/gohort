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
		cred := credentialForToolCall(sess, name)
		if cred == "" {
			return true
		}
		c, ok := Secure().Load(cred)
		if !ok || !c.RequiresConfirm {
			return true
		}
		return t.escalateToolConfirm(name, cred, args)
	}
}

// escalateToolConfirm renders the approval card and parks until the
// session owner answers. Returns false (deny) when no interactive
// viewer is attached, on timeout, or on an explicit Deny.
func (t *chatTurn) escalateToolConfirm(toolName, cred, argsPreview string) bool {
	if t == nil || t.sse == nil {
		Log("[orchestrate.confirm] %s via credential %q requires approval but this run has no interactive viewer — denied (fail closed)", toolName, cred)
		return false
	}
	if len(argsPreview) > 600 {
		argsPreview = argsPreview[:600] + "…"
	}
	id := "toolconfirm-" + UUIDv4()[:8]
	p := &pendingToolConfirm{user: t.user, ch: make(chan bool, 1)}
	toolConfirms.Store(id, p)
	defer toolConfirms.Delete(id)
	t.sse.Send(map[string]any{
		"kind":   "confirm",
		"id":     id,
		"prompt": fmt.Sprintf("Allow %s? Service %q requires approval for each call.", toolName, cred),
		"detail": argsPreview,
		"actions": []map[string]any{
			{"label": "Allow once", "value": "allow"},
			{"label": "Deny", "value": "deny", "variant": "danger"},
		},
	})
	select {
	case v := <-p.ch:
		return v
	case <-time.After(toolConfirmTimeout):
		Log("[orchestrate.confirm] approval for %s (credential %q) timed out after %s — denied", toolName, cred, toolConfirmTimeout)
		return false
	}
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
	// (ch is buffered 1; the waiter also deletes on its way out).
	toolConfirms.Delete(strings.TrimSpace(req.ID))
	p.ch <- (req.Value == "allow")
	w.WriteHeader(http.StatusNoContent)
}
