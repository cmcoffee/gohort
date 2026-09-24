package admin

import (
	"github.com/cmcoffee/gohort/core/ui"
)

// governanceSections is the governance part of the admin page: User-owned credentials, Global tools, User-owned agents, User-owned pipelines, User-owned machines, Pending promotions.
func (a *AdminApp) governanceSections() []ui.Section {
	return []ui.Section{
		// First: the deployment's obligations frame everything else here.
		rulesSection(),
		{
			Title:    "User-owned credentials",
			Subtitle: "Credentials users create for themselves, on their Extensions page.",
			Detail: "The admin API Credentials list above shows only GLOBAL creds, so without this the admin plane is blind to these.\n\n" +
				"The Lent column is the one to read first. A READ lend returns data the borrower could have asked the owner for; a WRITE lend arrives at the far end as the OWNER, so the page says they edited it and only the dispatch ledger records who actually made the call.\n\n" +
				"Revoke sharing clears both lists and the owner keeps their key. Narrowing a lend from writes to reads is the owner's to do, not the admin's: either the lend is acceptable or it stops. Disable revokes the credential itself without deleting it, for the owner and every borrower at once; Delete removes it and its encrypted secret.",
			Body: ui.Table{
				Source: "api/user-credentials",
				RowKey: "id",
				Columns: []ui.Col{
					{Field: "owner", Flex: 0, Label: "Owner"},
					{Field: "name", Flex: 1},
					{Field: "type", Flex: 0, Mute: true},
					{Field: "lent_to", Flex: 2, Mute: true, Label: "Lent to"},
					{Field: "lends_writes", Flex: 0, Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Writes as owner", Color: "warning"},
					}},
					{Field: "secured", Flex: 0, Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Secured", Color: "mute"},
					}},
					{Field: "disabled", Flex: 0, Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Disabled", Color: "danger"},
						{Value: false, Label: "Enabled", Color: "success"},
					}},
				},
				RowActions: []ui.RowAction{
					{Type: "button", Label: "Enable",
						PostTo: "api/user-credentials?action=enable&owner={owner}&name={name}",
						Method: "POST", OnlyIf: "disabled"},
					{Type: "button", Label: "Disable",
						PostTo: "api/user-credentials?action=disable&owner={owner}&name={name}",
						Method: "POST", HideIf: "disabled"},
					{Type: "button", Label: "Revoke sharing",
						PostTo:  "api/user-credentials?action=revoke_share&owner={owner}&name={name}",
						Method:  "POST",
						Confirm: "Revoke every share of this credential? Everyone it was lent to loses it immediately; the owner keeps the key and can still use it.",
						OnlyIf:  "lent",
						Variant: "danger"},
					{Type: "button", Label: "Delete",
						PostTo:  "api/user-credentials?owner={owner}&name={name}",
						Method:  "DELETE",
						Confirm: "Delete this user's credential? The encrypted secret goes with it. This cannot be undone.",
						Variant: "danger"},
				},
				EmptyText: "No user-owned credentials. When a user creates one on their Extensions page, it appears here.",
			},
		},
		{
			Title:    "Global tools",
			Subtitle: "What this deployment publishes, and who may take it.",
			Detail: "A tool reaches other people on three rungs: its owner's alone, shared by them with people they name, or published here to the deployment catalog. This is the third — the one an admin grants.\n\n" +
				"Published is not loaded. Each user opts in from their own Extensions catalog and picks which of their agents load it, which is their business and not shown here: an admin who wants somebody to stop using a tool takes away the permission rather than the preference. Access does that for one person; Unshare does it for everybody.\n\n" +
				"What users run is the VERSION you approved, not the owner's current copy. The owner's edits reach only their own agents until they request an update, which arrives in Pending promotions with the changes to review; approving makes it the next version for everyone. History keeps the last five versions, and any of them can be rolled back to.\n\n" +
				"Publish a tool, set its access, and inspect what it actually does on the Tools page.",
			Body: ui.Table{
				Source: "api/global-tools",
				RowKey: "id",
				Columns: []ui.Col{
					{Field: "tool", Flex: 1},
					{Field: "owner", Flex: 0, Label: "Published by"},
					{Field: "version", Flex: 0, Label: "Version", Mute: true},
					{Field: "approved_at", Format: "reltime", Flex: 0, Mute: true, Label: "Approved"},
					{Field: "access", Flex: 2, Mute: true, Label: "May be taken by"},
					{Field: "restricted", Flex: 0, Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Restricted", Color: "info"},
					}},
					{Field: "update_requested", Flex: 0, Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Update requested", Color: "warning"},
					}},
				},
				RowActions: []ui.RowAction{
					// The owner's requested next version, against the one users
					// run now. Approve or Deny it in Pending promotions below.
					ui.ExpandIf("Review update", "update_requested", "", ui.RecordView{
						Pairs: []ui.DisplayPair{{Label: "Requested changes", Field: "review", Block: true}},
					}),
					// The versions this one replaced, each with what rolling
					// back to it would change.
					ui.ExpandIf("History", "has_history", "", ui.Table{
						Source: "api/global-tools?history={tool}",
						RowKey: "past_version",
						Columns: []ui.Col{
							{Field: "label", Flex: 0, Label: "Version"},
							{Field: "approved_at", Format: "reltime", Flex: 0, Mute: true, Label: "Approved"},
							{Field: "approved_by", Flex: 1, Mute: true, Label: "By"},
						},
						RowActions: []ui.RowAction{
							ui.Expand("Changes", ui.RecordView{
								Pairs: []ui.DisplayPair{{Label: "Rolling back to this version changes", Field: "changes", Block: true}},
							}),
							{Type: "button", Label: "Roll back",
								PostTo:     "api/global-tools?action=rollback&tool={tool}&owner={owner}&to={past_version}",
								Method:     "POST",
								Confirm:    "Make this version the one every user who added the tool runs? The current version is kept in the history.",
								Invalidate: []string{"api/global-tools"}},
						},
						EmptyText: "No earlier versions kept.",
					}),
					// The same door the Tools page opens, rendered where the
					// question is asked. Who may take a published tool is the
					// governance question about it, so answering it here saves
					// a trip to a page about something else.
					ui.ModalAction("Access", ui.ACLPicker(ui.ACLPickerConfig{
						OptionsSource: "api/user-candidates",
						RecordSource:  "api/persistent-tools?allowed_users={tool}&owner={owner}",
						Field:         "allowed_users",
						PostTo:        "api/persistent-tools?action=set_allowed_users&name={tool}&owner={owner}",
						Method:        "POST",
						Noun:          "user",
						Intro:         "Which users may take this tool from their Extensions catalog. Empty = every user. Each of them then chooses which of their own agents load it.",
						EmptyText:     "No other users to grant yet.",
						Invalidate:    []string{"api/global-tools"},
					})),
					{Type: "button", Label: "Unshare",
						PostTo:  "api/persistent-tools?action=unshare&name={tool}&owner={owner}",
						Method:  "POST",
						Confirm: "Take this tool out of the shared catalog? Everyone who adopted it stops loading it, and it goes back to being its owner's alone.",
						Variant: "danger"},
				},
				EmptyText: "The deployment publishes no tools. A user asks for one to be published from their Extensions page, and it appears in Pending promotions below.",
			},
		},
		{
			Title:    "User-owned agents",
			Subtitle: "Agents users create, and optionally peer-share with specific other users.",
			Detail:   "Sharing is user-initiated; this is the admin's audit and revoke. A shared agent runs in its owner's context with each recipient's own credentials, so no secret travels.\n\nRevoke share clears the recipient list and the owner keeps the agent. Empty recipients means private.",
			Body: ui.Table{
				Source: "api/user-agents",
				RowKey: "id",
				Columns: []ui.Col{
					{Field: "owner", Flex: 0, Label: "Owner"},
					{Field: "name", Flex: 1},
					{Field: "shared_with", Flex: 2, Mute: true, Label: "Shared with"},
					{Field: "shared", Flex: 0, Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Shared", Color: "info"},
					}},
					{Field: "exposed", Flex: 0, Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Published", Color: "success"},
					}},
				},
				RowActions: []ui.RowAction{
					// Delegate to users — publish the agent as a /agents/ app the
					// admin can then grant. Shown until it's published; the owner's
					// peer-share stays intact.
					{Type: "button", Label: "Publish",
						PostTo:  "api/user-agents?action=publish&owner={owner}&id={id}",
						Method:  "POST",
						Confirm: "Publish this agent as a dashboard app? It becomes available at /agents/, which you can then grant to users. The owner's existing share is unchanged.",
						HideIf:  "exposed"},
					{Type: "button", Label: "Revoke share",
						PostTo:  "api/user-agents?action=revoke_share&owner={owner}&id={id}",
						Method:  "POST",
						Confirm: "Revoke this agent's sharing? Its recipients lose access; the owner keeps the agent.",
						OnlyIf:  "shared",
						Variant: "danger"},
				},
				EmptyText: "No user-owned agents yet. When a user creates one and shares it, it appears here.",
			},
		},
		{
			Title:    "User-owned pipelines",
			Subtitle: "Pipelines users author, and optionally peer-share.",
			Detail:   "The same audit as agents above, for the other half of the user plane: a share you cannot see is a share you cannot govern.\n\nA shared pipeline is a RECIPE. Recipients run the owner's definition against their own agents, tools and credentials, and cannot edit it. Revoke clears the recipient list; the owner keeps the pipeline.",
			Body: ui.Table{
				Source: "api/user-pipelines",
				RowKey: "id",
				Columns: []ui.Col{
					{Field: "owner", Flex: 0, Label: "Owner"},
					{Field: "name", Flex: 1},
					{Field: "stages", Flex: 0, Label: "Stages"},
					{Field: "shared_with", Flex: 2, Mute: true, Label: "Shared with"},
					{Field: "shared", Flex: 0, Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Shared", Color: "info"},
					}},
					{Field: "published", Flex: 0, Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Deployment-wide", Color: "warning"},
					}},
				},
				RowActions: []ui.RowAction{
					// No Publish twin: a pipeline has no /agents/-style app
					// surface to flip on, and a button that does nothing is
					// worse than an absent one.
					{Type: "button", Label: "Revoke share",
						PostTo:  "api/user-pipelines?action=revoke_share&owner={owner}&id={id}",
						Method:  "POST",
						Confirm: "Revoke this pipeline's sharing? Its recipients lose access; the owner keeps the pipeline.",
						OnlyIf:  "shared",
						Variant: "danger"},
				},
				EmptyText: "No user-owned pipelines yet. When a user authors one, it appears here.",
			},
		},
		{
			Title:    "User-owned machines",
			Subtitle: "Machines users author, and optionally peer-share.",
			Detail:   "The third kind in the user plane, governed exactly like the other two.\n\nA shared machine is a PROCEDURE. Recipients run the owner's definition against their own agents, tools and credentials, and cannot edit it. A machine marked Runs can be put on a timetable or dispatched by an agent; the rest are only ever reached by a person talking to them.\n\nRevoke clears the recipient list; the owner keeps the machine.",
			Body: ui.Table{
				Source: "api/user-machines",
				RowKey: "id",
				Columns: []ui.Col{
					{Field: "owner", Flex: 0, Label: "Owner"},
					{Field: "name", Flex: 1},
					{Field: "steps", Flex: 0, Label: "Steps"},
					{Field: "shared_with", Flex: 2, Mute: true, Label: "Shared with"},
					{Field: "published", Flex: 0, Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Deployment-wide", Color: "warning"},
					}},
					{Field: "unattended", Flex: 0, Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Runs", Color: "success"},
					}},
					{Field: "shared", Flex: 0, Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Shared", Color: "info"},
					}},
				},
				RowActions: []ui.RowAction{
					{Type: "button", Label: "Revoke share",
						PostTo:  "api/user-machines?action=revoke_share&owner={owner}&id={id}",
						Method:  "POST",
						Confirm: "Revoke this machine's sharing? Its recipients lose access, including any schedule they armed against it, which is marked broken rather than left to fail. The owner keeps the machine.",
						OnlyIf:  "shared",
						Variant: "danger"},
				},
				EmptyText: "No user-owned machines yet. When a user authors one, it appears here.",
			},
		},
		{
			Title:    "Pending promotions",
			Subtitle: "Users' bottom-up requests to publish their own resources deployment-wide.",
			Detail: "Approve a tool request to Share it to the global catalog, where each user then opts in from their Extensions page. A tool request marked Update asks for a published tool's next version: Review shows what changes, and approving it changes what every user who added the tool runs. What you approve is the definition as it was when the owner asked.\n\n" +
				"Approve an app request to share it with every signed-in user: each gets their own copy, and its scripts run with the owner's credentials, which is why an admin sees it first.\n\n" +
				"Approve a public-link request to mint the app's anonymous link: anyone who has the URL then runs its data sources as the owner, with no login.\n\n" +
				"A CREDENTIAL request is the one that is not a widening. The requester is handing their key to the deployment: the secret moves into the global namespace, the credential lands secured so that it has no user list and is reachable only through the tools bound to it, and the requester becomes an ordinary user of it. They cannot take it back afterwards — read the note and be sure the deployment should own this key, because the alternative to keeping it is deleting it.\n\n" +
				"Approve a SKILL request to move it from its author's list into the deployment's, where the classifier can activate it on any user's turn. Its bundled tools do not go with it, for the same reason a shared tool needs its own approval; its attached collections travel as references that only answer for people who can already read them. The author keeps editing it and can take it back without asking.\n\n" +
				"A PIPELINE or MACHINE request widens a recipe. Neither record moves and nothing of the author's travels: every run happens in the namespace of whoever started it, against their agents, tools and credentials. The author keeps editing it and can take it back without asking.\n\n" +
				"Deny to dismiss.",
			Body: ui.Table{
				Source: "api/promotions",
				RowKey: "id",
				Columns: []ui.Col{
					{Field: "owner", Flex: 0, Label: "Requested by"},
					// One label per publishable kind. A kind with no label here
					// still renders (as its raw value) — add the label when the
					// approver arrives.
					{Field: "kind", Flex: 0, Type: "badge", Badges: []ui.BadgeMapping{
						{Value: "tool", Label: "Tool", Color: "mute"},
						{Value: "app", Label: "App", Color: "info"},
						// One label per kind that can actually be FILED, per the
						// note above. "public_link" went with the anonymous app
						// surface, so it was a label for a row nothing could
						// produce.
						{Value: "agent", Label: "Agent", Color: "warning"},
						{Value: "collection", Label: "Collection", Color: "info"},
						// Danger, alone among the kinds, because approving it
						// transfers something rather than widening it: the key
						// stops being the requester's and the move is not one
						// they can undo.
						{Value: "credential", Label: "Credential", Color: "danger"},
						{Value: "skill", Label: "Skill", Color: "info"},
						{Value: "pipeline", Label: "Pipeline", Color: "info"},
						{Value: "machine", Label: "Machine", Color: "info"},
					}},
					{Field: "update", Flex: 0, Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Update", Color: "warning"},
					}},
					{Field: "name", Flex: 1},
					{Field: "note", Flex: 2, Mute: true},
					{Field: "created", Format: "reltime", Flex: 0, Mute: true},
				},
				RowActions: []ui.RowAction{
					// What approving a tool request would publish: a diff
					// against the published version, or the whole new tool.
					ui.ExpandIf("Review", "review", "", ui.RecordView{
						Pairs: []ui.DisplayPair{{Label: "What approving publishes", Field: "review", Block: true}},
					}),
					{Type: "button", Label: "Approve",
						PostTo:  "api/promotions?action=approve&id={id}",
						Method:  "POST",
						Confirm: "Approve this publish request? It goes live for its audience at once, running with its owner's credentials. A CREDENTIAL request instead transfers the key to the deployment, secured, and the requester cannot take it back.",
						// Approving a tool promotion shares it deployment-wide
						// — which is a badge on that tool's row two sections
						// down, in the table this queue exists to feed.
						Invalidate: []string{"api/persistent-tools", "api/global-tools"}},
					{Type: "button", Label: "Deny",
						PostTo:  "api/promotions?action=deny&id={id}",
						Method:  "POST",
						Confirm: "Deny this promotion request?",
						Variant: "danger"},
				},
				EmptyText: "No pending promotion requests.",
			},
		},
	}
}
