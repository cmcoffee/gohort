// Build outcomes — the admin view of core/buildledger, and the turn-side hook
// that stamps each verification with the model that ran it.
//
// The ledger is the first stage of tuning the authoring prompts from what
// builds actually did: which failures keep recurring, on which tier, and how
// many verifications it takes to get a tool or an app green. Nothing here
// changes a prompt. It is the evidence a later stage drafts from, and on its
// own it answers "is Builder getting better or worse at this".

package orchestrate

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/buildledger"
	"github.com/cmcoffee/gohort/core/sections"
	"github.com/cmcoffee/gohort/core/ui"
)

// buildStamper carries one loop's prompt clauses to the build ledger and
// stamps each round's verifications with the model and tier that served it.
// The digest arrives after the first response, before any tool runs, so every
// stamp has the clauses.
type buildStamper struct {
	session string
	clauses []string
}

func newBuildStamper(session string) *buildStamper { return &buildStamper{session: session} }

// Both methods are nil-safe: a loop built without a stamper stamps nothing.
func (bs *buildStamper) digest(d PromptDigest) {
	if bs != nil {
		bs.clauses = d.ClauseKeys
	}
}

// step runs from the loop's step callback, which fires after the round's
// tools have returned: the verdicts waiting on this session are this round's.
func (bs *buildStamper) step(info StepInfo) {
	if bs == nil || len(info.ToolCalls) == 0 {
		return
	}
	tier := info.Tier
	if tier == "unset" {
		tier = ""
	}
	buildledger.Stamp(bs.session, info.Model, tier, bs.clauses)
}

// buildOutcomeDays is the window the admin view reads.
const buildOutcomeDays = 30

// handleBuildOutcomes serves the ledger read over the window. GET, admin only.
func (T *OrchestrateApp) handleBuildOutcomes(w http.ResponseWriter, r *http.Request) {
	if !T.requireAdmin(w, r) {
		return
	}
	days := buildOutcomeDays
	if n, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && n > 0 && n <= 365 {
		days = n
	}
	rep := buildledger.Read(time.Now().AddDate(0, 0, -days))

	type episodeRow struct {
		buildledger.Episode
		ID     string `json:"id"`
		Result string `json:"result"`
		Served string `json:"served"`
	}
	recent := make([]episodeRow, 0, len(rep.Recent))
	for i, ep := range rep.Recent {
		res := "open"
		if ep.Green {
			res = "green"
		}
		recent = append(recent, episodeRow{Episode: ep, ID: strconv.Itoa(i), Result: res, Served: servedLabel(ep.Tier, ep.Model)})
	}
	type classRow struct {
		buildledger.ClassStat
		ID string `json:"id"`
	}
	classes := make([]classRow, 0, len(rep.Classes))
	for _, c := range rep.Classes {
		classes = append(classes, classRow{ClassStat: c, ID: c.Kind + "|" + c.Class})
	}
	type tierRow struct {
		buildledger.TierStat
		ID       string `json:"id"`
		FirstPct string `json:"first_pct"`
	}
	tiers := make([]tierRow, 0, len(rep.Tiers))
	for _, ts := range rep.Tiers {
		tiers = append(tiers, tierRow{TierStat: ts, ID: ts.Tier + "|" + ts.Model, FirstPct: outcomePct(ts.FirstTry, ts.Episodes)})
	}

	median := "-"
	if rep.Green > 0 {
		median = fmt.Sprintf("%d (mean %.1f)", rep.MedianToGreen, rep.MeanToGreen)
	}
	writeJSON(w, map[string]any{
		"window":        fmt.Sprintf("last %d days", days),
		"verifications": fmt.Sprintf("%d (%d passed, %d failed, %d unproven)", rep.Verifications, rep.Passes, rep.Fails, rep.Unproven),
		"builds":        fmt.Sprintf("%d (%d green, %d still open)", rep.Episodes, rep.Green, rep.Open),
		"first_try":     outcomePct(rep.FirstTry, rep.Episodes),
		"to_green":      median,
		"recent":        recent,
		"classes":       classes,
		"tiers":         tiers,
	})
}

