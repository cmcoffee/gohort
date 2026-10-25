package agenthost

// An app whose conversation runs on a hosted agent: the agent as the app holds
// it for a turn, the routes its chat panel calls, and the cards its tools put
// in the conversation.
//
// Scribe, Anvil and the other apps that put an agent behind their chat each
// wired the panel by hand, and each picked a different subset: edit and retry
// on a message, rename, rejoining a turn after a reload, tool confirmations,
// question cards, the guard notices. Every one of those is a URL on the panel,
// and a URL left off is a control that is not there, with nothing to say so.
// Chat is the one wiring. The app keeps what is its own: the send, which
// carries the app's tools, and its labels and fields. Everything else is
// filled in by Chat.Panel and answered by the host's ServeAppChat.

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// Agent is an agent as an app holds it for one turn: what LookupAppAgent
// returns, adjusted by the app, and handed back to the send. The fields are
// the ones an app sets for its own turn (a prompt section, a dispatch policy,
// a narrower tool list, the reasoning to use); the host writes them onto its
// record when the turn starts, and the change goes no further than that turn.
type Agent struct {
	ID                     string
	Name                   string
	OrchestratorPrompt     string
	AllowedTools           []string
	DispatchMode           string
	AllowedDispatchTargets []string
	ForcePrivate           bool
	EnableNotes            bool
	SeedNotes              string
	LeadModel              bool
	ThinkBudget            int
	// Record is the host's own record of the agent, which the fields above
	// are written back onto. An app leaves it alone.
	Record any
}

// AgentReferenceKind is the ReferenceSource kind an AGENT is attached under, so
// a consumer can tell an attached agent apart from an attached system or file
// store without hardcoding the string.
const AgentReferenceKind = "agent"

// AppPipelineRunKind is the restore kind an app's pipeline section runs under:
// the app that hosts the section registers the restorer, because only it can
// find the app again from a key.
const AppPipelineRunKind = "app_pipeline"

// ChatRunsPath is the run registry's path under an app. The host's run
// dispatcher reads the run id from the "/api/runs/" segment, so it cannot move.
const ChatRunsPath = "api/runs/"

// Block is a card an app's tool puts in the conversation through a
// Publisher: Type names the client-side renderer the app registered, ID is
// the card's identity (publishing the same ID again updates it), and Title,
// Text, HTML, URL and Data are that renderer's payload. The host keeps it on
// the session so a reload replays it.
type Block struct {
	Type  string
	ID    string
	Title string
	Text  string
	HTML  string
	URL   string
	Data  map[string]string
}

// Chat is where an app routes its agent chat.
type Chat struct {
	// Prefix is the app-relative path its chat endpoints live under, such as
	// "chat/". Runs are the exception: they live at "api/runs/", because the
	// run stream finds its id by that segment of the path.
	Prefix string
	// Query is appended to every session URL, carrying the app's own scope
	// placeholders ("guide={scope}", "project_id={project_id}") so the
	// session list follows what the app has open.
	Query string
	// Settings puts "Agent settings" in the chat's toolbar and serves the
	// app agent's settings page under Prefix ("chat/settings"): its rounds,
	// plan steps, gap detection and reasoning, and Reset to default
	// (app_agent_settings.go). Only for an app agent; Back is where the
	// page's back arrow goes, the app's page.
	Settings bool
	Back     string
	// Agents are app agents this app runs that another app owns, listed on
	// the settings page after its own: Scribe's Publish button runs the
	// Publishing app's Publisher. The app's own agents (every app agent
	// registered with the chat agent's OwningApp) are listed without this.
	Agents []string
}

// url is an endpoint under the prefix, with the app's query.
func (c Chat) url(path string) string {
	u := c.Prefix + path
	if c.Query != "" {
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		u += sep + c.Query
	}
	return u
}

