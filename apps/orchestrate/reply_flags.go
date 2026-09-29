package orchestrate

// Reply guards and flagged replies: the admin's view of the agent loop's
// built-in reply guards (core/replyguard), and the queue of replies people
// marked good or bad.
//
// The guards were code and nothing else: well tuned for the models this
// deployment runs most, with no way to see what they catch on another model
// or to turn one down where it misfires. The Reply guards section shows each
// guard, what it caught per model, and sets it on, in shadow (counted, reply
// left alone) or off, for all models or one.
//
// A thumbs-down on a reply files it for the admin with the person's own
// account of what was wrong, which is required: a bare "bad" says nothing a
// guard could be built from. A thumbs-up keeps a reply as a known-good
// example, which is what a future guard is tested against so it does not
// catch replies that were fine.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/replyguard"
	"github.com/cmcoffee/gohort/core/sections"
	"github.com/cmcoffee/gohort/core/ui"
)

const replyFlagTable = "reply_flags"

// Flag verdicts and review states.
const (
	flagDown = "down"
	flagUp   = "up"

	flagNew       = "new"       // waiting for the admin
	flagKept      = "kept"      // kept as a case for a future guard
	flagDismissed = "dismissed" // looked at, nothing to do
	flagGood      = "good"      // a thumbs-up: a known-good example, not reviewed
)

// Bounds on what a flag keeps: enough to judge the reply, not a transcript.
const (
	flagReplyMax = 8000
	flagAskedMax = 3000
	flagEvalMax  = 3000
)

