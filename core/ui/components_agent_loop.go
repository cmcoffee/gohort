package ui

import (
	"encoding/json"
)

// AgentLoopPanel models a multi-turn agent workflow with
// operator-in-the-loop confirmation. It's distinct from ChatPanel
// (single conversation thread) and PipelinePanel (one-shot
// structured run): the agent loops with the user, may call tools,
// may pause to ask permission, and may stream rich activity
// alongside the conversation.
//
// Visual shape (left rail + right pane both optional):
//
//	┌──────────┬──────────────────────┬────────────────┐
//	│ Context  │ Conversation         │ Activity       │
//	│ list     │ (user msgs +         │ (tool calls,   │
//	│ (opt.)   │  assistant replies)  │  outputs)      │
//	│          │                      ├────────────────┤
//	│          │                      │ Terminal       │
//	│          │ ┌──────────────────┐ │ (opt., xterm)  │
//	│          │ │ input + send     │ │                │
//	│          │ └──────────────────┘ │                │
//	└──────────┴──────────────────────┴────────────────┘
//
// The conversation/activity boundary is horizontally resizable.
// When Terminal is set, the right pane splits vertically with a
// resizable boundary between activity and terminal.
//
// Left rail — when ListURL/LoadURL/DeleteURL are all unset, the
// rail is hidden and the panel is single-pane on the left. When
// set, the rail lists context records (sessions, workspaces,
// conversations — whatever the app calls them). Apps that need
// the rail filtered by an outer selector (e.g. a parent-record
// picker) pass query params through the URL templates and refresh
// the panel when the outer selection changes.
//
// SSE protocol — every payload is `data: <json>` with a `kind`:
//
//	{kind: "session", id}                    — sets the active context id
//	{kind: "message", role, id, text}        — append a message bubble
//	{kind: "chunk", id, text}                — append text to message id
//	{kind: "chunk_replace", id, text}        — replace message body
//	{kind: "message_done", id}               — finalize (markdown pass)
//	{kind: "activity", type, id?, text}      — append activity row
//	{kind: "activity_update", id, text}      — replace activity row body
//	{kind: "confirm", id, prompt, detail?,
//	  actions: [{label, value, variant?}]}    — operator confirmation
//	{kind: "block", type, id, ...}           — app-registered renderer
//	  (the runtime calls window.UIBlockRenderers[type] with the data)
//	{kind: "block_done", id}                 — finalize an app block
//	{kind: "block_remove", id}               — drop a block
//	{kind: "status", text}                   — status pill (top of pane)
//	{kind: "done"}                           — round complete
//	{kind: "error", text}                    — fatal error
//
// Message roles supported out of the box: "user", "assistant",
// "system". Apps wanting other narration shapes (intent callouts,
// plan checklists, etc.) emit `kind: "block"` with an app-registered
// renderer, keeping the role set generic.
//
// Activity types out of the box: "status" (info line), "cmd"
// (monospace command call-out), "output" (collapsible result),
// "watch" (spinner + label), "error". Apps emitting custom
// activity rows route through `kind: "block"` similarly.
//
// Operator confirmation: when the server emits a confirm event,
// the runtime renders a card in the activity pane with the
// supplied prompt and a button per action. Clicking a button
// POSTs to ConfirmURL with `{id, value}` and the runtime stamps
// the card with the outcome.
//
// That stamp is DOM-deep, so a server that buffers a run's frames
// for reconnect should also emit `{kind:"confirm_resolved", id,
// value, label}` once the escalation is answered — otherwise a
// reload replays the question without the answer and the card
// comes back armed, asking again about a call already allowed.
// The runtime settles the matching card and ignores the frame if
// the card is already settled, so emitting it is always safe.

