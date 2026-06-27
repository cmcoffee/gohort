package bridges

import (
	"net/http"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// handleDashboard renders the Bridges control surface: the panic kill-switch,
// the master enable toggle, the bridge registry (connectors + status), and key
// management. Channel/agent behavior lives in Agency — this is transport only.
func (T *Bridges) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	page := ui.Page{
		Title:     "Bridges",
		ShowTitle: true,
		BackURL:   "/",
		MaxWidth:  "900px",
		// Transport kill-switch — flips the master switch off; inbound is still
		// recorded but nothing routes to an agent or delivers.
		Sticky: ui.PanicBar{
			Label:   "⚠ PANIC — disable all bridges",
			OnClick: "/bridges/api/panic",
			Confirm: "Disable all bridges? Inbound stops routing to agents and nothing is delivered until you re-enable. Reversible.",
		},
		Sections: []ui.Section{
			{
				Title: "Master switch",
				Body: ui.Stack{Children: []ui.Component{
					ui.ToggleGroup{
						Source: "/bridges/api/config",
						Toggles: []ui.Toggle{
							{Field: "enabled", Label: "Bridges enabled (route inbound + deliver replies)"},
						},
					},
					// Your own messages arrive over the bridge with no handle; this
					// labels them so they read as you in group threads and to the agent.
					ui.FormPanel{
						Source: "/bridges/api/config",
						Method: "PATCH",
						Fields: []ui.FormField{
							{Field: "self_name", Label: "Your name", Type: "text", Placeholder: "Craig",
								Help: "How your own messages are labeled in group chats."},
							{Field: "self_handle", Label: "Your handle", Type: "text", Placeholder: "+15551234567",
								Help: "Your own phone/email — lets the agent text you directly (notify_me) and resolve \"me\" as a recipient."},
						},
					},
				}},
			},
			{
				Title:    "Bridges",
				Subtitle: "Each connector for a messaging service. iMessage runs as the gohort-desktop daemon; others (Telegram, Slack) are server-side. Toggle one off to pause just that bridge; status shows the last check-in.",
				Body: ui.Table{
					Source: "/bridges/api/bridges",
					RowKey: "id",
					Columns: []ui.Col{
						{Field: "bridge", Label: "Bridge", Flex: 2},
						{Field: "status", Label: "Status"},
						{Field: "reach", Label: "", Mute: true},
					},
					RowActions: []ui.RowAction{
						// Per-bridge enable switch (independent of the global panic).
						{Type: "toggle", Field: "enabled", Method: "PATCH",
							PostTo: "/bridges/api/keys/{id}", Leading: true},
						{Type: "button", Label: "Revoke", Method: "DELETE",
							PostTo:  "/bridges/api/keys/{id}",
							Confirm: "Revoke this bridge key? Its connector will stop authenticating.", Compact: true},
					},
					EmptyText: "No bridges yet. The gohort-desktop daemon registers itself automatically the first time it connects to /bridges/api/hook.",
				},
			},
			{
				Title:    "Conversations",
				Subtitle: "The chats you're managing. Use Add conversation to pick a contact who's messaged you (or enter a number), then Connect it to an agent to have it answered.",
				Body: ui.Stack{Children: []ui.Component{
					// Add lives in the conversations menu, not a section below. The
					// modal offers two paths: pick an incoming contact (with their
					// last message) or type a specific number / handle.
					ui.ModalButton{
						Label:    "+ Add conversation",
						Title:    "Add a conversation",
						Subtitle: "Pick a contact who's messaged you, or enter a number to pre-bind one before they do.",
						Variant:  "primary",
						Align:    "left",
						Width:    "560px",
						Body: ui.Stack{Children: []ui.Component{
							ui.ActionList{
								Source:     "/bridges/api/incoming-convos",
								LabelField: "label",
								DescField:  "desc",
								ButtonText: "Add",
								Method:     "POST",
								PostTo:     "/bridges/api/add-convo?chat_id={id}",
								EmptyText:  "No new incoming chats yet — they appear here as people message you.",
								// Refresh the conversations table behind the modal, and
								// drop the just-added contact from this picker.
								Invalidate: []string{"/bridges/api/conversations"},
								ReloadSelf: true,
							},
							ui.FormPanel{
								Source:      "/bridges/api/add-convo",
								Method:      "POST",
								SubmitLabel: "Add number",
								// Surface the new conversation in the table behind the modal.
								Invalidate: []string{"/bridges/api/conversations"},
								Fields: []ui.FormField{
									{Field: "handle", Label: "Number / handle", Type: "text",
										Placeholder: "+15551234567"},
									{Field: "service", Label: "Service", Type: "text", Placeholder: "imessage",
										Help: "imessage, telegram, slack, …"},
								},
							},
						}},
					},
					ui.Table{
						Source: "/bridges/api/conversations",
						RowKey: "chat_id",
						Columns: []ui.Col{
							{Field: "name", Label: "Chat", Flex: 2},
							{Field: "service_label", Label: "Service"},
							{Field: "connected", Label: "Channel", Mute: true},
						},
						RowActions: []ui.RowAction{
							// Per-channel auto-reply switch (phantom's per-conversation
							// toggle) — only shows once connected; off = inbound still
							// recorded but the agent doesn't answer this chat.
							{Type: "toggle", Field: "auto_reply", Method: "PATCH",
								PostTo: "/bridges/api/set-autoreply?id={channel_id}",
								OnlyIf: "connected", Leading: true},
							// Thread view — the recent messages, who-said-what.
							ui.Expand("💬 Thread", ui.HistoryPanel{
								Source:    "/bridges/api/messages/{chat_id}",
								Header:    "Recent messages",
								EmptyText: "No messages yet.",
							}),
							// Participants + aliases — name people, add a person's
							// alternate handles, and the chat's alternate ids.
							ui.Expand("👥 Participants", ui.MemberEditor{
								Source:            "/bridges/api/conv-info/{chat_id}",
								PostTo:            "/bridges/api/conversation/{chat_id}",
								Method:            "PATCH",
								Field:             "members",
								AliasHandlesField: "alias_handles",
								EmptyText:         "No members yet. Add one — handle is the phone/email; aliases are the same person's other handles.",
							}),
							// Relabel — give the chat a friendly name (overrides the
							// raw handle shown in the list).
							ui.Expand("✎ Rename", ui.FormPanel{
								Source:      "/bridges/api/conv-info/{chat_id}",
								PostURL:     "/bridges/api/conversation/{chat_id}",
								Method:      "PATCH",
								SubmitLabel: "Rename",
								Fields: []ui.FormField{
									{Field: "display_name", Label: "Name", Type: "text",
										Placeholder: "Family chat"},
								},
							}),
							// Connect — pick a free channel (the interface to an agent).
							// Only offered while UNconnected; once bound, the auto-reply
							// toggle and Clear take over.
							ui.ExpandIf("🔗 Connect", "", "connected", ui.ActionList{
								Source:     "/bridges/api/agent-channels",
								LabelField: "label",
								DescField:  "desc",
								ButtonText: "Connect",
								Method:     "POST",
								PostTo:     "/bridges/api/connect-channel?chat_id={chat_id}&channel_id={id}",
								Confirm:    "Route this chat to this channel's agent?",
								EmptyText:  "No free channels — create one in Agency (or free one up by clearing another chat), then connect it here.",
							}),
							// Clear — the only thing that unbinds the channel and frees
							// it back to the pool. Non-destructive to the chat: the
							// thread and members stay, and the agent's session is kept
							// (it resumes if this chat is reconnected). To just stop it
							// answering for now, switch the auto-reply toggle off.
							{Type: "button", Label: "Clear", Method: "POST",
								PostTo:  "/bridges/api/connect-channel?chat_id={chat_id}&channel_id=",
								OnlyIf:  "connected",
								Confirm: "Clear this chat's channel binding? The channel returns to the available pool. The chat's messages are kept, and reconnecting the same chat resumes its session. To just pause replies, use the auto-reply toggle instead.", Compact: true},
							// Remove — delete the conversation (and its thread). Use
							// after folding it into another chat as an alias handle.
							{Type: "button", Label: "Remove", Method: "DELETE",
								PostTo:  "/bridges/api/conversation/{chat_id}",
								Confirm: "Remove this conversation and its messages? Use this only after folding it into another chat via an alias.", Compact: true},
						},
						EmptyText: "No conversations yet. Use + Add conversation above to pick a contact or enter a number.",
					},
				}},
			},
			{
				Title:    "API keys",
				Subtitle: "Bridge keys authenticate connectors and the MCP server. Minting one shows the secret ONCE — copy it then. Put a key in a connector's config or the MCP client's X-API-Key header. (The iMessage desktop daemon auto-registers its own key, so you don't mint one for it here.)",
				Body: ui.KeyManager{
					ListURL:   "/bridges/api/keys",
					CreateURL: "/bridges/api/keys",
					DeleteURL: "/bridges/api/keys",
					NewLabel:  "+ New key",
					EmptyText: "No keys yet. Mint one for an MCP client or a server-side connector.",
				},
			},
			{
				Title:    "MCP Server",
				Subtitle: "Lets an external MCP client (e.g. Claude Desktop) reach your agents over a JSON-RPC endpoint — a tool-API, not a messaging bridge: the client calls tools/call to dispatch to an agent. It authenticates with one of the bridge keys above in the X-API-Key header.",
				Body: ui.DisplayPanel{
					Source: "/mcp/status",
					Pairs: []ui.DisplayPair{
						{Label: "Endpoint", Field: "endpoint", Mono: true},
						{Label: "Transport", Field: "transport"},
						{Label: "Auth", Field: "auth"},
						{Label: "Exposed tools", Field: "tools"},
						{Label: "Default agent", Field: "agent", Mono: true},
					},
				},
			},
			// "Add a bridge" (manual key mint) is intentionally omitted for now: the
			// only live connector, the iMessage desktop daemon, auto-registers its
			// own key on first connect, and no other connector exists yet — so a
			// manual mint produces a key for something redundant or non-existent.
			// Restore it when a manual-key connector (Telegram, Slack) ships: a
			// FormPanel POSTing to /bridges/api/keys with {name, service} (ideally a
			// service dropdown from core.bridgeServices). See docs/bridges-telegram.md.
		},
	}
	page.Render(w)
}
