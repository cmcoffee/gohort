// LLM-facing open_session tool — lets the agent spin a topic off into a
// fresh chat session and hand the user a link to continue there. The
// seeded opening note is what makes the handoff feel continuous: the new
// thread arrives already carrying the context, instead of starting cold.
//
// Registered ONLY on the interactive web chat paths (runPlan /
// runWorkerStep, beside `recurring`) — never on channel relays, dispatch
// runs, or scheduled fires (they assemble tools via dispatchExtraTools,
// which excludes it). A channel counterparty is texting from
// iMessage/Telegram: a web deep link is useless there at best and leaks
// surface details at worst. The handler's sse guard is the belt to that
// structural suspenders.

package orchestrate

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func (t *chatTurn) openSessionToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "open_session",
			Description: "Create a NEW chat session with this agent and give the user a link to continue there. Use when a topic deserves its own thread — a deep-dive that would bloat this conversation, a project that will run for weeks, a handoff (\"I've set up a session for the trip planning\"). " +
				"The seed_note becomes the new session's opening message FROM YOU: write it as a proper handoff carrying over the key context (decisions made, constraints, where things stand), so the new thread starts warm instead of cold. " +
				"OFFER, never yank: present the returned url to the user as a markdown link they can click when ready. Do NOT treat the new session as active — this conversation continues here until the user switches. " +
				"Pairs with recurring: to give a schedule's reports their own home, open a session for them and then recurring(action=\"move\", to=\"session\", session_id=<the returned id>).",
			Parameters: map[string]ToolParam{
				"title":     {Type: "string", Description: "The new session's title, as it appears in the sessions rail (e.g. \"Trip planning — Portugal\")."},
				"seed_note": {Type: "string", Description: "Your opening message in the new session: a handoff note carrying over the relevant context from this conversation. Written to the new thread verbatim."},
			},
			Required: []string{"title", "seed_note"},
			Caps:     []Capability{CapWrite},
		},
		Handler: func(args map[string]any) (string, error) {
			// Web-surface guard: no live stream = this turn isn't a browser
			// user who can click the link (channel relay, dispatch, scheduled
			// fire — none of which should ever see this tool anyway).
			if t.sse == nil {
				return "", errors.New("open_session is only available on the interactive web chat")
			}
			title := strings.TrimSpace(stringArg(args, "title"))
			seed := strings.TrimSpace(stringArg(args, "seed_note"))
			if title == "" || seed == "" {
				return "", errors.New("title and seed_note are both required")
			}
			now := time.Now()
			sess := ChatSession{
				ID:      UUIDv4(),
				AgentID: t.agent.ID,
				Title:   title,
				Created: now,
				LastAt:  now,
				Messages: []ChatMessage{{
					Role:    "assistant",
					Content: seed,
					Created: now,
				}},
			}
			if _, err := saveChatSession(t.udb, sess); err != nil {
				return "", fmt.Errorf("could not create the session: %w", err)
			}
			// Relative to the chat surface, so it resolves for whatever path
			// this deployment serves the page on.
			link := "?agent=" + url.QueryEscape(t.agent.ID) + "&session=" + url.QueryEscape(sess.ID)
			return fmt.Sprintf("OPENED_OK id=%s — new session %q created with your seed note as its opening message. Give the user this link as markdown so they can continue there when ready: [%s](%s). Do not switch this conversation; they click through on their own.",
				sess.ID, title, title, link), nil
		},
	}
}
