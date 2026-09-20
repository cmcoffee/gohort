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
			rows = append(rows, toRow(g, g.Reach, ""))
			continue
		}
		for _, u := range g.Recipients {
			rows = append(rows, toRow(g, g.Reach, u))
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
					"A row marked Everybody was widened by an administrator, or published by you from that thing's own page; it is taken back there rather than here, because un-publishing is a different act from dropping one person.",
				Body: ui.Table{
					Source:  "api/mine",
					RowKey:  "id",
					Columns: cols,
					RowActions: []ui.RowAction{
						{Type: "button", Label: "Take back",
							PostTo:     "api/revoke?kind={kind}&id={id}&recipient={recipient}",
							Method:     "POST",
							OnlyIf:     "revocable",
							Confirm:    "Take this back? They lose it now, including anything they had scheduled against it.",
							Variant:    "danger",
							Invalidate: []string{"api/mine"}},
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
