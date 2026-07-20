package bridges

import (
	"net/http"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// handleDashboard renders the Bridges control surface: the panic kill-switch,
// the master enable toggle, the bridge registry (connectors + status), and key
// management. Channel/agent behavior lives in Agents — this is transport only.
func (T *Bridges) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	page := ui.Page{
		Title:     "Bridges",
		ShowTitle: true,
		BackURL:   "/",
		MaxWidth:  "900px",
		Nav:       HubNav("/bridges"), // shared hub tabs, Bridges active

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
							{Field: "tag_override", Label: "Outbound name tag (global override)", Type: "text", Placeholder: "(agent's own name)",
								Help: "Optional. When set, every agent that signs its outbound messages uses THIS name instead of its own — a deployment-wide label so all agents present as one identity. Leave blank to let each agent sign with its own name. A per-channel override still wins over this. Whether an agent tags at all is set per-agent in the agent editor."},
						},
					},
				}},
			},
			{
				Title:    "Message bridges",
				Subtitle: "PUSH sources: a messaging connector delivers inbound into a channel. iMessage runs as the gohort-desktop daemon; others (Telegram, Slack) are server-side. Toggle one off to pause just that bridge; status shows the last check-in.",
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
					EmptyText: "No message bridges yet. The gohort-desktop daemon registers itself automatically the first time it connects to /bridges/api/hook.",
				},
			},
			{
				// Stage A of the unified-bridge model: a POLL source is the
				// same bridge concept as a message (push) bridge — one source
				// feeding an agent — so it belongs here, not only in the
				// orchestrate `bridge` tool + Admin. Data + actions come from
				// the shared console endpoints (owner-global, admin-gated),
				// so this view stays a thin projection with no duplicated
				// storage. (Poll → CHANNEL target and unified creation are
				// Stages B/C; today a poll bridge wakes its agent's thread.)
				Title:    "Polling bridges",
				Subtitle: "POLL sources: gohort calls an API on a schedule (through a saved credential) and, when the response changes, delivers into the target — a channel (its agent reacts in that conversation) or an agent's own thread. Agents create these with the bridge tool; pause or delete one here. Zero LLM cost until something changes.",
				Body: ui.Table{
					Source: "/orchestrate/api/console/bridges",
					RowKey: "name",
					Columns: []ui.Col{
						{Field: "name", Label: "Bridge", Flex: 1},
						{Field: "credential", Label: "Credential", Mute: true},
						{Field: "detail", Label: "Source → destination", Mute: true, Flex: 2},
						{Field: "state", Label: "Status", Type: "badge", Badges: []ui.BadgeMapping{
							{Value: "active", Label: "Active", Color: "success"},
							{Value: "paused", Label: "Paused", Color: "warning"},
						}},
						{Field: "last_fired", Label: "Last fired", Mute: true},
					},
					RowActions: []ui.RowAction{
						// Hook this bridge to a channel — the change is delivered into
						// that conversation and its bound agent reacts there. Offered
						// while UNhooked; once connected, Detach takes over. A bridge
						// can target any channel (delivery target, not an exclusive
						// binding), so the picker lists them all.
						ui.ExpandIf("🔗 Connect channel", "", "_connected", ui.ActionList{
							Source:     "/orchestrate/api/console/bridge-channels?owner={owner}",
							LabelField: "label",
							DescField:  "desc",
							ButtonText: "Connect",
							Method:     "POST",
							PostTo:     "/orchestrate/api/console/bridges/set-channel?owner={owner}&name={name}&channel_id={id}",
							Confirm:    "Deliver this bridge's changes into this channel? Its bound agent will react there.",
							EmptyText:  "No channels yet — create one in Agents, then connect it here.",
						}),
						// Channel activity — the recent messages that have landed in the
						// connected channel's conversation. Only when hooked (a poll
						// bridge with no channel wakes the agent's own thread instead).
						ui.ExpandIf("💬 Channel activity", "_connected", "", ui.HistoryPanel{
							Source:    "/orchestrate/api/console/bridge-thread?owner={owner}&name={name}",
							Header:    "Recent activity in the connected channel",
							WhoField:  "sender",
							EmptyText: "Nothing delivered into this channel yet.",
						}),
						// Detach — revert to waking the agent's own thread. Only when hooked.
						{Type: "button", Label: "Detach", Method: "POST",
							PostTo:  "/orchestrate/api/console/bridges/set-channel?owner={owner}&name={name}&channel_id=",
							OnlyIf:  "_connected",
							Confirm: "Detach this bridge from its channel? Changes will wake the agent's own thread instead.", Compact: true},
						{Type: "button", Label: "Pause", Method: "POST",
							PostTo: "/orchestrate/api/console/bridges/pause?owner={owner}&name={name}",
							HideIf: "_paused", Compact: true},
						{Type: "button", Label: "Resume", Method: "POST",
							PostTo: "/orchestrate/api/console/bridges/resume?owner={owner}&name={name}",
							OnlyIf: "_paused", Variant: "primary", Compact: true},
						{Type: "button", Label: "Delete", Method: "POST",
							PostTo:  "/orchestrate/api/console/bridges/delete?owner={owner}&name={name}",
							Confirm: "Delete this polling bridge? Its schedule is cancelled; the agent and credential it used are untouched.",
							Compact: true},
					},
					EmptyText: "No polling bridges yet. Ask an agent to \"bridge\" an API (e.g. watch a feed and wake me on new items) — it drafts the credential and creates the poll here.",
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
							// Edit — one place to rename the chat AND link/relink it to
							// an agent's channel. The channel picker reuses
							// connect-channel, which clears any current binding and
							// rebinds, so the SAME control connects an unbound chat and
							// relinks a bound one to a different agent. (Clear, below,
							// stays a one-click unbind.)
							ui.Expand("✎ Edit", ui.Stack{Children: []ui.Component{
								ui.Card{HTML: "<div style=\"font-weight:600;margin-bottom:0.35rem\">Name</div>"},
								ui.FormPanel{
									Source:      "/bridges/api/conv-info/{chat_id}",
									PostURL:     "/bridges/api/conversation/{chat_id}",
									Method:      "PATCH",
									SubmitLabel: "Rename",
									Fields: []ui.FormField{
										{Field: "display_name", Label: "", Type: "text",
											Placeholder: "Family chat"},
									},
								},
								ui.Card{HTML: "<div style=\"font-weight:600;margin:0.6rem 0 0.35rem\">Link to an agent</div><div style=\"opacity:0.7;font-size:0.85em;margin-bottom:0.4rem\">Pick a channel to route this chat to. Replaces the current binding if any.</div>"},
								ui.ActionList{
									Source:     "/bridges/api/agent-channels",
									LabelField: "label",
									DescField:  "desc",
									ButtonText: "Link",
									Method:     "POST",
									PostTo:     "/bridges/api/connect-channel?chat_id={chat_id}&channel_id={id}",
									Confirm:    "Route this chat to this channel's agent? Any current binding is replaced.",
									EmptyText:  "No free channels — create one in Agents (or free one up by clearing another chat), then link it here.",
									Invalidate: []string{"/bridges/api/conversations"},
								},
								// Clear — unbind the channel, freeing it back to the
								// pool. Only shown while connected (row context gates
								// it). Non-destructive: the thread + session are kept.
								ui.Button{
									Label:      "Clear channel binding",
									URL:        "/bridges/api/connect-channel?chat_id={chat_id}&channel_id=",
									Method:     "POST",
									Confirm:    "Clear this chat's channel binding? The channel returns to the available pool. The chat's messages are kept, and reconnecting the same chat resumes its session. To just pause replies, use the auto-reply toggle instead.",
									Invalidate: []string{"/bridges/api/conversations"},
									Variant:    "danger",
									OnlyIf:     "connected",
								},
							}}),
							// (Clear moved into the ✎ Edit panel above, alongside the
							// rename + link controls.)
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