type AgentLoopPanel struct {
	// Left rail — drives a generic list of named records. There
	// are two flavors, picked by ListIsContext:
	//
	//   - SESSION mode (default, ListIsContext=false): rows are
	//     past conversations. Clicking one replays its messages
	//     into the conversation pane and binds future sends to
	//     that session id. Best for chat-style apps where each
	//     conversation has its own thread.
	//
	//   - CONTEXT mode (ListIsContext=true): rows are reference
	//     contexts (workspaces, projects, system profiles). The
	//     conversation pane is ephemeral; each send creates a
	//     new chat session on the server. Clicking a row marks
	//     it as the active context — the id rides on every send
	//     body under ListBodyField — but does not clear or
	//     replay the conversation.
	ListURL   string `json:"list_url,omitempty"`   // GET → []record
	LoadURL   string `json:"load_url,omitempty"`   // GET {id} → record (+messages in SESSION mode)
	DeleteURL string `json:"delete_url,omitempty"` // DELETE {id}
	// Channels rail section — optional. When ChannelsURL is set, a DISTINCT
	// region renders at the top of the rail (above the sessions list) with its
	// own header, a + Add control, and edit/remove per row: the agent's
	// messaging-channel bindings, separate from chat Sessions.
	//   ChannelsURL    GET  → []{id, name, service, address, agent_id, auto_reply, direction, gatekeeper}
	//   ChannelSaveURL POST → upsert (body carries id on edit); create/edit a binding
	//   ChannelDeleteURL DELETE {id} → remove the binding
	// Clicking a channel opens its thread (session id "chan:<address>"). The
	// chat-session list excludes chan: rows so a channel lives only here.
	ChannelsURL      string `json:"channels_url,omitempty"`
	ChannelSaveURL   string `json:"channel_save_url,omitempty"`
	ChannelDeleteURL string `json:"channel_delete_url,omitempty"`
	// DiagnosticsURL — optional per-session diagnostics trail. When set, the
	// toolbar shows a small ⚠ affordance; clicking it fetches this URL (the
	// literal "{session}" placeholder substituted with the ACTIVE session id
	// at click time; extra-field placeholders like {agent_id} substituted as
	// usual) and lists the returned entries ([{at, kind, level, id, detail}],
	// newest first) in a modal. The intent: framework decisions made on the
	// user's behalf in THIS conversation — suppressed replies, discarded
	// inputs, retries — which otherwise vanish into server logs. Empty = no
	// affordance.
	//
	// Entries carrying level "blocked" are ALSO placed in the conversation
	// flow, among the messages they happened between, because the trail is
	// where you look once you already suspect a guard fired. The panel reads
	// the same URL on session load for that, and shows one live {kind:
	// "notice"} stream frame and its trail entry once, matching on id — so a
	// server that streams notices must give a frame the same id the trail
	// serves for it.
	DiagnosticsURL string `json:"diagnostics_url,omitempty"`
	// StatusURL — optional per-session status readout. When set, the toolbar
	// shows a small pill whose text comes from this URL, refreshed when the
	// active session changes and after each turn completes. {session} in the
	// URL is substituted with the active session id.
	//
	//	GET → {label, title?, tone?, detail_url?, actions?}
	//
	// label is the pill's text; empty / absent renders NOTHING, which is how a
	// session with nothing to report stays quiet. title is the hover text.
	//
	// detail_url, when present, makes the pill a BUTTON: clicking it GETs the
	// URL and renders whatever JSON comes back in a modal, generically (an
	// object as labelled fields, arrays of objects as sub-cards, long text
	// preformatted — the same renderer the row actions' ShowResult uses), so
	// a status that has more behind it than a word can show the rest without
	// the panel knowing what it is. actions is an optional list of buttons for
	// that modal — [{label, url, method?, confirm?, options_url?}] — each
	// firing "<method> <url>[&value=<picked>]" and then refreshing the pill;
	// an action with options_url GETs [{value,label}] first and offers them
	// in a select, for "move to one of these" controls. URLs are served
	// concrete (the app embeds agent and session itself). tone
	// is "mute" (default) or "active" for a stronger treatment.
	//
	// Deliberately generic: the panel knows a session can have a one-word state
	// worth showing beside the composer, and nothing about what that state
	// means. An app that wants to surface a workflow phase, a connection state,
	// or a review stage serves this shape and names it in its own words.
	StatusURL string `json:"status_url,omitempty"`
	// ChannelAgentsURL — optional GET → [{id, name}] of the agents a channel
	// may be bound to, so the channel editor can re-point a channel at a
	// different agent. Omit to hide the agent picker.
	ChannelAgentsURL string `json:"channel_agents_url,omitempty"`
	// DefaultGatekeeperRule — the app's canonical default wake rule, surfaced so
	// the channel editor can offer a "Reset to default" control on the gatekeeper
	// rules. The default itself lives in Go (single source of truth); this just
	// carries its text to the client. Omit to hide the reset control.
	DefaultGatekeeperRule string `json:"default_gatekeeper_rule,omitempty"`
	// RenameURL — optional. When set, each rail row gets a ✎ button.
	// Clicking prompts for a new name; the runtime POSTs {id, name}
	// to RenameURL, then refreshes the list. Useful for workspaces /
	// projects where the title is user-editable.
	RenameURL string `json:"rename_url,omitempty"`
	// TruncateURL — optional. When set, the LAST user message bubble
	// gets Edit + Delete affordances. PATCH {at: N} to TruncateURL
	// (with {id} substituted to the active session id) drops persisted
	// messages from index N onward. Edit replaces + truncates +
	// resends; Delete just truncates. The URL template substitutes
	// {id} with the active session id at click time.
	TruncateURL string `json:"truncate_url,omitempty"`
	// MessageScrub — optional. When true (and TruncateURL is set), each
	// replayed message bubble gets a ✕ affordance that deletes JUST that one
	// message (PATCH {delete_at: rawIndex}), keeping the rest of the thread —
	// the in-thread per-turn scrub. Gated so apps whose PATCH endpoint only
	// understands {at} truncation don't render a button that would misfire.
	MessageScrub bool   `json:"msg_scrub,omitempty"`
	NewLabel     string `json:"new_label,omitempty"`  // default "New"
	ListTitle    string `json:"list_title,omitempty"` // sidebar header (default "Sessions")
	// ListPosition picks where the sessions list lives. All three show the same
	// list — search, unread marks, rename, delete, New — and differ only in what
	// it costs to have around:
	//   "rail" (default) — a left rail, collapsed by default behind a ☰ tab.
	//   "top"            — a permanent 260px rail on desktop, not collapsible
	//                      (the toggles are hidden); the drawer still applies on
	//                      mobile. The chat-app shape, for a page whose whole
	//                      job is the conversation.
	//   "modal"          — no rail at all. The toolbar gets one button that
	//                      opens the list as a dialog, and picking a session
	//                      loads it and closes. For a panel that is already the
	//                      narrowest column on its page — a workbench chat,
	//                      where a rail cannot fit and a COLLAPSED rail is the
	//                      worst of both: it still costs a hamburger, an expand
	//                      tab, and a layout mode, all to reach a list you
	//                      wanted for two seconds.
	ListPosition string `json:"list_position,omitempty"`
	// Record field overrides (defaults match capitalized
	// chat-style schemas: ID / Title / LastAt / Messages).
	IDField       string `json:"id_field,omitempty"`
	TitleField    string `json:"title_field,omitempty"`
	DateField     string `json:"date_field,omitempty"`
	MessagesField string `json:"messages_field,omitempty"`
	// ListIsContext switches the rail to CONTEXT mode (see above).
	ListIsContext bool `json:"list_is_context,omitempty"`
	// ListBodyField names the JSON key under which the active rail
	// id ships on every send body when ListIsContext is set.
	// Default "context_id". Apps that already key their server on
	// a different name (e.g. "workspace_id") override here.
	ListBodyField string `json:"list_body_field,omitempty"`

	SendURL   string `json:"send_url"` // POST → SSE
	CancelURL string `json:"cancel_url,omitempty"`
	// InjectURL — when set, pressing Send while a session is
	// already in flight POSTs {id, text} here instead of starting
	// a new session. Mirrors legacy servitor's interjection flow:
	// the user can type a follow-up note while the agent is
	// running, and the agent picks it up between rounds.
	InjectURL string `json:"inject_url,omitempty"`
	// ConfirmURL receives POSTs from operator-confirmation card
	// clicks. Body shape: {id, value} where value is the chosen
	// action's value field. Required when the server emits
	// `kind: "confirm"` events.
	ConfirmURL string `json:"confirm_url,omitempty"`
	// BlockResolveURL settles an ACTIONABLE persisted block durably.
	// A `kind:"block"` card that asks the user for a decision has two
	// lifetimes: the DOM one, which ends at the click, and the stored
	// one, which does not — so without this the answered card replays
	// on every session load with its buttons live again. Renderers of
	// such cards call window.uiResolveBlock(id, note) once the answer
	// lands; the note is what the card shows in place of its controls
	// on replay, and the server echoes it back as the block's
	// `resolved` field. Template — {id} is the session id (as in every
	// other URL here), {block_id} is the block, and extra-input
	// placeholders substitute the same way the session URLs' do:
	//   "api/sessions/{id}/blocks/{block_id}/resolve?agent_id={agent_id}"
	// Empty = cards settle in the DOM only (the previous behavior).
	// Display-only blocks (artifacts, link cards) never call it.
	BlockResolveURL string `json:"block_resolve_url,omitempty"`
	// EventsURL — optional SSE reconnect endpoint. When set, the
	// runtime can reattach to an in-flight session after a page
	// reload (deep-link or refresh). Server returns the same
	// event stream the original send is producing.
	EventsURL string `json:"events_url,omitempty"`
	// RunsURLBase — optional base path for the run-registry endpoints
	// added in the agency-runs detach work. When set (e.g.
	// "api/runs/"), the runtime will:
	//   - On openSession, GET <base>active?session_id=<sid> to find
	//     any in-flight run for the loaded session.
	//   - If found, open EventSource on
	//     <base><run_id>/stream?since=<received_count> to replay
	//     missed events and tail live.
	//   - Cancel button POSTs to <base><run_id>/cancel.
	// Empty = behaves as before (no reconnect, /api/cancel by sid).
	// The server's RunRegistry / handleRunsStream / handleRunsCancel
	// shape this expects.
	RunsURLBase string `json:"runs_url_base,omitempty"`
	// DeepLinkParam — when set, the runtime mirrors the active
	// context id into the URL query string (e.g. ?session=abc).
	// Reloading the page picks up the parameter and reconnects.
	DeepLinkParam string `json:"deep_link_param,omitempty"`
	// AutoSend — a message the panel sends ONCE, automatically, after it
	// mounts and is ready (via its own send path, not a simulated click).
	// Used for deep-link handoffs that pre-load a first message (e.g. the
	// page reads a one-shot brief id and stamps the brief here) so the agent
	// responds immediately without the user re-typing. Empty = no auto-send.
	AutoSend string `json:"auto_send,omitempty"`
	// Terminal — when set, splits the right pane vertically with
	// activity on top and a terminal below. The terminal field
	// names the WebSocket endpoint the runtime opens to drive
	// xterm.js. Actual xterm wiring is gated by Phase 2b; for
	// now the renderer reserves the pane slot and ships a
	// placeholder.
	Terminal *AgentTerminal `json:"terminal,omitempty"`
	// Empty-state copy for the conversation pane.
	EmptyText string `json:"empty_text,omitempty"`
	// SubmitLabel — input button label, default "Send".
	SubmitLabel string `json:"submit_label,omitempty"`
	// Placeholder for the input textarea.
	Placeholder string `json:"placeholder,omitempty"`
	// Markdown enables a markdown pass on assistant messages once
	// their `message_done` event arrives. Streaming chunks stay
	// plain until done.
	Markdown bool `json:"markdown,omitempty"`
	// BulkSelect adds checkboxes + bulk delete to the sessions
	// sidebar (same shape as ChatPanel).
	BulkSelect bool `json:"bulk_select,omitempty"`
	// MarkAllReadURL, when set, adds a "Mark all read" button next to the
	// Select pill in the sidebar header. It POSTs to this URL (extras like
	// {agent_id} substituted) and reloads the list. The button shows only
	// when at least one session is unread. core/ui owns the affordance; the
	// app owns the endpoint that clears the unread state.
	MarkAllReadURL string `json:"mark_all_read_url,omitempty"`
	// Attachments enables a 📎 button on the input row. When set,
	// the runtime base64-encodes selected images and ships them in
	// the send body as `images: [...]`.
	Attachments bool `json:"attachments,omitempty"`
	// Actions — toolbar buttons rendered above the input row.
	// Same Method semantics as ToolbarAction elsewhere
	// (client / post / open / redirect). "client" handlers
	// receive {sessionId, button, action} via
	// window.uiRegisterClientAction — use for app-specific
	// behavior (open a settings modal, switch context, etc.).
	Actions []ToolbarAction `json:"actions,omitempty"`
	// ExtraFields render in a strip beside the input. Each
	// field's current value rides along on every send body so
	// the server sees them as round parameters.
	ExtraFields []ChatField `json:"extra_fields,omitempty"`
	// ExtraFieldsInSidebar moves the ExtraFields strip OUT of the
	// chat-pane topbar and into the sessions rail header (between
	// the rail's title row and the session list). Use for context
	// pickers that scope the session list itself — e.g. an app with
	// an "agent" picker whose rail sessions are scoped to the active
	// pick reads more naturally as a rail header than as a topbar
	// control. Only meaningful when ListPosition is
	// "top" + the rail is visible; the field still substitutes into
	// list/load/delete URL templates the same way.
	ExtraFieldsInSidebar bool `json:"extra_fields_in_sidebar,omitempty"`
	// HideActivity collapses the right-hand activity pane on
	// load. The user can drag the divider to reveal it. Useful
	// for apps whose default flow doesn't surface much activity
	// (only enable when needed).
	HideActivity bool `json:"hide_activity,omitempty"`

	// LockActivity hides the activity pane AND the floating
	// expand affordance — the pane is not user-toggleable. Use
	// for chat-only surfaces that intentionally route every
	// thread into the conversation pane. Implies HideActivity.
	LockActivity bool `json:"lock_activity,omitempty"`

	// Modes adds per-session toggle pills above the input row
	// (Private, Explorer, etc.). Each Mode is bound to a boolean
	// setting via GET/POST endpoints; the runtime mixes the
	// active flags into every send body so the server sees the
	// turn's mode state alongside the message. Same semantics as
	// ChatPanel.Modes — re-used here so AgentLoopPanel apps can
	// adopt the same per-turn-toggle pattern without porting to
	// ChatPanel.
	Modes []ChatMode `json:"modes,omitempty"`

	// OrchestratorNav, when set, replaces the session list with these nav
	// items for agents the host app opts in (see AltNavFlag). Every other
	// agent is untouched and keeps its session list. Each item is a sidebar
	// view: empty Source = the chat; a Source URL = a table view swapped into
	// the main pane. The picker + toolbar stay as-is.
	OrchestratorNav []OrchestratorNavItem `json:"orchestrator_nav,omitempty"`
	// AltNavFlag names a window-global object the host page sets: a map whose
	// keys are the agent ids that get the OrchestratorNav (instead of the
	// session list) and whose values are each agent's pinned session id (the
	// one ongoing thread to resume). Empty = the feature is off. core/ui
	// hardcodes no app-specific global or session-id scheme; the app owns the
	// name, the membership, and the per-agent session.
	AltNavFlag string `json:"alt_nav_flag,omitempty"`
	// AltPrimaryLabel names the pinned home-thread hero row (the 🧠 row at the
	// top of the rail) for alt-nav agents. App-owned wording; core/ui defaults
	// to "Channel" when unset rather than hardcoding any one app's term.
	AltPrimaryLabel string `json:"alt_primary_label,omitempty"`

	// Height overrides the panel's default size — any CSS length ("360px",
	// "50vh"). The default fills the viewport, which is right for a page whose
	// whole job is the conversation and wrong everywhere else: mounted inside a
	// row expander or a modal, a viewport-tall chat pushes the thing it belongs
	// to off the screen. Set it whenever the conversation is a PART of a page
	// rather than the page.
	Height string `json:"height,omitempty"`

	// NewVariants, when set, turns the rail's "+ New" button into a split
	// control: the primary button starts an ordinary new session, and a
	// caret (▾) opens a menu of alternate new-session modes. Each variant
	// arms its Extras onto the FIRST send of the new session (creation-time
	// flags the server reads when it mints the session, e.g. a clean-room
	// "incognito" session). Use this for choices that are decided when a
	// session is BORN rather than toggled mid-thread — unlike Modes, which
	// are live per-turn switches on the session you're already in.
	NewVariants []NewSessionVariant `json:"new_variants,omitempty"`
}

