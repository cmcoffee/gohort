// Asking in the MCP client, per key.
//
// An action that asks to confirm before it runs is left out over MCP unless
// the key is set to ask in the client: then a client that can put a question
// to its person mid-call (MCP elicitation; Claude Code and Claude Desktop
// can) lists the action, and each call asks the app's own question there
// before it runs (apps/mcpserver/app_ask.go). Off by default, because a
// client could claim it can ask and answer yes itself; the key's owner turns
// it on for a client they know puts the question in front of them.
//
// The question can carry "Don't ask again": a yes kept for that key and that
// action alone. It is not a grant in gohort: the app's page and the owner's
// agents still ask. Turning asking off forgets the key's yeses, as revoking
// the key does.
package customapps

import (
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// mcpKeyAskTable holds, by key id, whether the key asks in the client and the
// actions it runs unasked.
const mcpKeyAskTable = "mcp_key_asks"

// mcpKeyAsk is one key's asking: on or off, and the confirm scopes
// (ToolConfirmation.Scope) its person said not to ask about again.
type mcpKeyAsk struct {
	Asks    bool     `json:"asks"`
	Unasked []string `json:"unasked,omitempty"`
}

// loadMCPKeyAsk is key id's asking, fresh per read (gob leaves a reused
// struct's old values in place).
func loadMCPKeyAsk(id string) mcpKeyAsk {
	var a mcpKeyAsk
	if RootDB != nil && id != "" {
		RootDB.Get(mcpKeyAskTable, id, &a)
	}
	return a
}

// saveMCPKeyAsk stores key id's asking, or drops the row when it is off: a
// key that does not ask keeps no yeses.
func saveMCPKeyAsk(id string, a mcpKeyAsk) {
	if RootDB == nil || id == "" {
		return
	}
	if !a.Asks {
		RootDB.Unset(mcpKeyAskTable, id)
		return
	}
	RootDB.Set(mcpKeyAskTable, id, a)
}

// MCPKeyAsks reports whether key id asks in the client before an action that
// confirms first.
func MCPKeyAsks(id string) bool { return loadMCPKeyAsk(id).Asks }

// SetMCPKeyAsks turns asking in the client on or off for key id. Off forgets
// the yeses it kept.
func SetMCPKeyAsks(id string, on bool) {
	a := loadMCPKeyAsk(id)
	a.Asks = on
	saveMCPKeyAsk(id, a)
}

// MCPRunsUnasked reports whether key id's person said not to ask again about
// the action whose confirm scope is scope.
func MCPRunsUnasked(id, scope string) bool {
	if scope == "" {
		return false
	}
	for _, s := range loadMCPKeyAsk(id).Unasked {
		if s == scope {
			return true
		}
	}
	return false
}

// RememberMCPYes keeps a yes for key id and the action whose confirm scope is
// scope, so it runs from that key unasked. Kept only while the key asks.
func RememberMCPYes(id, scope string) {
	a := loadMCPKeyAsk(id)
	if scope == "" || MCPRunsUnasked(id, scope) {
		return
	}
	a.Unasked = append(a.Unasked, scope)
	sort.Strings(a.Unasked)
	saveMCPKeyAsk(id, a)
}

// forgetMCPYeses drops the yeses key id kept, leaving it asking.
func forgetMCPYeses(id string) {
	a := loadMCPKeyAsk(id)
	a.Unasked = nil
	saveMCPKeyAsk(id, a)
}

// unaskedNames are the action names key id runs unasked: the last part of
// each confirm scope ("app-action:<owner>/<slug>/<action>").
func unaskedNames(id string) []string {
	var out []string
	for _, s := range loadMCPKeyAsk(id).Unasked {
		if i := strings.LastIndex(s, "/"); i >= 0 {
			s = s[i+1:]
		}
		out = append(out, s)
	}
	return out
}
