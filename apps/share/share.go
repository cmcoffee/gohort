// Package share answers the two questions about sharing that no single record
// can: what have I given out, and what do I have because somebody gave it to
// me.
//
// The per-thing controls stay exactly where they are. You share an agent from
// the agent page you are already on, a skill from the skills list, a credential
// from the credential card — sharing a thing belongs with the thing, and making
// somebody come here first to pick it out of a list would be adding navigation
// to solve navigation.
//
// What belongs here is what belongs nowhere else. Both questions above are
// cross-kind by nature, so neither can live on any one page: auditing what you
// have handed out currently means walking seven surfaces and remembering which
// door each grant went through, and "what do I have that isn't mine" gets a
// different answer in four different places.
//
// This app names no kind. Every row comes from core/shareledger, which each
// owning package registers into, so a kind added later appears here by
// registering and nothing in this file changes.
package share

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/shareledger"
	"github.com/cmcoffee/gohort/core/ui"
)

func init() { RegisterApp(new(ShareApp)) }

type ShareApp struct {
	AppCore
}

func (T ShareApp) Name() string         { return "share" }
func (T ShareApp) SystemPrompt() string { return "" }
func (T ShareApp) Desc() string {
	return "Apps: What you have shared with other people, and what they have shared with you."
}

func (T *ShareApp) Init() error { return T.Flags.Parse() }

func (T *ShareApp) Main() error {
	Log("Share is a dashboard-only app. Start with:\n  gohort serve :8080")
	return nil
}

func (T *ShareApp) WebPath() string { return "/share" }
func (T *ShareApp) WebName() string { return "Sharing" }
func (T *ShareApp) WebDesc() string {
	return "Everything you have shared, and everything shared with you."
}

func (T *ShareApp) Routes() {
	T.HandleFunc("/api/mine", T.serveMine)
	T.HandleFunc("/api/to-me", T.serveToMe)
	T.HandleFunc("/api/carries", T.serveCarries)
	T.HandleFunc("/api/revoke", T.serveRevoke)
	T.HandleFunc("/api/plan", T.servePlan)
	T.HandleFunc("/api/apply", T.serveApply)
	T.HandleFunc("/plan", T.servePlanPage)
	T.HandleFunc("/", T.servePage)
}

// row is the wire shape. Deliberately the same for both directions: one table
// component, one set of columns, and the difference between "who has it" and
// "who gave it to me" is which column the server filled.
type row struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	Name      string `json:"name"`
	Who       string `json:"who"`
	Reach     string `json:"reach,omitempty"`
	Detail    string `json:"detail,omitempty"`
	Wide      bool   `json:"wide"`
	Revocable bool   `json:"revocable"`
	// Recipient is the single name a Revoke button acts on, empty when the
	// row covers several. A grant to three people revokes as three rows
	// rather than one button that silently takes back more than it says.
	Recipient string `json:"recipient,omitempty"`
	// Needs marks a row that is waiting on the reader. Something shared with
	// you that does not work yet, and does not say so, is worse than not
	// having it: you find out by running it.
	Needs bool `json:"needs,omitempty"`
	// Carries says whether anything comes WITH this, so the expand appears
	// only on rows that have something behind it. Record is the id the expand
	// asks about, kept apart from ID because that one is made unique per row.
	Carries bool   `json:"carries,omitempty"`
	Record  string `json:"record,omitempty"`
	// Uses is who relies on this and through what, in their words: the
	// recipient's own agents that reference it. Confirm is the take-back
	// question for THIS row, naming them, since "they lose it" and "their
	// Triage agent stops working" are different decisions.
	Uses    string `json:"uses,omitempty"`
	Confirm string `json:"confirm,omitempty"`
}

func (T *ShareApp) serveMine(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	rows := []row{}
	for _, g := range shareledger.Mine(user) {
		// One row per RECIPIENT. A grant naming three people is three
		// decisions, and a single Revoke over all of them takes back more than
		// the button says it does.
		if len(g.Recipients) == 0 {
			rows = append(rows, withDependents(toRow(g, g.Reach, ""), g, ""))
			continue
		}
		for _, u := range g.Recipients {
			rows = append(rows, withDependents(toRow(g, g.Reach, u), g, u))
		}
	}
	writeJSON(w, rows)
}