// NewSessionVariant is one alternate entry in the rail's "+ New ▾" menu
// (see AgentLoopPanel.NewVariants). Selecting it opens a fresh session and
// arms Extras onto that session's first send — a creation-time choice, not
// a live toggle.
type NewSessionVariant struct {
	Label string `json:"label"`
	Title string `json:"title,omitempty"` // tooltip
	// Extras are mixed into the body of the new session's first send. The
	// server reads them at session creation (the framework's pending-extras
	// channel rides one send, then clears — exactly creation-time scope).
	Extras map[string]any `json:"extras,omitempty"`
}

// OrchestratorNavItem is one sidebar nav entry for an orchestrator-mode agent
// (see AgentLoopPanel.OrchestratorNav).
type OrchestratorNavItem struct {
	Label  string `json:"label"`
	Source string `json:"source,omitempty"` // GET → table rows; empty = the chat view
	// RowActions render as per-row buttons in the table. Each fires
	// "<Method> <URL>?id=<row._id>" then reloads the view. Rows carry their
	// target in a hidden "_id" field (e.g. a Delete button, or an
	// Approve / Deny pair).
	RowActions []OrchestratorRowAction `json:"row_actions,omitempty"`
	// ActionURL makes this item a BUTTON that POSTs to the URL (with the
	// current agent id appended as ?agent=<id>) after an optional Confirm,
	// instead of opening a chat or table view — for channel-level operations
	// (clear the thread, decommission). Empty = not an action item.
	ActionURL string `json:"action_url,omitempty"`
	// ActionMethod "client" makes ActionURL the NAME of a registered client
	// action (window.uiRegisterClientAction) instead of an endpoint to POST —
	// for an item that opens the app's own form. The action receives {reload}.
	// Empty keeps the POST behaviour. Generic: core/ui dispatches by name and
	// never learns what the form is for.
	ActionMethod string `json:"action_method,omitempty"`
	Confirm      string `json:"confirm,omitempty"` // confirmation prompt before an ActionURL POST
	Variant      string `json:"variant,omitempty"` // "danger" | "warning" | "" — styles an action item
	// Pinned lifts this item OUT of the "Manage ▾" dropdown and renders it as a
	// prominent row ABOVE the session list — for an action queue (e.g.
	// Permissions) that's time-sensitive enough to deserve a fixed, glanceable
	// home rather than being buried in a menu. Always visible for fleet agents.
	Pinned bool `json:"pinned,omitempty"`
	// Topbar renders this item as a control in the topbar action row instead of
	// a rail row or a Manage-menu entry — right-aligned, with its count badge.
	// The rail belongs to the selected agent (its threads, its channels), so an
	// item whose data spans agents reads as that agent's when it sits there.
	// Pair with AllAgents for a queue that belongs to the USER. Overrides
	// Pinned when both are set.
	Topbar bool `json:"topbar,omitempty"`
	// AllAgents renders this item for EVERY agent, not only the ones the host
	// app opted into the alt nav. Use it when the item's DATA is user-scoped
	// rather than agent-scoped — an approvals queue that any agent can add to,
	// say. Gating such a queue on the alt-nav opt-in hides work the user still
	// has to act on, and can strand it entirely when no agent qualifies.
	//
	// Honored wherever an item can render: a pinned row, a topbar control, and
	// a menu entry alike. A menu whose items are ALL gated disappears with
	// them, so a dropdown never opens onto an empty panel; a group heading
	// whose rows are all hidden goes too, since it then labels nothing.
	// Menu placement used to ignore this flag — the dropdown was shown or
	// hidden wholesale on the alt-nav opt-in, so a per-item exemption inside
	// one could never be reached.
	AllAgents bool `json:"all_agents,omitempty"`
	// BadgeField names a hidden row field; the count badge then reflects only
	// rows where that field is truthy (e.g. "_pending" counts just the pending
	// approvals on a page that also lists granted ones). Empty = count all rows.
	BadgeField string `json:"badge_field,omitempty"`
	// AutoRefreshMS re-fetches this item's Source on an interval while its
	// view is open, for live data (an activity feed, a queue). 0 = static
	// (fetch once per open). Mirrors Table.AutoRefreshMS.
	AutoRefreshMS int `json:"auto_refresh_ms,omitempty"`
	// Layout picks how the source rows render: "" / "table" (default, dense
	// grid) or "cards" (one card per row — first field bold as the title, the
	// rest as detail lines, row actions as buttons). Cards suit an approval
	// queue (Permissions) where each row is a decision, not a data point.
	//
	// A "cards" row may carry a hidden "_section" field; when its value
	// changes from the previous row's, that heading is drawn before the card.
	// It is how one source renders as several titled lists — a summary view —
	// without the menu growing an entry per list.
	Layout string `json:"layout,omitempty"`
	// Menu names the topbar dropdown this item appears in. Menus are built in
	// the order their first item appears and labelled with this exact string,
	// so the label is the panel's own name rather than a wrapper around it.
	// Empty puts the item in the default menu ("Manage").
	//
	// Group was the first answer to the same problem and is the weaker one: a
	// heading inside one dropdown still requires opening a menu named
	// something else to reach it, and a menu in one agent's topbar makes
	// everything under it read as that agent's whatever the heading says. When
	// a group is a noun the user already thinks in, it should be the button.
	Menu string `json:"menu,omitempty"`
	// Group names the heading this item sits under, WITHIN its menu. Items
	// keep their declared order; each new group value draws its heading once.
	// Empty means no heading. Use it for a subdivision inside a menu that is
	// already correctly named — reach for Menu first.
	Group string `json:"group,omitempty"`
	// Scope decides what the item's Source is asked about. The default ("" or
	// "agent") appends the selected agent, which is what a per-agent view
	// needs. "fleet" appends nothing, so the handler answers for everything
	// the user owns.
	//
	// Without this every source is asked about one agent whether it means to
	// be or not, and a handler written to answer fleet-wide when given no
	// agent can never actually do so through this menu.
	Scope string `json:"scope,omitempty"`
	// Icon is an optional leading glyph (emoji or short text) shown before the
	// label on a Pinned rail row — so a pinned action queue reads as a distinct
	// tier alongside the Channel hero, not as a bare list entry.
	Icon string `json:"icon,omitempty"`
	// StateField + StateOptions add a segmented STATE control to each card in a
	// "cards" layout (e.g. a permission's Always allow / Needs approval /
	// Blocked). StateField names the hidden row field holding the current value;
	// the option whose Value matches it is highlighted. Clicking an option POSTs
	// its URL with ?id=<row._id>&agent=<id>&value=<Value>. Rows lacking
	// StateField render no control — so pending cards (buttons) and policy rows
	// (segmented control) can coexist in one view.
	StateField   string                    `json:"state_field,omitempty"`
	StateOptions []OrchestratorStateOption `json:"state_options,omitempty"`
}

