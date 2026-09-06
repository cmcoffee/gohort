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
// ScheduleCreator is one "+ New …" button in the Scheduler modal: a label and
// the name of a client action (window.uiRegisterClientAction) the app registers
// to run the create flow. Kept generic so core/ui offers a create affordance
// without knowing any schedule kind.
type ScheduleCreator struct {
	Label  string `json:"label"`
	Action string `json:"action"`
}

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
	// usual) and lists the returned entries ([{at, kind, detail}], newest
	// first) in a modal. The intent: framework decisions made on the user's
	// behalf in THIS conversation — suppressed replies, discarded inputs,
	// retries — which otherwise vanish into server logs. Empty = no affordance.
	DiagnosticsURL string `json:"diagnostics_url,omitempty"`
	// StatusURL — optional per-session status readout. When set, the toolbar
	// shows a small pill whose text comes from this URL, refreshed when the
	// active session changes and after each turn completes. {session} in the
	// URL is substituted with the active session id.
	//
	//	GET → {label, title?, tone?}
	//
	// label is the pill's text; empty / absent renders NOTHING, which is how a
	// session with nothing to report stays quiet. title is the hover text. tone
	// is "mute" (default) or "active" for a stronger treatment.
	//
	// Deliberately generic: the panel knows a session can have a one-word state
	// worth showing beside the composer, and nothing about what that state
	// means. An app that wants to surface a workflow phase, a connection state,
	// or a review stage serves this shape and names it in its own words.
	StatusURL string `json:"status_url,omitempty"`
	// Schedules rail section — optional. When SchedulesURL is set, the rail shows
	// a single "Scheduler" entry carrying the TOTAL count; clicking it opens a
	// modal that lists every entry grouped by category. This keeps a schedule
	// visible within the agent it fires (shown for ANY agent, unlike the
	// operator-only OrchestratorNav). Hidden when the agent has none.
	//   SchedulesURL GET → []{name, detail, paused, pause_url, resume_url,
	//                         delete_url, edit_action?, id?, category, category_label}
	// Each row carries its own action URLs (the record types have different
	// endpoints), so the rail JS stays generic. `category` groups rows in the
	// modal and `category_label` is the section header — core/ui never names a
	// category itself; it renders whatever the app provides, in first-seen order.
	SchedulesURL string `json:"schedules_url,omitempty"`
	// ScheduleCreators optionally adds "+ New …" buttons to the top of the
	// Scheduler modal (for schedule kinds the app lets the user CREATE from the
	// rail, not only via chat). Each renders a button that invokes the named
	// client action (registered with window.uiRegisterClientAction) with a
	// {reload, sessionId} context — the app owns the create form and endpoint.
	// Generic: core/ui renders the button and dispatches the action by name; it
	// never knows what a "recurring task" is. Omit for none.
	ScheduleCreators []ScheduleCreator `json:"schedule_creators,omitempty"`
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
	Confirm   string `json:"confirm,omitempty"` // confirmation prompt before an ActionURL POST
	Variant   string `json:"variant,omitempty"` // "danger" | "warning" | "" — styles an action item
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
	// Only meaningful with Pinned; the "Manage ▾" dropdown stays alt-nav only.
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
	Layout string `json:"layout,omitempty"`
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
