package share

// The guided route: pick a thing, pick who, answer what that decides, confirm.
//
// Two surfaces rather than one wizard, because the questions in the middle
// depend on the answers at the start — sharing an agent raises one per
// credential it touches, sharing a skill raises none — and a form whose fields
// are fixed when the page renders cannot ask them.
//
//	/share            → what and who
//	/share/plan?…     → the decisions that follow, then the report
//
// This file names no kind either. The first page lists whatever registered
// Candidates, the second renders whatever Plan returned, and the submit hands
// the answers back to the kind that asked. What a credential decision MEANS
// lives with credentials; what it looks like lives here.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/shareledger"
	"github.com/cmcoffee/gohort/core/ui"
)

// shareStartSection is the first step, on the main page: one thing, some
// people. Deliberately small — everything consequential is on the next screen,
// where it can be about the thing that was actually picked.
func shareStartSection(user string) ui.Section {
	var opts []ui.SelectOption
	for _, g := range shareledger.Options(user) {
		opts = append(opts, ui.SelectOption{
			Value: g.Kind + ":" + g.ID,
			Label: g.Label + " — " + g.Name,
		})
	}
	sort.Slice(opts, func(i, j int) bool { return opts[i].Label < opts[j].Label })
	people := userOptions(user)

	if len(opts) == 0 || len(people) == 0 {
		// Two dead dropdowns over an empty deployment is the same fault as a
		// button that cannot work. Say which half is missing.
		why := "You have nothing to share yet. Make an agent, a skill or a tool first."
		if len(opts) > 0 {
			why = "There is nobody else here to share with yet."
		}
		return ui.Section{Title: "Share something", Subtitle: why}
	}
	return ui.Section{
		Title:    "Share something",
		Subtitle: "Pick a thing and the people who should have it.",
		Detail: "The next screen asks about anything the thing depends on that they cannot reach — its tools, its documents, and the credentials underneath them. Nothing is shared until you confirm there.\n\n" +
			"You can also share from a thing's own page, which is the fast route when you know exactly what you want. Both end up in the same place.",
		Body: ui.FormPanel{
			PostURL:     "api/plan",
			SubmitLabel: "Continue",
			// The server hands back the URL of the decisions for THIS choice,
			// because what gets asked is not known until something is picked.
			RedirectURL:    "{url}",
			RedirectTarget: "_self",
			Fields: []ui.FormField{
				{Field: "what", Type: "select", Label: "What", Options: opts, Required: true},
				{Field: "who", Type: "checklist", Label: "Who should have it", Options: people, Required: true,
					Help: "They get it on your terms: yours to edit, theirs to use."},
			},
		},
	}
}