func (T *ShareApp) serveToMe(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	rows := []row{}
	for _, g := range shareledger.ToMe(user) {
		r := toRow(g, g.Reach, "")
		// What THIS person still has to supply, asked of the kind that knows.
		// The generic line the provider wrote is about the share; this is
		// about them, and it is the one they can act on.
		if need := shareledger.Manifest(g.Kind, g.Owner, g.ID, user); len(need) > 0 {
			r.Detail = strings.Join(need, " ")
			r.Needs = true
		}
		// Whether there is anything to open, so a row with nothing behind it
		// offers no control rather than an expand onto an empty list.
		r.Carries = len(shareledger.Carries(g.Kind, g.Owner, g.ID, user)) > 0
		r.Record = g.ID
		rows = append(rows, r)
	}
	writeJSON(w, rows)
}

func toRow(g shareledger.Grant, reach, recipient string) row {
	who := g.Owner
	if recipient != "" {
		who = recipient
	} else if who == "" && g.Wide {
		who = "Everybody"
	}
	return row{
		ID: g.Kind + ":" + g.ID + ":" + recipient, Kind: g.Kind, Label: g.Label,
		Name: g.Name, Who: who, Reach: reach, Detail: g.Detail, Wide: g.Wide,
		Revocable: g.Revocable, Recipient: recipient,
	}
}

// takeBackPrompt is the default take-back question, for a row nobody relies on.
const takeBackPrompt = "Take this back? They lose it now, including anything they had scheduled against it."

// withDependents fills the row's Uses and its take-back question from the
// grant's dependents: this recipient's, or everybody's on a row that covers
// the whole deployment.
func withDependents(r row, g shareledger.Grant, recipient string) row {
	r.Confirm = takeBackPrompt
	var parts []string
	var names []string
	for _, d := range g.Dependents {
		if recipient != "" && d.User != recipient {
			continue
		}
		if len(d.Uses) == 0 {
			continue
		}
		names = append(names, d.Uses...)
		if recipient != "" {
			parts = append(parts, strings.Join(d.Uses, ", "))
		} else {
			parts = append(parts, d.User+": "+strings.Join(d.Uses, ", "))
		}
	}
	if len(parts) == 0 {
		return r
	}
	r.Uses = strings.Join(parts, "; ")
	who := "Their agents "
	if recipient == "" {
		who = "Agents "
	} else if len(names) == 1 {
		who = "Their agent "
	}
	verb := " use it and will run without it."
	if len(names) == 1 {
		verb = " uses it and will run without it."
	}
	r.Confirm = "Take this back? " + who + quoteList(names) + verb + " They are told, and can remove it or ask you to share it again."
	return r
}

func quoteList(names []string) string {
	q := make([]string, 0, len(names))
	for _, n := range names {
		q = append(q, "\""+n+"\"")
	}
	return strings.Join(q, ", ")
}

// serveRevoke takes one grant back, routed to the kind that owns the record.
// Only ever the caller's OWN: the owner is the session user, never a parameter,
// so no request can revoke on somebody else's behalf.
func (T *ShareApp) serveRevoke(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	recipient := strings.TrimSpace(r.URL.Query().Get("recipient"))
	if kind == "" || id == "" {
		http.Error(w, "kind and id required", http.StatusBadRequest)
		return
	}
	if err := shareledger.Revoke(kind, user, id, recipient); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	Log("[share] %q took back %s %q from %q", user, kind, id, chOr(recipient, "everybody"))
	w.WriteHeader(http.StatusNoContent)
}