// Panel fills p's chat endpoints, leaving any the app set itself.
func (c Chat) Panel(p ui.AgentLoopPanel) ui.AgentLoopPanel {
	set := func(field *string, v string) {
		if *field == "" {
			*field = v
		}
	}
	set(&p.CancelURL, c.Prefix+"cancel")
	set(&p.ConfirmURL, c.Prefix+"confirm")
	set(&p.InjectURL, c.Prefix+"inject")
	// A note the agent never read goes on as the next message (handleSend
	// takes it from_notes), as in the main chat.
	p.ResendUnreadNotes = true
	set(&p.ListURL, c.url("sessions"))
	set(&p.LoadURL, c.url("sessions/{id}"))
	set(&p.DeleteURL, c.url("sessions/{id}"))
	// Edit, Retry and Delete on a message: the thread is cut back to it.
	set(&p.TruncateURL, c.url("sessions/{id}"))
	set(&p.RenameURL, c.url("sessions/{id}/rename"))
	// Why a turn did what it did: a denied tool, a guard that stopped it.
	set(&p.DiagnosticsURL, c.url("sessions/{session}/diagnostics"))
	// An answered question card settles instead of replaying on every load.
	set(&p.BlockResolveURL, c.url("sessions/{id}/blocks/{block_id}/resolve"))
	// A reload or a closed tab rejoins the turn still running.
	set(&p.RunsURLBase, ChatRunsPath)
	if c.Settings {
		p.Actions = append(p.Actions, ui.ToolbarAction{Label: "Agent settings", Method: "redirect", URL: c.Prefix + "settings",
			Title: "This chat's agent: its rounds, plan steps, gap detection and reasoning. Reset to default is there too."})
	}
	return p
}

// Letting an app's tools put a CARD in the conversation.
//
// An app supplies tools to a run through PublicHandleSendWithAppTools, and
// those tools return text. Text is enough for a tool that reports a fact and
// wrong for a tool whose result the user needs to LOOK at and act on: a diff
// to review and revert, a generated table, a preview. Those already have a
// shape: a {kind:"block"} SSE event with an app-registered renderer, persisted
// on the session so it survives a reload. Emitting one needs the turn's SSE
// writer and session, and neither exists when the app builds its tools.
//
// The publisher closes that gap without handing an app the turn. The app makes
// one, closes its tool handlers over Publish, and passes it alongside its
// tools; the host binds it to the turn just before the run starts. Publish
// before binding, or on a run with no live stream at all like a headless
// dispatch, is a no-op, which is the behavior an app wants: the card is an
// enhancement to a tool result, never the tool result itself, so a tool must
// still say in words what it did.
//
// This is deliberately the whole surface. An app gets to publish a block; it
// does not get the SSE writer, the session, or anything else about the turn.

// Publisher carries an app's block emissions into a live turn.
//
// The zero value is usable and inert: an app may always build its tools over
// one, and whether anything is displayed is then a property of how the run
// was dispatched rather than a branch every tool has to write.
type Publisher struct {
	mu      sync.Mutex
	publish func(Block)
}

// Publish emits blk into the bound turn: live to the browser, and onto the
// session so a reload replays it. Blocks are upserted by ID, so a tool that
// re-publishes the same ID updates its card rather than stacking a new one.
//
// Safe to call from any goroutine, including a tool handler's, and safe to
// call on an unbound or nil publisher.
func (p *Publisher) Publish(blk Block) {
	if p == nil || blk.Type == "" {
		return
	}
	p.mu.Lock()
	publish := p.publish
	p.mu.Unlock()
	if publish != nil {
		publish(blk)
	}
}

// Bind is the host's half: it points the publisher at the turn now running,
// by the function that sends a block into it.
func (p *Publisher) Bind(publish func(Block)) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.publish = publish
	p.mu.Unlock()
}

// ServeWaitingNotes answers GET ?id=<session> on an inject endpoint: the notes
// still waiting in that session's running turn, {notes: [{id, text}]}, for a
// page that joins the turn after they were written. Only the caller's own run;
// anything else (no run, finished, somebody else's) is an empty list, the
// same answer as nothing waiting. Shared by every app on the inject contract.
func ServeWaitingNotes(w http.ResponseWriter, r *http.Request, user string) {
	type note struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	out := []note{}
	if q := core.LookupInjectionQueue(strings.TrimSpace(r.URL.Query().Get("id"))); q != nil && q.Owner != "" && q.Owner == user {
		for _, n := range q.Pending() {
			if n.Framework {
				continue // the framework's, not a note the person wrote
			}
			out = append(out, note{ID: n.ID, Text: n.Text})
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"notes": out})
}
