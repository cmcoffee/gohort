package orchestrate

// The Security page's Activity tab: what an agent DID, beside the tabs that
// say what it CAN do. Read-only, and it owns no store: every row comes from a
// record something else already keeps.
//
//   - Tool calls from its chat threads (view=activity): the calls persisted on
//     each assistant message, since an ordinary chat turn writes no run
//     record. Its sub-agents' calls are included, under their own names.
//   - Credentials it used (view=credentials): the credential audit rings,
//     which name the agent that made each call (SecureAPIAuditEntry.Agent).
//     A row from before they did names none and is not counted, never guessed.
//
// Runs, guardrail blocks and spend come from the console's own endpoints,
// narrowed to the agent (page_agent_access.go).

import (
	"context"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/revisions"
	"github.com/cmcoffee/gohort/core/ui"
)

// activityWindow is how far back the tab reads, and activityMax how many
// tool calls it shows at most, newest first; the table's search narrows
// within them.
const (
	activityWindow = 30 * 24 * time.Hour
	activityMax    = 300
)

// accessActivityRow is one tool call an agent made in a chat thread.
type accessActivityRow struct {
	When    string `json:"when"`
	Agent   string `json:"agent,omitempty"` // a sub-agent's name; empty for the agent itself
	Tool    string `json:"tool"`
	Band    string `json:"band,omitempty"`
	Outcome string `json:"outcome"`
	Detail  string `json:"detail,omitempty"`
	Thread  string `json:"thread,omitempty"`
	ID      string `json:"_id"`
	at      time.Time
}

// activityFamily is the agent and its sub-agents, the agent first: a
// sub-agent's work is done for its parent and reads as part of it.
func activityFamily(udb Database, user string, rec AgentRecord) []AgentRecord {
	out := []AgentRecord{rec}
	for _, a := range listAgents(udb, user) {
		if a.ID != rec.ID && strings.TrimSpace(a.OwnedBy) == rec.ID {
			out = append(out, a)
		}
	}
	return out
}

// accessActivityRows is the tool calls rec and its sub-agents made in chat
// threads within the window, newest first, each with the band the Tools tab
// gives that tool.
func (T *OrchestrateApp) accessActivityRows(ctx context.Context, udb Database, user string, rec AgentRecord, now time.Time) []accessActivityRow {
	loc := UserLocation(user)
	since := now.Add(-activityWindow)
	rows := []accessActivityRow{}
	for _, a := range activityFamily(udb, user, rec) {
		bands := map[string]string{}
		if tools, err := T.resolvedAgentTools(ctx, udb, user, a); err == nil {
			for _, t := range tools {
				bands[t.Name] = t.Band
			}
		}
		who := ""
		if a.ID != rec.ID {
			who = chFirst(a.Name, a.ID)
		}
		for _, s := range listChatSessions(udb, a.ID) {
			full, ok := loadChatSession(udb, a.ID, s.ID)
			if !ok {
				continue
			}
			for mi, m := range full.Messages {
				if len(m.ToolCalls) == 0 || m.Created.Before(since) {
					continue
				}
				for ci, tc := range m.ToolCalls {
					row := accessActivityRow{
						When:    m.Created.In(loc).Format("Jan 2 15:04"),
						Agent:   who,
						Tool:    chFirst(tc.Label, tc.Name),
						Band:    bands[tc.Name],
						Outcome: "done",
						Thread:  full.Title,
						ID:      a.ID + "/" + full.ID + "/" + strconv.Itoa(mi) + "/" + strconv.Itoa(ci),
						at:      m.Created,
					}
					switch {
					case tc.Err != "":
						row.Outcome, row.Detail = "failed", truncateObs(tc.Err, 160)
					case tc.Cached:
						row.Outcome = "cached"
					}
					rows = append(rows, row)
				}
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].at.After(rows[j].at) })
	if len(rows) > activityMax {
		rows = rows[:activityMax]
	}
	return rows
}

// accessCredentialUseRow is one credential's use by an agent in the window.
type accessCredentialUseRow struct {
	Credential string `json:"credential"`
	Calls      int    `json:"calls"`
	Writes     int    `json:"writes"`
	Errors     int    `json:"errors"`
	Sent       string `json:"sent,omitempty"` // the hosts it wrote to
	Last       string `json:"last"`
	ID         string `json:"_id"`
	last       time.Time
}

