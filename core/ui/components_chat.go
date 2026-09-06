package ui

import (
	"encoding/json"
)

// ChatPanel renders a streaming-chat layout: sessions sidebar, message
// thread, and an input area. The runtime handles SSE event parsing
// (chunk / tool_call / tool_result / done / error / session / status),
// markdown rendering of completed messages, and per-message Copy
// actions. Designed as the generic conversation primitive.
//
// Session APIs (all required):
//   - SessionsListURL: GET → array of session summaries with the
//     fields named by SessionIDField, SessionTitleField,
//     SessionLastAtField. Default field names match chat's existing
//     ChatSession schema (capitalized).
//   - SessionLoadURL: GET with {id} → full session including messages.
//   - SessionDeleteURL: DELETE with {id}.
//
// Send endpoint:
//   - SendURL: POST → SSE response. The runtime POSTs JSON
//     {session_id, message, history, ...ExtraSendFields}. Each SSE
//     event is parsed and dispatched: chunk events stream into the
//     latest assistant bubble; tool_call/tool_result render inline
//     pills; session events update the active session id; done/error
//     finalize the round.
type ChatPanel struct {
	SessionsListURL  string `json:"sessions_list_url"`
	SessionLoadURL   string `json:"session_load_url"`
	SessionDeleteURL string `json:"session_delete_url"`
	SendURL          string `json:"send_url"`
	// CancelURL aborts the in-flight server-side stream when set.
	// POST → 204 expected; runtime fires alongside the client-side
	// AbortController so server-side resources release immediately.
	CancelURL string `json:"cancel_url,omitempty"`
	// InjectURL accepts a message the user sends WHILE a turn is
	// streaming — the same contract AgentLoopPanel.InjectURL uses, so an
	// app wires one endpoint for both surfaces. POST {id, text} → 204;
	// 404 when the turn already finished, which the runtime treats as
	// "put the text back and let them press send".
	//
	// Without it this panel silently DROPS a second message: doSend
	// returns early while a stream is in flight, so what the user typed
	// is simply lost. The agent-loop panel has had this for a while;
	// this brings the plain chat panel to the same behaviour.
	InjectURL string `json:"inject_url,omitempty"`
	// Field names for the session summary records — defaults match
	// chat's ChatSession schema. Override only when migrating an app
	// with a different shape.
	SessionIDField     string `json:"session_id_field,omitempty"`      // default "ID"
	SessionTitleField  string `json:"session_title_field,omitempty"`   // default "Title"
	SessionLastAtField string `json:"session_last_at_field,omitempty"` // default "LastAt"
	// Field on the loaded session that holds the messages array.
	// Default "Messages".
	SessionMessagesField string `json:"session_messages_field,omitempty"`
	// Empty-state copy.
	EmptyText string `json:"empty_text,omitempty"`
	// Modes are toggle pills shown above the input. Each binds to a
	// pair of GET/POST URLs that round-trip a single boolean. The
	// runtime mixes the toggle's current value into the send body so
	// the server sees the active modes.
	Modes []ChatMode `json:"modes,omitempty"`
	// BulkSelect adds checkboxes to each session in the sidebar and a
	// bulk-actions bar at the top of the list. Currently supports
	// bulk delete via SessionDeleteURL repeated per selection.
	BulkSelect bool `json:"bulk_select,omitempty"`
	// Attachments enables a 📎 button + paste/drag attach for plain-
	// text files (logs, configs). The text is appended to the next
	// outgoing message in a fenced code block.
	Attachments bool `json:"attachments,omitempty"`
	// Markdown enables rendering of completed assistant messages
	// through a small built-in markdown renderer (headings, code
	// fences, inline code, bold, italic, links, lists). Streaming
	// chunks render plain until the round's done event arrives.
	Markdown bool `json:"markdown,omitempty"`
	// ExtraFields are arbitrary form fields rendered in the modes
	// bar (alongside Private/Explorer toggles). Their current values
	// ride along on every send body so the server sees them as
	// session parameters. Used by debate for the rounds picker.
	ExtraFields []ChatField `json:"extra_fields,omitempty"`
	// PrefillURL is an optional GET endpoint that returns plain text
	// to drop into the message input. When set, a small button is
	// rendered next to the send button that triggers the fetch.
	// Debate uses this for the "Suggest a topic" button.
	PrefillURL   string `json:"prefill_url,omitempty"`
	PrefillLabel string `json:"prefill_label,omitempty"` // default "Suggest"
	// ToolsURL — when set, the modes bar shows an expandable
	// "N tools" badge that fetches the URL on first open. Server
	// returns a JSON array of {name, desc}. Useful for showing the
	// LLM's available tool catalog at a glance.
	ToolsURL string `json:"tools_url,omitempty"`
	// Single renders the panel as ONE ongoing conversation: no
	// sessions sidebar, no New / picker / delete. The init path opens
	// the first (and only) session SessionsListURL returns. Generic —
	// any "one room" surface (an Operator console, an always-on
	// assistant) uses it instead of faking a one-item session list.
	Single bool `json:"single,omitempty"`
}

// ChatField defines one extra form field rendered in the chat
// toolbar. Only the bare minimum types — number, select, text —
// are supported; anything richer should ride on a Mode toggle.
type ChatField struct {
	Name    string   `json:"name"`              // POST body key + DOM id
	Label   string   `json:"label"`             // visible label
	Type    string   `json:"type"`              // "number" | "select" | "text"
	Options []string `json:"options,omitempty"` // for type=select — value == label
	// OptionPairs is the {value,label} alternative to Options. When set,
	// each <option> uses Value as the form value and Label as the
	// visible text. Useful when the id you want sent to the server
	// isn't readable (UUIDs, opaque keys). If both are set,
	// OptionPairs takes precedence.
	OptionPairs []SelectOption `json:"option_pairs,omitempty"`
	Default     string         `json:"default,omitempty"`
	Min         int            `json:"min,omitempty"` // for type=number
	Max         int            `json:"max,omitempty"` // for type=number
}

// ChatMode describes a toggle pill above the chat input. Bound to a
// single boolean field on the GET endpoint. The runtime mixes
// {field: true|false} into the SendURL body so the server sees the
// active modes (e.g. private_mode).
type ChatMode struct {
	Label   string `json:"label"`
	Title   string `json:"title,omitempty"` // tooltip
	GetURL  string `json:"get_url"`
	PostURL string `json:"post_url"`
	Field   string `json:"field"` // bool field name on GET / POST body
	// SendField is the body field name when sending the chat message
	// — defaults to Field. Use when the server's send-handler key
	// differs from the settings-endpoint key.
	SendField string `json:"send_field,omitempty"`
}

func (ChatPanel) componentType() string { return "chat_panel" }

func (c ChatPanel) MarshalJSON() ([]byte, error) {
	type alias ChatPanel
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"chat_panel", alias(c)})
}