func userOptions(except string) []ui.SelectOption {
	var out []ui.SelectOption
	for _, u := range AuthListUsers(AuthDB()) {
		if u.Username != except {
			out = append(out, ui.SelectOption{Value: u.Username, Label: u.Username})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// servePlan builds the URL of the decisions screen. A POST rather than a link
// because the first form is the thing that knows the answers, and threading
// them through the page that renders it is what lets the second screen be
// about one specific share.
func (T *ShareApp) servePlan(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		What string   `json:"what"`
		Who  []string `json:"who"`
	}
	if err := decodeJSON(r, &body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	kind, id, found := strings.Cut(strings.TrimSpace(body.What), ":")
	if !found || len(body.Who) == 0 {
		http.Error(w, "pick something and at least one person", http.StatusBadRequest)
		return
	}
	_ = user
	writeJSON(w, map[string]string{
		"url": "plan?kind=" + urlArg(kind) + "&id=" + urlArg(id) + "&who=" + urlArg(strings.Join(body.Who, ",")),
	})
}

// servePlanPage is the decisions, then the confirm. One control per question
// the kind raised, with each answer's consequence written under it — the whole
// reason this screen exists is that those outcomes are not obvious from the
// option names.
func (T *ShareApp) servePlanPage(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	who := splitList(r.URL.Query().Get("who"))
	if kind == "" || id == "" || len(who) == 0 {
		http.Redirect(w, r, ".", http.StatusSeeOther)
		return
	}
	name := candidateName(user, kind, id)
	decisions := shareledger.Plan(kind, user, id, who)

	fields := []ui.FormField{{
		Type:  "header",
		Label: "Sharing " + name + " with " + andList(who),
		Help:  planSummary(len(decisions)),
	}}
	for _, d := range decisions {
		var opts []ui.SelectOption
		for _, c := range d.Options {
			opts = append(opts, ui.SelectOption{Value: c.Value, Label: c.Label, Help: c.Help})
		}
		// The default first, because a select with nothing chosen shows its
		// first option — so the ordering IS the default, and stating it
		// somewhere else would be a second place for the two to disagree.
		sort.SliceStable(opts, func(i, j int) bool { return opts[i].Value == d.Default })
		fields = append(fields, ui.FormField{
			Field: "a_" + safeKey(d.Key), Type: "select", Label: d.Title,
			Help: d.Intro, Options: opts,
		})
	}
	page := ui.Page{
		Title:     "Share " + name,
		ShowTitle: true,
		BackURL:   ".",
		Nav:       HubNav("/share"),
		Sections: []ui.Section{{
			Title:    "What this decides",
			Wide:     true,
			Subtitle: "Nothing has been shared yet. Nothing is, until you confirm.",
			Body: ui.FormPanel{
				PostURL:     "api/apply?kind=" + urlArg(kind) + "&id=" + urlArg(id) + "&who=" + urlArg(strings.Join(who, ",")),
				SubmitLabel: "Share it",
				Fields:      fields,
				OnSuccess:   "share_report",
			},
		}},
		ExtraHeadHTML: shareReportScript,
	}
	page.ServeHTTP(w, r)
}

// planSummary says how much is being asked and why, so a screen with no
// questions on it does not read as one that failed to load.
func planSummary(n int) string {
	switch n {
	case 0:
		return "Nothing it uses is out of their reach, so there is nothing to decide. Confirm to share it."
	case 1:
		return "One thing it uses does not reach them yet. Your answer decides what happens to it."
	default:
		return strconv.Itoa(n) + " things it uses do not reach them yet. Each answer below decides one of them."
	}
}

// serveApply performs the share and hands back the report.
func (T *ShareApp) serveApply(w http.ResponseWriter, r *http.Request) {
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
	who := splitList(r.URL.Query().Get("who"))
	if kind == "" || id == "" || len(who) == 0 {
		http.Error(w, "kind, id and who are required", http.StatusBadRequest)
		return
	}
	raw := map[string]any{}
	_ = decodeJSON(r, &raw)
	// The answers come back under the form's own prefixed field names; the
	// kind that asked gets them back under the keys it used.
	answers := map[string]string{}
	for k, v := range raw {
		if !strings.HasPrefix(k, "a_") {
			continue
		}
		if s, ok := v.(string); ok {
			answers[unsafeKey(strings.TrimPrefix(k, "a_"))] = s
		}
	}
	lines, err := shareledger.Share(kind, user, id, who, answers)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	Log("[share] %q shared %s %q with %s: %s", user, kind, id, strings.Join(who, ", "), strings.Join(lines, "; "))
	writeJSON(w, map[string]any{"lines": lines})
}

// candidateName resolves the display name, so every screen after the first
// says what is being shared rather than quoting an id back.
func candidateName(user, kind, id string) string {
	for _, g := range shareledger.Options(user) {
		if g.Kind == kind && g.ID == id {
			return g.Name
		}
	}
	return id
}

// safeKey / unsafeKey move a decision key through a form field name. Keys
// carry colons (kind:id) and field names cannot, so the round trip is explicit
// rather than a guess at what survives.
func safeKey(k string) string   { return strings.ReplaceAll(k, ":", "__") }
func unsafeKey(k string) string { return strings.ReplaceAll(k, "__", ":") }

func splitList(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func andList(users []string) string {
	switch len(users) {
	case 0:
		return "nobody"
	case 1:
		return users[0]
	default:
		return strings.Join(users[:len(users)-1], ", ") + " and " + users[len(users)-1]
	}
}

func decodeJSON(r *http.Request, v any) error { return json.NewDecoder(r.Body).Decode(v) }

func urlArg(s string) string { return url.QueryEscape(s) }

// shareReportScript renders what the share actually did, in place, rather than
// bouncing to a page that would have to be told. A report that lists what it
// could NOT do beside what it did is the point: one showing only successes is
// how somebody concludes their team has a working thing while a piece of it is
// still missing.
const shareReportScript = `<script>
window.uiRegisterClientAction('share_report', function(ctx) {
  var d = (ctx && ctx.response) || {};
  var lines = d.lines || [];
  var box = document.createElement('div');
  box.style.cssText = 'margin-top:1rem;padding:0.7rem 0.8rem;border:1px solid var(--border);border-radius:0.5rem;background:var(--bg-1)';
  var head = document.createElement('div');
  head.textContent = lines.length ? 'Shared' : 'Nothing to do';
  head.style.cssText = 'font-weight:600;margin-bottom:0.4rem';
  box.appendChild(head);
  lines.forEach(function(l) {
    var row = document.createElement('div');
    row.textContent = l;
    row.style.cssText = 'font-size:0.85rem;color:var(--text-mute);padding:0.15rem 0';
    box.appendChild(row);
  });
  var back = document.createElement('a');
  back.href = '.';
  back.textContent = 'Back to sharing';
  back.style.cssText = 'display:inline-block;margin-top:0.6rem;font-size:0.85rem;color:var(--accent)';
  box.appendChild(back);
  var host = ctx && ctx.panel ? ctx.panel : document.body;
  host.appendChild(box);
});
</script>`