// replyFlag is one reply somebody marked.
type replyFlag struct {
	ID         string    `json:"id"`
	At         time.Time `json:"at"`
	User       string    `json:"user"`
	AgentID    string    `json:"agent_id"`
	Agent      string    `json:"agent"`
	SessionID  string    `json:"session_id"`
	Model      string    `json:"model"`
	Verdict    string    `json:"verdict"`
	Evaluation string    `json:"evaluation,omitempty"`
	Asked      string    `json:"asked,omitempty"`
	Reply      string    `json:"reply"`
	Status     string    `json:"status"`
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// handleReplyFlag files a thumbs-up or thumbs-down on one of the caller's own
// replies. POST {session_id, agent_id, reply, verdict, evaluation}. The reply
// is matched in the stored session, so what the admin reads is what the agent
// said, with the message before it and the model that wrote it.
func (T *OrchestrateApp) handleReplyFlag(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		SessionID  string `json:"session_id"`
		AgentID    string `json:"agent_id"`
		Reply      string `json:"reply"`
		Verdict    string `json:"verdict"`
		Evaluation string `json:"evaluation"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	req.Evaluation = strings.TrimSpace(req.Evaluation)
	switch {
	case req.Verdict != flagDown && req.Verdict != flagUp:
		http.Error(w, "verdict must be up or down", http.StatusBadRequest)
		return
	case req.Verdict == flagDown && req.Evaluation == "":
		http.Error(w, "Say what was wrong with the reply: that is what a fix is built from.", http.StatusBadRequest)
		return
	case strings.TrimSpace(req.Reply) == "":
		http.Error(w, "no reply to flag", http.StatusBadRequest)
		return
	}
	udb := UserDB(T.DB, user)
	sess, found := loadChatSession(udb, req.AgentID, req.SessionID)
	if found && sess.Incognito {
		http.Error(w, "This is an incognito session: nothing from it is kept, so it cannot be flagged.", http.StatusBadRequest)
		return
	}
	f := replyFlag{
		ID: UUIDv4(), At: time.Now(), User: user, AgentID: req.AgentID, SessionID: req.SessionID,
		Verdict: req.Verdict, Evaluation: clip(req.Evaluation, flagEvalMax), Reply: clip(req.Reply, flagReplyMax),
		Status: flagNew,
	}
	if f.Verdict == flagUp {
		f.Status = flagGood
	}
	if a, ok := loadAgent(udb, req.AgentID); ok {
		f.Agent = chFirst(a.Name, a.ID)
	}
	if found {
		if m, asked, ok := findFlaggedReply(sess, req.Reply); ok {
			f.Reply = clip(m.Content, flagReplyMax)
			f.Asked = clip(asked, flagAskedMax)
			if m.Usage != nil {
				f.Model = m.Usage.Model
			}
		}
	}
	f.Model = replyguard.NormalizeModel(f.Model)
	T.DB.Set(replyFlagTable, f.ID, f)
	Log("[orchestrate.flag] user=%q agent=%q model=%s marked a reply %s", user, f.Agent, f.Model, f.Verdict)
	msg := "Kept as a good example."
	if f.Verdict == flagDown {
		msg = "Sent to the admin for review."
	}
	writeJSON(w, map[string]any{"ok": true, "message": msg})
}

// findFlaggedReply finds the assistant message a flag is about, newest first:
// the one whose text matches, else the one that starts the same way (a bubble
// can differ from the stored text by trailing whitespace or a late edit). It
// returns the message the person sent before it too.
func findFlaggedReply(sess ChatSession, reply string) (ChatMessage, string, bool) {
	want := strings.TrimSpace(reply)
	prefix := want
	if r := []rune(prefix); len(r) > 200 {
		prefix = string(r[:200])
	}
	match := -1
	for i := len(sess.Messages) - 1; i >= 0 && match < 0; i-- {
		if m := sess.Messages[i]; m.Role == "assistant" && strings.TrimSpace(m.Content) == want {
			match = i
		}
	}
	for i := len(sess.Messages) - 1; i >= 0 && match < 0; i-- {
		if m := sess.Messages[i]; m.Role == "assistant" && strings.HasPrefix(strings.TrimSpace(m.Content), prefix) {
			match = i
		}
	}
	if match < 0 {
		return ChatMessage{}, "", false
	}
	asked := ""
	for i := match - 1; i >= 0; i-- {
		if sess.Messages[i].Role == "user" {
			asked = sess.Messages[i].Content
			break
		}
	}
	return sess.Messages[match], asked, true
}

// requireAdmin answers a non-admin with 403 and reports whether to go on.
func (T *OrchestrateApp) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return false
	}
	if !RequestIsAdmin(r) {
		http.Error(w, "admin only", http.StatusForbidden)
		return false
	}
	return true
}

// handleReplyFlags lists the flags for the admin: the thumbs-downs to review
// and the thumbs-ups kept as good examples. GET.
func (T *OrchestrateApp) handleReplyFlags(w http.ResponseWriter, r *http.Request) {
	if !T.requireAdmin(w, r) {
		return
	}
	type row struct {
		replyFlag
		Label string `json:"label"`
	}
	review, good := []row{}, []row{}
	for _, k := range T.DB.Keys(replyFlagTable) {
		var f replyFlag
		if !T.DB.Get(replyFlagTable, k, &f) {
			continue
		}
		rw := row{replyFlag: f, Label: clip(chFirst(f.Evaluation, f.Reply), 160)}
		if f.Verdict == flagUp {
			good = append(good, rw)
		} else {
			review = append(review, rw)
		}
	}
	// Waiting ones first, then newest first.
	sort.Slice(review, func(i, j int) bool {
		if (review[i].Status == flagNew) != (review[j].Status == flagNew) {
			return review[i].Status == flagNew
		}
		return review[i].At.After(review[j].At)
	})
	sort.Slice(good, func(i, j int) bool { return good[i].At.After(good[j].At) })
	writeJSON(w, map[string]any{"review": review, "good": good})
}

// handleReplyFlagStatus sets a flag's review state, or deletes it. POST
// ?id=&status=kept|dismissed|new|delete.
func (T *OrchestrateApp) handleReplyFlagStatus(w http.ResponseWriter, r *http.Request) {
	if !T.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	var f replyFlag
	if id == "" || !T.DB.Get(replyFlagTable, id, &f) {
		http.Error(w, "no such flag", http.StatusNotFound)
		return
	}
	switch status {
	case "delete":
		T.DB.Unset(replyFlagTable, id)
	case flagKept, flagDismissed, flagNew:
		f.Status = status
		T.DB.Set(replyFlagTable, id, f)
	default:
		http.Error(w, "status must be kept, dismissed, new or delete", http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// replyGuardRow is one line of the Reply guards table: a guard's default
// (Model "*") or its setting and firings on one model.
type replyGuardRow struct {
	Key      string `json:"_id"`
	Guard    string `json:"guard"`
	Group    string `json:"group"`
	Desc     string `json:"desc,omitempty"`
	Model    string `json:"model"`
	ModelKey string `json:"model_key"`
	Mode     string `json:"_mode"`
	Override bool   `json:"_override,omitempty"`
	Acted    int    `json:"acted"`
	Shadowed int    `json:"shadowed"`
	Last     string `json:"last,omitempty"`
	Samples  string `json:"samples,omitempty"`
}

// handleReplyGuards lists every guard with its default and one row per model
// it has fired on or has a setting for. GET.
func (T *OrchestrateApp) handleReplyGuards(w http.ResponseWriter, r *http.Request) {
	if !T.requireAdmin(w, r) {
		return
	}
	stats := map[string]replyguard.Stat{}
	for _, st := range replyguard.Stats() {
		stats[st.ID+"|"+st.Model] = st
	}
	set := map[string]replyguard.Mode{}
	for _, s := range replyguard.Settings() {
		set[s.ID+"|"+s.Model] = s.Mode
	}
	var rows []replyGuardRow
	for _, g := range replyguard.Guards() {
		group := g.Name
		if g.Judge {
			group += " (judge: a model call per check)"
		}
		def := replyGuardRow{Key: g.ID + "|" + replyguard.AllModels, Guard: g.ID, Group: group, Desc: g.Desc,
			Model: "All models", ModelKey: replyguard.AllModels, Mode: string(replyguard.DefaultMode(g.ID))}
		rows = append(rows, def)
		models := map[string]bool{}
		for k := range stats {
			if id, m, _ := strings.Cut(k, "|"); id == g.ID {
				models[m] = true
			}
		}
		for k := range set {
			if id, m, _ := strings.Cut(k, "|"); id == g.ID && m != replyguard.AllModels {
				models[m] = true
			}
		}
		var names []string
		for m := range models {
			names = append(names, m)
		}
		sort.Strings(names)
		for _, m := range names {
			st := stats[g.ID+"|"+m]
			_, override := set[g.ID+"|"+m]
			row := replyGuardRow{Key: g.ID + "|" + m, Guard: g.ID, Group: group, Model: m, ModelKey: m,
				Mode: string(replyguard.ModeFor(g.ID, m)), Override: override, Acted: st.Acted, Shadowed: st.Shadowed}
			if !st.Last.IsZero() {
				row.Last = st.Last.Format(time.RFC3339)
			}
			var b strings.Builder
			for _, s := range st.Samples {
				tag := "corrected"
				if s.Shadow {
					tag = "shadow: left as it was"
				}
				fmt.Fprintf(&b, "[%s, %s]\n%s\n\n", s.At.Local().Format("Jan 2 15:04"), tag, s.Text)
			}
			row.Samples = strings.TrimSpace(b.String())
			rows = append(rows, row)
		}
	}
	writeJSON(w, map[string]any{"records": rows})
}

// handleReplyGuardMode sets one guard's mode on one model or all of them.
// POST ?id=<guard>|<model> with {"_mode": on|shadow|off|""}, "" to follow the
// default again; or, from the form, {guard, model, mode}.
func (T *OrchestrateApp) handleReplyGuardMode(w http.ResponseWriter, r *http.Request) {
	if !T.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var body map[string]string
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body)
	guard, model, _ := strings.Cut(strings.TrimSpace(r.URL.Query().Get("id")), "|")
	mode, hasMode := body["_mode"]
	if guard == "" {
		guard, model, mode, hasMode = body["guard"], body["model"], body["mode"], true
	}
	if r.URL.Query().Get("clear") == "1" {
		mode, hasMode = "", true
	}
	if !hasMode {
		http.Error(w, "no mode given", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(model) == "" {
		model = replyguard.AllModels
	}
	if err := replyguard.SetMode(strings.TrimSpace(guard), strings.TrimSpace(model), replyguard.Mode(strings.TrimSpace(mode))); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	Log("[orchestrate.replyguard] %s on %s set to %q", guard, model, mode)
	writeJSON(w, map[string]any{"ok": true})
}

// replyGuardModeOptions is the three-way control on each row.
func replyGuardModeOptions() []ui.SelectOption {
	return []ui.SelectOption{
		{Value: string(replyguard.On), Label: "On"},
		{Value: string(replyguard.Shadow), Label: "Shadow"},
		{Value: string(replyguard.Off), Label: "Off"},
	}
}

func init() {
	for _, s := range replyGuardSections() {
		sections.RegisterAdminSection(sections.AdminSectionEntry{App: "/orchestrate", Section: s})
	}
}

func replyGuardSections() []ui.Section {
	const guards = "/orchestrate/api/console/reply-guards"
	const flags = "/orchestrate/api/console/reply-flags"
	var guardOpts []ui.SelectOption
	for _, g := range replyguard.Guards() {
		guardOpts = append(guardOpts, ui.SelectOption{Value: g.ID, Label: g.Name})
	}
	flagDetail := ui.RecordView{Pairs: []ui.DisplayPair{
		{Label: "What was wrong", Field: "evaluation", Block: true},
		{Label: "They asked", Field: "asked", Block: true},
		{Label: "The reply", Field: "reply", Block: true},
		{Label: "Agent", Field: "agent"},
		{Label: "Model", Field: "model", Mono: true},
		{Label: "User", Field: "user", Mono: true},
		{Label: "Session", Field: "session_id", Mono: true},
	}}
	status := func(to, label, variant string, onlyIf string) ui.RowAction {
		return ui.RowAction{Type: "button", Label: label, Variant: variant, Compact: true, OnlyIf: onlyIf,
			PostTo: flags + "/status?id={id}&status=" + to}
	}
	return []ui.Section{
		{
			Group:    "Agents",
			Title:    "Reply guards",
			Subtitle: "The checks that catch a model's reply going wrong in a known way and send it back. What each caught, per model, and whether it runs.",
			Detail: "On corrects the reply. Shadow counts what the guard would have done and leaves the reply alone, which is how to try a guard on a model before trusting it, or to see whether one misfires. Off does not run it; a judge guard then makes no model call.\n\n" +
				"The All models row is each guard's default. A model row appears once the guard has fired on that model, or when you set one below; its setting wins over the default, and Follow default clears it.\n\n" +
				"Expand a model row for the last replies the guard caught there.",
			Wide: true,
			Body: ui.Stack{Children: []ui.Component{
				ui.Table{Source: guards, RowKey: "_id", GroupBy: "group",
					Columns: []ui.Col{
						{Field: "model", Label: "Model", Flex: 2},
						{Field: "acted", Label: "Corrected", Format: "thousands"},
						{Field: "shadowed", Label: "Shadow", Format: "thousands"},
						{Field: "last", Label: "Last", Format: "reltime", Mute: true},
						{Field: "desc", Label: "", Flex: 4, Mute: true, Line: 2},
					},
					RowActions: []ui.RowAction{
						{Type: "segmented", Field: "_mode", PostTo: guards + "/mode?id={_id}", Options: replyGuardModeOptions()},
						{Type: "button", Label: "Follow default", Compact: true, OnlyIf: "_override", PostTo: guards + "/mode?id={_id}&clear=1"},
						ui.Expand("Caught", ui.RecordView{Pairs: []ui.DisplayPair{{Label: "Recent replies", Field: "samples", Block: true}}}),
					},
					EmptyText: "No reply guards are registered."},
				ui.FormPanel{PostURL: guards + "/mode", SubmitLabel: "Set",
					Invalidate: []string{guards},
					Fields: []ui.FormField{
						{Field: "guard", Label: "Guard", Type: "select", Options: guardOpts, Required: true},
						{Field: "model", Label: "Model", Type: "text", Placeholder: "gemini-2.5-flash", Required: true,
							Help: "As the provider names it. Set a guard on a model before it has fired there."},
						{Field: "mode", Label: "Mode", Type: "select", Options: replyGuardModeOptions()},
					}},
			}},
		},
		{
			Group:    "Agents",
			Title:    "Flagged replies",
			Subtitle: "Replies people marked with a thumbs-down, with what they said was wrong. The cases a new reply guard would be built from.",
			Detail: "Keep one that shows a pattern worth a guard; dismiss one that does not. A kept case is what a future guard is tested against: it must catch it.\n\n" +
				"Good examples are replies marked with a thumbs-up. A future guard must NOT catch them, so they are kept as they are, with no review.",
			Wide: true,
			Body: ui.Stack{Children: []ui.Component{
				ui.Table{Source: flags, RecordsField: "review", RowKey: "id",
					Columns: []ui.Col{
						{Field: "at", Label: "When", Format: "reltime", Mute: true},
						{Field: "status", Label: "Status", Type: "badge", Badges: []ui.BadgeMapping{
							{Value: flagNew, Label: "to review", Color: "warning"},
							{Value: flagKept, Label: "kept", Color: "success"},
							{Value: flagDismissed, Label: "dismissed", Color: "mute"},
						}},
						{Field: "agent", Label: "Agent"},
						{Field: "model", Label: "Model", Mute: true},
						{Field: "label", Label: "What was wrong", Flex: 4, Line: 2},
					},
					RowActions: []ui.RowAction{
						ui.Expand("Detail", flagDetail),
						status(flagKept, "Keep", "success", ""),
						status(flagDismissed, "Dismiss", "", ""),
						{Type: "button", Label: "Delete", Variant: "danger", Compact: true, PostTo: flags + "/status?id={id}&status=delete",
							Confirm: "Delete this flag? The reply and what was said about it are gone."},
					},
					Search: true, SearchPlaceholder: "Filter by agent, model or what was wrong",
					EmptyText: "Nothing flagged yet. A thumbs-down on any reply lands here."},
				ui.Table{Source: flags, RecordsField: "good", RowKey: "id",
					Columns: []ui.Col{
						{Field: "at", Label: "When", Format: "reltime", Mute: true},
						{Field: "agent", Label: "Agent"},
						{Field: "model", Label: "Model", Mute: true},
						{Field: "label", Label: "Good example", Flex: 4, Line: 2},
					},
					RowActions: []ui.RowAction{
						ui.Expand("Detail", flagDetail),
						{Type: "button", Label: "Delete", Variant: "danger", Compact: true, PostTo: flags + "/status?id={id}&status=delete"},
					},
					EmptyText: "No good examples yet. A thumbs-up on a reply keeps it here."},
			}},
		},
	}
}
