// Letting a host app's tools put a CARD in the conversation.
//
// An app supplies tools to a run through PublicHandleSendWithAppTools, and
// those tools return text. Text is enough for a tool that reports a fact and
// wrong for a tool whose result the user needs to LOOK at and act on — a diff
// to review and revert, a generated table, a preview. Those already have a
// shape in this codebase: a {kind:"block"} SSE event with an app-registered
// renderer, persisted on the session so it survives a reload. Until now only
// orchestrate's own tools could emit one, because emitting needs the turn's
// SSE writer and session, and neither exists when the app builds its tools.
//
// The publisher closes that gap without handing an app the turn. The app
// makes one, closes its tool handlers over Publish, and passes it alongside
// its tools; orchestrate binds it to the turn just before the run starts.
// Publish before binding — or on a run with no live stream at all, like a
// headless dispatch — is a no-op, which is the behavior an app wants: the
// card is an enhancement to a tool result, never the tool result itself, so
// a tool must still say in words what it did.
//
// This is deliberately the whole surface. An app gets to publish a block; it
// does not get the SSE writer, the session, or anything else about the turn.
package orchestrate

import (
	"net/http"
	"sync"

	. "github.com/cmcoffee/gohort/core"
)

// AppBlockPublisher carries a host app's block emissions into a live turn.
//
// The zero value is usable and inert: an app may always build its tools over
// one, and whether anything is displayed is then a property of how the run
// was dispatched rather than a branch every tool has to write.
type AppBlockPublisher struct {
	mu   sync.Mutex
	turn *chatTurn
}

// Publish emits blk into the bound turn — live to the browser, and onto the
// session so a reload replays it. Blocks are upserted by ID, so a tool that
// re-publishes the same ID updates its card rather than stacking a new one.
//
// Safe to call from any goroutine, including a tool handler's, and safe to
// call on an unbound or nil publisher.
func (p *AppBlockPublisher) Publish(blk UIBlock) {
	if p == nil || blk.Type == "" {
		return
	}
	p.mu.Lock()
	t := p.turn
	p.mu.Unlock()
	if t == nil {
		return
	}
	t.publishAppBlock(blk)
}

// bind attaches the publisher to a turn. Called by the dispatch path once the
// turn exists; not exported, because an app has no business naming a turn.
func (p *AppBlockPublisher) bind(t *chatTurn) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.turn = t
	p.mu.Unlock()
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
func (T *OrchestrateApp) PublicHandleSendWithAppToolsPublishing(w http.ResponseWriter, r *http.Request, agent AgentRecord, appTools []AgentToolDef, pub *AppBlockPublisher) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleSendWithAppToolsPublishing(w, r, udb, user, agent, appTools, pub)
}
