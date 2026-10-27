// The ask-before-every-call mark, read and written from the Tools modal.
//
// Sits beside admin_tool_scope.go's /api/tool-scope: Scope answers "where is
// this available", this answers "does this agent stop and ask first". The
// mark is per agent and keyed by name, so it reaches framework tools too.
//
// The console Permissions page writes the same flag through
// handleConsolePermissionPolicy's "confirmtool:" kind. Two routes, one setter:
// both call core.SetUserToolConfirmInChat and neither derives the state, which
// is the trap the earlier per-tool ladder fell into.
package orchestrate

import (
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// toolConfirmGet serves the Ask control in the Tools modal: the name to flag
// for this agent of every tool the user owns, and of every tool marked to ask,
// the framework's included (a mark is keyed by name and needs no record). The
// modal offers the control on every row; a name it does not find here does
// not ask.
func (T *OrchestrateApp) toolConfirmGet(c *jsonapi.Call) (any, error) {
	tools := map[string]bool{}
	agentID := strings.TrimSpace(c.R.URL.Query().Get("agent"))
	asks := map[string]bool{}
	for _, n := range AskInChatTools(AuthDB(), c.User, agentID) {
		asks[n] = true
	}
	for _, p := range LoadPersistentTempTools(AuthDB(), c.User) {
		tools[p.Tool.Name] = asks[p.Tool.Name]
	}
	for n := range asks {
		tools[n] = true
	}
	// No caching, for the reason /api/tool-scope gives: the modal re-reads
	// right after a write and a stale body snaps the control back.
	c.W.Header().Set("Cache-Control", "no-store")
	return map[string]any{"tools": tools}, nil
}

// toolConfirmSet flips one: {"name":…,"on":…}.
func (T *OrchestrateApp) toolConfirmSet(c *jsonapi.Call, body struct {
	Name string `json:"name"`
	On   bool   `json:"on"`
}) (any, error) {
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" {
		return nil, jsonapi.BadRequest("name required")
	}
	// Scoped to this user's own pool by construction: the setter is handed
	// the requesting account, so a name belonging to somebody else simply
	// is not found rather than being edited.
	// Agent-scoped, like every other permission. The mark is keyed by
	// name so it needs no record, which is what lets a framework tool
	// carry one too.
	if !SetUserToolAsksInChat(AuthDB(), c.User, strings.TrimSpace(c.R.URL.Query().Get("agent")), body.Name, body.On) {
		return nil, jsonapi.Status(http.StatusInternalServerError, "could not record that")
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}
