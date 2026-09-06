package admin

import (
	"github.com/cmcoffee/gohort/core/ui"
)

// governanceSections is the governance part of the admin page: User-owned credentials, Global-tool adoptions, User-owned agents, User-owned pipelines, User-owned machines, Pending promotions.
func (a *AdminApp) governanceSections() []ui.Section {
	return []ui.Section{
		{
			Title:    "User-owned credentials",
			Subtitle: "Credentials users create for themselves (on their Extensions page) — the admin API Credentials list above shows only GLOBAL creds, so without this the admin plane is blind to these. Disable revokes a credential without deleting it (the owner keeps the record; it stops resolving); Delete removes it and its encrypted secret. (User-owned agents will join this governance area once peer-sharing ships.)",
			Body: ui.Table{
				Source: "api/user-credentials",
				RowKey: "id",
				Columns: []ui.Col{
					{Field: "owner", Flex: 0, Label: "Owner"},
					{Field: "name", Flex: 1},
					{Field: "type", Flex: 0, Mute: true},
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
			Title:    "Global-tool adoptions",
			Subtitle: "Who has pulled each SHARED global tool into their fleet (opt-in from their Extensions catalog). Shows a shared tool's blast radius before you revoke it, and lets you force-remove one user's adoption. A ⚠ row is a stale adoption — the tool has since left the shared catalog. Removing an adoption stops that user's agents loading the tool until they re-adopt (if still permitted by its access list).",
			Body: ui.Table{
				Source: "api/tool-adoptions",
				RowKey: "id",
				Columns: []ui.Col{
					{Field: "tool", Flex: 1},
					{Field: "user", Flex: 0, Label: "Adopted by"},
					{Field: "stale", Flex: 0, Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "⚠ tool unshared", Color: "warning"},
					}},
				},
				RowActions: []ui.RowAction{
					{Type: "button", Label: "Remove",
						PostTo:  "api/tool-adoptions?action=unadopt&user={user}&name={tool}",
						Method:  "POST",
						Confirm: "Remove this user's adoption of the tool? Their agents stop loading it until they re-adopt (if still permitted).",
						Variant: "danger"},
				},
				EmptyText: "No global-tool adoptions yet. When a user adopts a shared tool from their Extensions catalog, it appears here.",
			},
		},
		{
			Title:    "User-owned agents",
			Subtitle: "Agents users create and (optionally) peer-share with specific other users. Sharing is user-initiated — this is the admin's audit + revoke. A shared agent runs in its owner's context with each recipient's own credentials (no secret travels). Revoke share clears the recipient list; the owner keeps the agent. Empty recipients = private.",
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
			Subtitle: "Pipelines users author and (optionally) peer-share. Same audit as agents above, for the other half of the user plane — a share you cannot see is a share you cannot govern. A shared pipeline is a RECIPE: recipients run the owner's definition against their own agents, tools and credentials, and cannot edit it. Revoke clears the recipient list; the owner keeps the pipeline.",
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
			Subtitle: "Machines users author and (optionally) peer-share — the third kind in the user plane, and governed exactly like the other two. A shared machine is a PROCEDURE: recipients run the owner's definition against their own agents, tools and credentials, and cannot edit it. A machine marked Runs can be put on a timetable or dispatched by an agent; the rest are only ever reached by a person talking to them. Revoke clears the recipient list; the owner keeps the machine.",
			Body: ui.Table{
				Source: "api/user-machines",
				RowKey: "id",
				Columns: []ui.Col{
					{Field: "owner", Flex: 0, Label: "Owner"},
					{Field: "name", Flex: 1},
					{Field: "steps", Flex: 0, Label: "Steps"},
					{Field: "shared_with", Flex: 2, Mute: true, Label: "Shared with"},
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
						Confirm: "Revoke this machine's sharing? Its recipients lose access — including any schedule they armed against it, which is marked broken rather than left to fail. The owner keeps the machine.",
						OnlyIf:  "shared",
						Variant: "danger"},
				},
				EmptyText: "No user-owned machines yet. When a user authors one, it appears here.",
			},
		},
		{
			Title:    "Pending promotions",
			Subtitle: "Users' bottom-up requests to publish their own resources deployment-wide. Approve a tool request to Share it to the global catalog (each user then opts in from their Extensions page); Deny to dismiss. Credential and agent promotion arrive with their approve paths.",
			Body: ui.Table{
				Source: "api/promotions",
				RowKey: "id",
				Columns: []ui.Col{
					{Field: "owner", Flex: 0, Label: "Requested by"},
					{Field: "kind", Flex: 0},
					{Field: "name", Flex: 1},
					{Field: "note", Flex: 2, Mute: true},
					{Field: "created", Format: "reltime", Flex: 0, Mute: true},
				},
				RowActions: []ui.RowAction{
					{Type: "button", Label: "Approve",
						PostTo: "api/promotions?action=approve&id={id}",
						Method: "POST",
						// Approving a tool promotion shares it deployment-wide
						// — which is a badge on that tool's row two sections
						// down, in the table this queue exists to feed.
						Invalidate: []string{"api/persistent-tools"}},
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
