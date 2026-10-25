// Letting a host app's tools put a CARD in the conversation: the turn's half
// of agenthost.Publisher, which says why it exists.
package orchestrate

import (
	"net/http"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
)

// AppBlockPublisher is agenthost.Publisher, which an app's tools publish cards
// through.
type AppBlockPublisher = agenthost.Publisher

// bindPublisher attaches p to a turn. Called by the dispatch path once the
// turn exists; the app never names the turn, it only publishes. What the app
// publishes is a card; what the session keeps is a UIBlock.
func bindPublisher(p *AppBlockPublisher, t *chatTurn) {
	p.Bind(func(b agenthost.Block) {
		t.publishAppBlock(UIBlock{Type: b.Type, ID: b.ID, Title: b.Title, Text: b.Text, HTML: b.HTML, URL: b.URL, Data: b.Data})
	})
}

// publishAppBlock is the turn-side half: send it live, then persist it.
func (t *chatTurn) publishAppBlock(blk UIBlock) {
	if t == nil {
		return
	}
	if t.sse != nil {
		payload := map[string]any{
			"kind":  "block",
			"type":  blk.Type,
			"id":    blk.ID,
			"title": blk.Title,
			"text":  blk.Text,
		}
		if len(blk.Data) > 0 {
			payload["data"] = blk.Data
		}
		if blk.HTML != "" {
			payload["html"] = blk.HTML
		}
		if blk.URL != "" {
			payload["url"] = blk.URL
		}
		t.sse.Send(payload)
	}
	if t.session != nil {
		t.toolMu.Lock()
		t.session.upsert_ui_block(blk, nil)
		t.toolMu.Unlock()
	}
}

// PublicHandleSendWithAppToolsPublishing is PublicHandleSendWithAppTools for
// an app whose tools publish cards. The publisher is bound to this run's turn
// before any tool can fire, so a card emitted by the first tool call in the
// first round still reaches the browser.
func (T *OrchestrateApp) PublicHandleSendWithAppToolsPublishing(w http.ResponseWriter, r *http.Request, agent agenthost.Agent, appTools []AgentToolDef, pub *AppBlockPublisher) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleSendWithAppToolsPublishing(w, r, udb, user, appRecord(agent), appTools, pub)
}