// accessCredentialUseRows is each credential rec or one of its sub-agents
// called within the window, read from the credential audit rings: calls,
// writes (anything but GET and HEAD), errors (refused, failed, or a status of
// 400 and up), the hosts it wrote to, and when it last used it. Only rows
// that name one of these agents and this user count.
func accessCredentialUseRows(udb Database, user string, rec AgentRecord, now time.Time) []accessCredentialUseRow {
	family := map[string]bool{}
	for _, a := range activityFamily(udb, user, rec) {
		family[a.ID] = true
	}
	since := now.Add(-activityWindow)
	loc := UserLocation(user)
	seen := map[string]bool{}
	var creds []SecureCredential
	add := func(c SecureCredential) {
		if k := c.Owner + "\x00" + c.Name; !seen[k] {
			seen[k] = true
			creds = append(creds, c)
		}
	}
	for _, c := range Secure().ListUser(user) {
		add(c)
	}
	for _, c := range Secure().SharedWithUser(user) {
		add(c)
	}
	for _, c := range Secure().List() {
		if Secure().UserMayUse(c, user) {
			add(c)
		}
	}
	rows := []accessCredentialUseRow{}
	for _, c := range creds {
		row := accessCredentialUseRow{Credential: c.Name, ID: c.Owner + ":" + c.Name}
		hosts := map[string]bool{}
		for _, e := range Secure().LoadAudit(c.Owner, c.Name) {
			if !family[e.Agent] || e.DispatchedBy != user || e.Timestamp.Before(since) {
				continue
			}
			row.Calls++
			if e.Error != "" || e.Status >= 400 {
				row.Errors++
			}
			if m := strings.ToUpper(strings.TrimSpace(e.Method)); m != "" && m != "GET" && m != "HEAD" {
				row.Writes++
				if u, err := url.Parse(e.URL); err == nil && u.Host != "" {
					hosts[u.Host] = true
				}
			}
			if e.Timestamp.After(row.last) {
				row.last = e.Timestamp
			}
		}
		if row.Calls == 0 {
			continue
		}
		var hs []string
		for h := range hosts {
			hs = append(hs, h)
		}
		sort.Strings(hs)
		row.Sent = strings.Join(hs, ", ")
		row.Last = row.last.In(loc).Format("Jan 2 15:04")
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].last.After(rows[j].last) })
	return rows
}