// OrchestratorStateOption is one segment of a card's state control.
type OrchestratorStateOption struct {
	Label  string `json:"label"`
	Value  string `json:"value"`            // matches StateField's row value
	URL    string `json:"url"`              // POST <url>?id=<row._id>&agent=…&value=<Value>
	Method string `json:"method,omitempty"` // default POST
}

// OrchestratorRowAction is one per-row button in an orchestrator nav view.
type OrchestratorRowAction struct {
	Label   string `json:"label"`
	URL     string `json:"url"`               // POST/DELETE <url>?id=<row._id>
	Method  string `json:"method,omitempty"`  // default POST
	Variant string `json:"variant,omitempty"` // "success" | "danger" | ""
	Confirm string `json:"confirm,omitempty"`
	// OnlyIf / HideIf gate the button on a row field's truthiness (use a
	// hidden "_"-prefixed field so it doesn't render as a column). OnlyIf
	// shows the button only when row[field] is truthy; HideIf hides it when
	// truthy. e.g. a Pause action with HideIf:"_paused" + a Resume action
	// with OnlyIf:"_paused" so only the applicable one shows per row.
	OnlyIf string `json:"only_if,omitempty"`
	HideIf string `json:"hide_if,omitempty"`
	// PickerSource turns the action into a "pick a target, then act" control: on
	// click it GETs PickerSource (which returns a JSON list of {value,label},
	// with ?agent=<id> appended) and shows the choices in a modal; picking one
	// POSTs URL?id=<row._id>&agent=<id>&value=<picked>. Generic — a relink to a
	// live agent, a reassignment, any choose-from-a-list-then-act row action.
	// When empty the action is a plain fixed-URL button as before.
	PickerSource string `json:"picker_source,omitempty"`
	PickerTitle  string `json:"picker_title,omitempty"` // modal heading (default: Label)
	// ShowResult makes the action a READ: the runtime fetches URL?id=<row._id>
	// (Method defaults to GET) and renders the JSON reply in a modal titled
	// Label, instead of firing-and-reloading. Objects render as labelled
	// fields, long or multi-line strings as preformatted text, arrays of
	// objects as one sub-card each — so a row can open its full record (a
	// run's step trace, a request's payload) without the list carrying it.
	// Field order follows the reply, so the server decides what reads first.
	ShowResult bool `json:"show_result,omitempty"`
	// View makes this action NAVIGATE to another nav view instead of calling an
	// endpoint. Name the target "<Menu>/<Label>" ("Fleet/Runs") — by menu and
	// label rather than by Source, because the same Source legitimately appears
	// in two menus (one view asked about one thing and about everything) and
	// landing on the wrong one answers the wrong question.
	//
	// It exists so a summary figure can reach the list it counts. Without it a
	// count and its rows sit in different menus with nothing joining them, and
	// the number becomes a dead end: the reader is told two of something failed
	// and left to go and find them.
	View string `json:"view,omitempty"`
	// Query is appended to the target view's Source for that open only, so a
	// view can be entered already narrowed without changing what its own button
	// asks. "{agent}" resolves to the agent in view, which is how a per-agent
	// summary hands its scope to a view that is otherwise fleet-wide.
	Query string `json:"query,omitempty"`
	// Note is the line shown above a view entered through Query, saying in the
	// app's own words what was narrowed. The marker itself is not optional — a
	// filtered pane is otherwise indistinguishable from the whole one, so when
	// this is empty the query is rendered instead ("status: failed"). Set it to
	// say something a reader recognises.
	Note string `json:"note,omitempty"`
}

// AgentTerminal configures the optional bottom-right terminal pane
// of an AgentLoopPanel. The runtime opens a WebSocket to URL and
// pipes bytes to xterm.js (Phase 2b will wire xterm proper; for
// now the pane reserves space and shows a placeholder).
type AgentTerminal struct {
	URL   string `json:"url"`             // WebSocket endpoint
	Title string `json:"title,omitempty"` // pane header (default "Terminal")
}

func (AgentLoopPanel) componentType() string { return "agent_loop_panel" }

func (c AgentLoopPanel) MarshalJSON() ([]byte, error) {
	type alias AgentLoopPanel
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"agent_loop_panel", alias(c)})
}