func chOr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// servePage is the two views, in the order somebody asks them. What you gave
// out comes first because it is the one with consequences you can still change;
// what you were given is the reference half.
func (T *ShareApp) servePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	// One column set, both directions. Who means the recipient on the left
	// table and the person who gave it to you on the right, which is the only
	// difference between the two questions.
	cols := []ui.Col{
		{Field: "label", Label: "Kind", Flex: 0},
		{Field: "name", Flex: 1},
		{Field: "who", Label: "Who", Flex: 1},
		{Field: "wide", Label: "", Flex: 0, Type: "badge", Badges: []ui.BadgeMapping{
			{Value: true, Label: "Everybody", Color: "warning"},
		}},
		{Field: "reach", Label: "What it allows", Flex: 2, Mute: true},
		{Field: "detail", Label: "", Flex: 2, Mute: true},
	}
	// The owner's table adds who relies on each grant, before the question of
	// taking it back is asked.
	mineCols := append(append([]ui.Col{}, cols[:len(cols)-1]...),
		ui.Col{Field: "uses", Label: "Relied on by", Flex: 2, Mute: true},
		cols[len(cols)-1])
	page := ui.Page{
		Title:      "Sharing",
		ShowTitle:  true,
		BackURL:    "/",
		Nav:        HubNav("/share"),
		SectionNav: true,
		Sections: []ui.Section{
			shareStartSection(user),
			{
				Title:    "What you have shared",
				Wide:     true,
				Subtitle: "Everything of yours that reaches somebody else.",
				Detail: "One row per person, not per thing: a skill you gave three colleagues is three grants, and taking one back should not quietly take back the other two.\n\n" +
					"Sharing itself stays where the thing is — you share an agent from the agent page, a credential from its card. This is the audit, and the one place to take any of it back.\n\n" +
					"Relied on by names their own agents that use the thing, so you can see what a take-back stops before you do it; they are told when you do.\n\n" +
					"A row marked Everybody was widened by an administrator, or published by you from that thing's own page; it is taken back there rather than here, because un-publishing is a different act from dropping one person.",
				Body: ui.Table{
					Source:  "api/mine",
					RowKey:  "id",
					Columns: mineCols,
					RowActions: []ui.RowAction{
						{Type: "button", Label: "Take back",
							PostTo:       "api/revoke?kind={kind}&id={id}&recipient={recipient}",
							Method:       "POST",
							OnlyIf:       "revocable",
							Confirm:      takeBackPrompt,
							ConfirmField: "confirm",
							Variant:      "danger",
							Invalidate:   []string{"api/mine"}},
					},
					EmptyText: "You have not shared anything. Share a thing from its own page and it appears here.",
				},
			},
			{
				Title:    "Shared with you",
				Wide:     true,
				Subtitle: "What you have because somebody else gave it to you.",
				Detail: "Yours to use, not to edit: the record stays with the person who made it, and what they change is what you get.\n\n" +
					"A row marked Needs you does not work yet. Most of these run in YOUR namespace against your own tools and credentials, which is what keeps a share a way of working rather than a way into somebody's account — and it is also why something can arrive needing a credential of your own by the right name. The last column says which.",
				Body: ui.Table{
					Source: "api/to-me",
					RowKey: "id",
					RowActions: []ui.RowAction{
						// The recipient's own version of the owner's reach
						// panel. Somebody about to run an agent is about to run
						// its author's code against its author's documents;
						// that they cannot reach any of it outside the agent is
						// what makes that safe, not a reason to leave them
						// guessing about what happens inside it.
						ui.ExpandIf("What it carries", "carries", "", ui.Table{
							Source: "api/carries?kind={kind}&owner={who}&id={record}",
							RowKey: "line",
							Columns: []ui.Col{
								{Field: "line", Flex: 1},
							},
							EmptyText: "Nothing of theirs comes with it.",
						}),
					},
					Columns: append(append([]ui.Col{}, cols[:len(cols)-1]...),
						ui.Col{Field: "needs", Label: "", Flex: 0, Type: "badge", Badges: []ui.BadgeMapping{
							{Value: true, Label: "Needs you", Color: "warning"},
						}},
						cols[len(cols)-1]),
					EmptyText: "Nobody has shared anything with you.",
				},
			},
		},
	}
	page.ServeHTTP(w, r)
}

// serveCarries lists what comes with something shared with the caller.
//
// Gated on the caller being a RECIPIENT of that record, which the ledger
// answers by listing it: asking what somebody else's agent carries is not a
// question this answers for anybody who did not receive it.
func (T *ShareApp) serveCarries(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	held := false
	for _, g := range shareledger.ToMe(user) {
		if g.Kind == kind && g.Owner == owner && g.ID == id {
			held = true
			break
		}
	}
	if !held {
		http.NotFound(w, r)
		return
	}
	type line struct {
		Line string `json:"line"`
	}
	out := []line{}
	for _, l := range shareledger.Carries(kind, owner, id, user) {
		out = append(out, line{Line: l})
	}
	writeJSON(w, out)
}
