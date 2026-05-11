// Phantom dashboard — fully declarative via core/ui. Renders both
// desktop (/phantom) and the legacy mobile alias (/phantom/mobile)
// from the same page definition. The page is laid out responsively:
// MaxWidth caps the form column at a comfortable reading width on
// big screens; on phones the framework's mobile drawer + tap-target
// rules kick in automatically.
//
// Reuses /api/config + /api/conversation* server endpoints; no
// hand-written HTML/CSS/JS in this file.

package phantom

import (
	"encoding/json"
	"net/http"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// compactExpand wraps ui.Expand to set Compact=true so the resulting
// row button renders as an icon-sized control. Lets the row layout
// keep the display name + auto-reply switch front-and-center on
// narrow viewports.
func compactExpand(label string, c ui.Component) ui.RowAction {
	a := ui.Expand(label, c)
	a.Compact = true
	return a
}

// handleDashboard renders the phantom dashboard for desktop and
// mobile. Auth is enforced via the same session middleware as the
// rest of phantom; the rest of the page is described declaratively
// below.
func (T *Phantom) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	page := ui.Page{
		Title:     "Phantom",
		ShowTitle: true,
		BackURL:   "/",
		// 900px keeps the form/table comfortable on desktop without
		// stretching label-text across the whole monitor; the
		// framework's mobile media-query collapses it to single-column
		// regardless of this cap.
		MaxWidth:  "900px",
		Sticky: ui.PanicBar{
			Label:   "⚠ PANIC — disable everything",
			OnClick: "/phantom/api/mobile/panic",
			Confirm: "Disable everything? Phantom master OFF, all conv auto-reply OFF, secure-API OFF, proactive OFF. Reversible.",
		},
		Sections: []ui.Section{
			{
				Title: "Master switches",
				Body: ui.ToggleGroup{
					Source: "/phantom/api/config",
					Toggles: []ui.Toggle{
						{Field: "enabled", Label: "Phantom enabled"},
						{Field: "auto_reply_all", Label: "Auto-reply to all conversations"},
						{Field: "secure_api_enabled", Label: "Allow secure-API tools"},
						{Field: "proactive_enabled", Label: "Allow proactive messaging"},
					},
				},
			},
			{
				Title:    "Persona",
				Subtitle: "How the AI introduces itself, who it acts as, and how it talks. Saved automatically as you type.",
				Body: ui.FormPanel{
					Source: "/phantom/api/config",
					Fields: []ui.FormField{
						{Field: "persona_name", Label: "Persona name", Type: "text",
							Placeholder: "AI Assistant", Help: "Name the AI introduces itself as."},
						{Field: "owner_name", Label: "Owner name", Type: "text",
							Placeholder: "your name", Help: "How the AI refers to you when talking about you."},
						{Field: "owner_handle", Label: "Owner phone", Type: "tel",
							Placeholder: "+15551234567", Help: "Your number — messages from this handle are treated as from you."},
						{Field: "personality", Label: "Personality", Type: "textarea", Rows: 3,
							Placeholder: "Describe who the AI is — character, tone, voice.",
							ChipsSource:     "/phantom/api/personas",
							ChipsValueField: "personality",
							ChipsCreateURL:  "/phantom/api/personas",
							ChipsDeleteURL:  "/phantom/api/personas/{id}",
							ChipsAssistURL:  "/phantom/api/persona-assist",
							ChipsAddLabel:   "+ New persona",
							// Persona chips carry both .personality and .name —
							// fan the name into the Persona name input so a
							// single click sets BOTH fields. Master rules that
							// rely on "respond when called by name" then see
							// the actual persona name in the gatekeeper context.
							ChipsAlsoSet: map[string]string{"persona_name": "name"}},
						{Field: "system_prompt", Label: "Conversation rules", Type: "rules",
							Placeholder: "No rules yet. Each rule is a single line.",
							Help: "One per line. Sent to the LLM as a numbered list."},
						{Field: "gatekeeper_prompt", Label: "Gatekeeper rules", Type: "rules",
							Placeholder: "No filter rules. Without any, the AI replies to every message.",
							Help: "Each rule is evaluated; a message must satisfy all of them to be answered."},
						{Field: "proactive_window", Label: "Proactive window", Type: "text",
							Placeholder: "09:00-21:00", Help: "Daily window when proactive messages may fire.",
							ShowWhen: "proactive_enabled"},
						{Field: "proactive_max_per_day", Label: "Proactive max / day", Type: "number",
							Min: 0, Max: 50, Help: "0 = unlimited.",
							ShowWhen: "proactive_enabled"},
						{Field: "proactive_prompt", Label: "Proactive prompt", Type: "textarea", Rows: 2,
							Placeholder: "What kinds of messages may be sent unprompted, when, and how often.",
							ShowWhen: "proactive_enabled"},
					},
				},
			},
			{
				Title:    "API keys",
				Subtitle: "Tokens for the macOS bridge to authenticate with /api/hook + /api/poll. Each key is shown in full once on creation.",
				Body: ui.KeyManager{
					ListURL:    "/phantom/api/keys",
					CreateURL:  "/phantom/api/keys",
					DeleteURL:  "/phantom/api/keys",
					SecretHint: "Paste this into the bridge's PHANTOM_API_KEY config.",
					EmptyText:  "No keys yet — create one for your bridge to use.",
				},
			},
			{
				Title: "Conversations",
				Body: ui.Table{
					Source:        "/phantom/api/conversations",
					RowKey:        "chat_id",
					EmptyText:     "No conversations yet.",
					AutoRefreshMS: int((30 * time.Second) / time.Millisecond),
					PullToRefresh: true,
					SortBy:        "updated",
					SortDesc:      true,
					Columns: []ui.Col{
						{Field: "display_name", Flex: 1},
						{Field: "updated", Format: "reltime", Mute: true},
					},
					RowActions: []ui.RowAction{
						// Auto-reply toggle pinned to the LEFT —
						// always thumb-reachable even when a long
						// display name would otherwise push controls
						// off the right edge of a narrow phone.
						{
							Type:    "toggle",
							Field:   "auto_reply",
							PostTo:  "/phantom/api/conversation/{chat_id}",
							Method:  "PATCH",
							Leading: true,
						},
						// Secondary actions use compact icon-only
						// buttons so the display name keeps more
						// horizontal room before truncating.
						compactExpand("💬", ui.HistoryPanel{
							Source:      "/phantom/api/conversation/{chat_id}",
							Header:      "Last 20 messages (LLM context window)",
							EmptyText:   "No messages yet.",
							MaxMessages: 20,
						}),
						compactExpand("🔧", ui.ChipPicker{
							OptionsSource: "/phantom/api/tools",
							// ?record=1 returns the Conversation record (with the
							// existing enabled_tools list) instead of the message
							// history. Without this we'd render every chip as
							// "off" and PATCHing one would wipe the list.
							RecordSource: "/phantom/api/conversation/{chat_id}?record=1",
							Field:        "enabled_tools",
							PostTo:       "/phantom/api/conversation/{chat_id}",
							Method:       "PATCH",
						}),
						// Per-conversation settings — overrides the master
						// persona for this specific chat. Empty fields mean
						// "inherit from master config." PATCH semantics so we
						// don't clobber other fields like enabled_tools.
						compactExpand("⚙", ui.FormPanel{
							Source: "/phantom/api/conversation/{chat_id}?record=1",
							Method: "PATCH",
							// Note: PATCH posts to /phantom/api/conversation/{chat_id}
							// (no query param). FormPanel uses Source for both GET
							// and POST, so we route writes through a dedicated
							// no-query URL via the runtime's PATCH-strips-query rule.
							Fields: []ui.FormField{
								{Field: "display_name", Label: "Display name", Type: "text",
									Placeholder: "What you call this chat in the list."},
								{Field: "persona_name", Label: "Persona name (override)", Type: "text",
									Placeholder: "Inherit from master", Help: "Override how the AI introduces itself in this chat."},
								{Field: "personality", Label: "Personality (override)", Type: "textarea", Rows: 3,
									Placeholder: "Inherit from master",
									ChipsSource:     "/phantom/api/personas",
									ChipsValueField: "personality",
									ChipsCreateURL:  "/phantom/api/personas",
									ChipsDeleteURL:  "/phantom/api/personas/{id}",
									ChipsAssistURL:  "/phantom/api/persona-assist",
									ChipsAddLabel:   "+ New persona",
									// Companion field — picking a persona chip
									// also fills the Persona name override input.
									ChipsAlsoSet: map[string]string{"persona_name": "name"}},
								{Field: "system_prompt", Label: "Conversation rules (added to master)", Type: "rules",
									Placeholder: "No extra rules for this chat — master rules still apply.",
									Help: "Each line is added to the master rules; both lists apply together."},
								{Field: "gatekeeper_prompt", Label: "Gatekeeper rules (added to master)", Type: "rules",
									Placeholder: "No extra filters for this chat — master gatekeeper still applies.",
									Help: "Per-chat filters add to the master ones. A message must satisfy ALL rules across both lists."},
							},
						}),
						// Members + aliases editor — for group chats, plus
						// conversation-level alias handles (phone/email that
						// route to this same chat). Saves on blur via PATCH.
						compactExpand("👥", ui.MemberEditor{
							Source:            "/phantom/api/conv-info/{chat_id}",
							PostTo:            "/phantom/api/conversation/{chat_id}",
							Method:            "PATCH",
							Field:             "members",
							AliasHandlesField: "alias_handles",
							EmptyText:         "No members yet. Add one below — handle is the phone number or email; aliases are alternate handles for the same person.",
						}),
						// Memory facts the LLM has stored about this contact
						// via the memory(action="save") tool. Read-only list
						// with delete — there's no add path from the UI; the
						// LLM creates entries inline during conversations.
						compactExpand("🧠", ui.Table{
							Source:    "/phantom/api/memory/{chat_id}",
							RowKey:    "id",
							EmptyText: "No memories saved yet. The AI adds these automatically when it learns something worth remembering.",
							Columns: []ui.Col{
								{Field: "note", Flex: 1},
								{Field: "created_at", Format: "reltime", Mute: true},
							},
							RowActions: []ui.RowAction{
								{
									Type:    "button",
									Label:   "×",
									PostTo:  "/phantom/api/memory/{chat_id}/{id}",
									Method:  "DELETE",
									Confirm: "Delete this memory? The AI will lose this fact.",
									Variant: "danger",
									Compact: true,
								},
							},
						}),
						// Wipe message history (settings retained). Compact red
						// icon at the right edge — confirm-protected since this
						// is destructive.
						{
							Type:    "button",
							Label:   "🗑",
							PostTo:  "/phantom/api/conversation-clear/{chat_id}",
							Method:  "POST",
							Confirm: "Clear all message history for this conversation? Settings (auto-reply, persona, tools) are kept.",
							Variant: "danger",
							Compact: true,
						},
					},
				},
			},
		},
	}
	page.ServeHTTP(w, r)
}

// handleMobilePanic is the kill-switch endpoint. Flips the master
// Enabled flag off, AutoReplyAll off, ProactiveEnabled off, and sets
// every conv's AutoReply off. Idempotent — safe to call repeatedly.
func (T *Phantom) handleMobilePanic(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	cfg := defaultConfig(T.DB)
	cfg.Enabled = false
	cfg.AutoReplyAll = false
	cfg.ProactiveEnabled = false
	cfg.SecureAPIEnabled = false
	T.DB.Set(configTable, configKey, cfg)
	convsPaused := 0
	for _, k := range T.DB.Keys(conversationTable) {
		var c Conversation
		if !T.DB.Get(conversationTable, k, &c) {
			continue
		}
		if c.AutoReply {
			c.AutoReply = false
			T.DB.Set(conversationTable, k, c)
			convsPaused++
		}
	}
	Log("[phantom] PANIC button engaged: master + auto-reply-all + proactive + secure-api OFF; %d conversations had auto-reply paused", convsPaused)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":       "panic engaged",
		"convs_paused": convsPaused,
		"message":      "Engaged. Reload to confirm.",
	})
}