// activitySections is the Activity tab: what the agent did, read from the
// records the rest of the page's tabs sit beside. Nothing on it is a control.
func (ap *accessPage) activitySections() []ui.Section {
	T := ap.T
	agent := url.QueryEscape(ap.agent.ID)
	return []ui.Section{
		{
			Group: "Activity",
			Title: "Tool calls in its chats",
			Subtitle: "Every tool it called in a chat thread over the last 30 days, newest first, up to 300. A sub-agent's calls are here under its name. " +
				"The band is the one the Tools tab gives that tool now.",
			Detail: "Read from the calls kept on each reply, since a chat turn writes no run record. What its schedules, monitors and delegations did is under Runs.",
			Body: ui.Table{
				Source:            ap.src + "&view=activity",
				RowKey:            "_id",
				Search:            true,
				SearchPlaceholder: "Find a tool, thread or error",
				EmptyText:         "No tool calls in its chats in the last 30 days.",
				Columns: []ui.Col{
					{Field: "when", Label: "When", Mute: true},
					{Field: "agent", Label: "Sub-agent", Mute: true},
					{Field: "tool", Label: "Tool", Flex: 2},
					{Field: "band", Label: "Band", Mute: true},
					{Field: "outcome", Label: "Outcome"},
					{Field: "detail", Label: "", Flex: 2, Mute: true},
					{Field: "thread", Label: "Thread", Flex: 2, Mute: true},
				},
			},
		},
		{
			Group: "Activity",
			Title: "Changes to it",
			Subtitle: "Each edit its version history kept, newest first: what changed, and who, where an agent or a maintainer made it. " +
				"The version is the one the edit replaced, which Revert on the editor can bring back.",
			Body: ui.Table{
				Source:    ap.src + "&view=changes",
				RowKey:    "_id",
				EmptyText: "No edits kept yet.",
				Columns: []ui.Col{
					{Field: "when", Label: "When", Mute: true},
					{Field: "what", Label: "Change", Flex: 3},
					{Field: "version", Label: "Replaced", Mute: true},
				},
			},
		},
		{
			Group:    "Activity",
			Title:    "Runs",
			Subtitle: "Its scheduled, standing, monitor, trigger and delegated runs, newest first.",
			Body: ui.Table{
				Source:    T.WebPrefix() + "/api/console/runs?agent=" + agent,
				RowKey:    "_id",
				EmptyText: "No runs on record.",
				Columns: []ui.Col{
					{Field: "when", Label: "When", Mute: true},
					{Field: "task", Label: "Task", Flex: 2},
					{Field: "trigger", Label: "Started by", Mute: true},
					{Field: "Status", Label: "Status"},
					{Field: "summary", Label: "", Flex: 3, Mute: true},
				},
			},
		},
		{
			Group: "Activity",
			Title: "Credentials it used",
			Subtitle: "Each credential it or a sub-agent called in the last 30 days: calls, writes (anything but a read), errors, and the hosts it wrote to. " +
				"A call made before calls were recorded with their agent does not name one, so it is not counted here.",
			Body: ui.Table{
				Source:    ap.src + "&view=credentials",
				RowKey:    "_id",
				EmptyText: "It used no credential in the last 30 days.",
				Columns: []ui.Col{
					{Field: "credential", Label: "Credential", Flex: 2},
					{Field: "calls", Label: "Calls", Format: "thousands"},
					{Field: "writes", Label: "Writes", Format: "thousands"},
					{Field: "errors", Label: "Errors", Format: "thousands"},
					{Field: "sent", Label: "Wrote to", Flex: 2, Mute: true},
					{Field: "last", Label: "Last used", Mute: true},
				},
			},
		},
		{
			Group:    "Activity",
			Title:    "Stopped by its guardrails",
			Subtitle: "What a guardrail kept it from doing, newest first.",
			Body: ui.Table{
				Source:    T.WebPrefix() + "/api/console/guardrail-blocks?agent=" + agent,
				RowKey:    "at",
				EmptyText: "No guardrail has stopped it.",
				Columns: []ui.Col{
					{Field: "at", Label: "When", Format: "reltime", Mute: true},
					{Field: "name", Label: "Rule", Flex: 2},
					{Field: "where", Label: "Where", Mute: true},
					{Field: "reason", Label: "Why", Flex: 2, Mute: true},
				},
			},
		},
		{
			Group:    "Activity",
			Title:    "Spend",
			Subtitle: "What its model use has cost, banked per run.",
			Body: ui.Table{
				Source:    T.WebPrefix() + "/api/console/spend?agent=" + agent,
				RowKey:    "_id",
				EmptyText: "No spend on record.",
				Columns: []ui.Col{
					{Field: "today", Label: "Today"},
					{Field: "7_days", Label: "7 days"},
					{Field: "30_days", Label: "30 days"},
					{Field: "runs_30d", Label: "Runs (30 days)", Mute: true},
					{Field: "last_active", Label: "Last active", Mute: true},
				},
			},
		},
	}
}

// accessChangeRow is one edit to an agent, from its version history.
type accessChangeRow struct {
	When    string `json:"when"`
	What    string `json:"what"`
	Version string `json:"version"`
	ID      string `json:"_id"`
}

// accessChangeRows is rec's edits, newest first, from the versions its
// history kept. A kept version names the edit that REPLACED it, and that
// edit was made when the next version was written: the newer version's
// stamp, or for the newest kept one, the agent's own last save.
func accessChangeRows(udb Database, user string, rec AgentRecord) []accessChangeRow {
	loc := UserLocation(user)
	revs := revisions.List(udb, revisions.KindAgent, rec.ID) // newest first
	rows := []accessChangeRow{}
	for i, rev := range revs {
		when := rec.Updated
		if i > 0 {
			if t, err := time.Parse(time.RFC3339, revs[i-1].Stamp); err == nil {
				when = t
			}
		}
		row := accessChangeRow{
			What:    chFirst(rev.Reason, "update"),
			Version: "#" + strconv.Itoa(rev.Seq),
			ID:      strconv.Itoa(rev.Seq),
		}
		if !when.IsZero() {
			row.When = when.In(loc).Format("Jan 2 15:04")
		}
		rows = append(rows, row)
	}
	return rows
}