func servedLabel(tier, model string) string {
	switch {
	case tier == "" && model == "":
		return "unknown"
	case model == "":
		return tier
	case tier == "":
		return model
	}
	return tier + " · " + model
}

func outcomePct(n, of int) string {
	if of == 0 {
		return "-"
	}
	return fmt.Sprintf("%d%% (%d of %d)", n*100/of, n, of)
}

func init() {
	sections.RegisterAdminSection(sections.AdminSectionEntry{App: "/orchestrate", Section: buildOutcomesSection()})
}

func buildOutcomesSection() ui.Section {
	const src = "/orchestrate/api/console/build-outcomes"
	return ui.Section{
		Group:    "Agents",
		Title:    "Build outcomes",
		Subtitle: "Every tool test and app verify agents ran: what failed, on which model, and how many tries it took to get green.",
		Detail: "A build is one push to green: the verifications of a tool or an app from its last pass up to the next one. First-try is a build that passed its first verification; open is one that has not passed yet.\n\n" +
			"Failure kinds are ranked by how many different tools and apps hit them, so a habit shows above one stuck build. Unproven runs found nothing wrong but could not prove the thing works (a write never fired, a tool tested without cases); they count toward tries, not failures.\n\n" +
			"This is evidence only. Nothing here changes a prompt.",
		Wide: true,
		Body: ui.Stack{Children: []ui.Component{
			ui.DisplayPanel{Source: src, Pairs: []ui.DisplayPair{
				{Label: "Window", Field: "window"},
				{Label: "Verifications", Field: "verifications"},
				{Label: "Builds", Field: "builds"},
				{Label: "Green first try", Field: "first_try"},
				{Label: "Tries to green, median", Field: "to_green"},
			}},
			subheading("Failure kinds"),
			ui.Table{Source: src, RecordsField: "classes", RowKey: "id",
				Columns: []ui.Col{
					{Field: "class", Label: "Kind", Flex: 2},
					{Field: "kind", Label: "Target", Mute: true},
					{Field: "verdict", Label: "", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: string(buildledger.Fail), Label: "failed", Color: "danger"},
						{Value: string(buildledger.Unproven), Label: "unproven", Color: "warning"},
					}},
					{Field: "targets", Label: "Builds hit", Format: "thousands"},
					{Field: "count", Label: "Runs", Format: "thousands"},
					{Field: "lead", Label: "Lead", Format: "thousands", Mute: true},
					{Field: "worker", Label: "Worker", Format: "thousands", Mute: true},
					{Field: "last", Label: "Last", Format: "reltime", Mute: true},
				},
				EmptyText: "No failed or unproven verifications in this window."},
			subheading("By tier"),
			ui.Table{Source: src, RecordsField: "tiers", RowKey: "id",
				Columns: []ui.Col{
					{Field: "tier", Label: "Tier"},
					{Field: "model", Label: "Model", Flex: 2, Mute: true},
					{Field: "verifications", Label: "Runs", Format: "thousands"},
					{Field: "fails", Label: "Failed", Format: "thousands"},
					{Field: "episodes", Label: "Builds", Format: "thousands"},
					{Field: "first_pct", Label: "Green first try", Flex: 2},
				},
				EmptyText: "No verifications in this window."},
			subheading("Builds"),
			ui.Table{Source: src, RecordsField: "recent", RowKey: "id",
				Columns: []ui.Col{
					{Field: "end", Label: "When", Format: "reltime", Mute: true},
					{Field: "result", Label: "", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: "green", Label: "green", Color: "success"},
						{Value: "open", Label: "open", Color: "warning"},
					}},
					{Field: "target", Label: "Target", Flex: 2},
					{Field: "kind", Label: "", Mute: true},
					{Field: "attempts", Label: "Tries"},
					{Field: "classes", Label: "Failed on", Type: "pills", Flex: 3},
					{Field: "served", Label: "Served by", Flex: 2, Mute: true},
					{Field: "owner", Label: "Owner", Mute: true},
				},
				Search: true, SearchPlaceholder: "Filter by target, owner or failure kind",
				EmptyText: "No builds verified in this window."},
		}},
	}
}
