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
	"html"
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

const (
	replyFlagTable = "reply_flags"
	// replyFlagRefTable indexes a person's flags by the thread they are in:
	// "<user>|<agent>|<session>" -> []flagRef, so a session load can show
	// which replies they marked without scanning every flag.
	replyFlagRefTable = "reply_flag_refs"
)

// flagRef ties a flag to one reply in a thread: its position, and the start
// of its text, since a retry or an edit can move what sits at a position.
type flagRef struct {
	ID      string `json:"id"`
	Index   int    `json:"index"`
	Verdict string `json:"verdict"`
	Prefix  string `json:"prefix"`
}

func flagRefKey(user, agentID, sessionID string) string {
	return user + "|" + agentID + "|" + sessionID
}

func flagPrefix(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 120 {
		return string(r[:120])
	}
	return s
}

// markFlaggedReplies marks the replies in a served thread that this person
// flagged (see ChatMessage.Flag). A reference whose position no longer holds
// the reply it was made on is skipped rather than shown on the wrong one.
func markFlaggedReplies(db Database, user, agentID, sessionID string, msgs []ChatMessage) {
	if db == nil || sessionID == "" {
		return
	}
	var refs []flagRef
	if !db.Get(replyFlagRefTable, flagRefKey(user, agentID, sessionID), &refs) {
		return
	}
	for _, ref := range refs {
		if ref.Index < 0 || ref.Index >= len(msgs) || msgs[ref.Index].Role != "assistant" ||
			!strings.HasPrefix(strings.TrimSpace(msgs[ref.Index].Content), ref.Prefix) {
			continue
		}
		msgs[ref.Index].Flag, msgs[ref.Index].FlagID = ref.Verdict, ref.ID
	}
}

// refFlag files a flag against the reply at index in its thread. One mark per
// reply: a thumbs-down after a thumbs-up replaces it, and the replaced flag is
// gone from the admin's queue too.
func refFlag(db Database, f replyFlag, index int) {
	key := flagRefKey(f.User, f.AgentID, f.SessionID)
	var refs []flagRef
	db.Get(replyFlagRefTable, key, &refs)
	kept := []flagRef{}
	for _, ref := range refs {
		if ref.Index == index {
			db.Unset(replyFlagTable, ref.ID)
			continue
		}
		kept = append(kept, ref)
	}
	kept = append(kept, flagRef{ID: f.ID, Index: index, Verdict: f.Verdict, Prefix: flagPrefix(f.Reply)})
	db.Set(replyFlagRefTable, key, kept)
}

// unflag removes one of a person's flags, and its thread reference.
func unflag(db Database, f replyFlag) {
	db.Unset(replyFlagTable, f.ID)
	key := flagRefKey(f.User, f.AgentID, f.SessionID)
	var refs []flagRef
	if !db.Get(replyFlagRefTable, key, &refs) {
		return
	}
	kept := []flagRef{}
	for _, r := range refs {
		if r.ID != f.ID {
			kept = append(kept, r)
		}
	}
	if len(kept) == 0 {
		db.Unset(replyFlagRefTable, key)
		return
	}
	db.Set(replyFlagRefTable, key, kept)
}

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
	// The turn around the reply, for testing a drafted guard against it:
	// what the assistant showed earlier in the same turn, and how many tool
	// calls the turn made. TurnKept is false on flags from before these were
	// recorded, and on replies that could not be found in their thread.
	Earlier   string `json:"earlier,omitempty"`
	ToolCalls int    `json:"tool_calls,omitempty"`
	TurnKept  bool   `json:"turn_kept,omitempty"`
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
	if r.Method == http.MethodDelete {
		// Pressed again: take it back. Only the person's own.
		var f replyFlag
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" || !T.DB.Get(replyFlagTable, id, &f) || f.User != user {
			http.Error(w, "no such flag", http.StatusNotFound)
			return
		}
		unflag(T.DB, f)
		writeJSON(w, map[string]any{"ok": true, "message": "Taken back."})
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
	index := -1
	if found {
		if i, asked, ok := findFlaggedReply(sess, req.Reply); ok {
			m := sess.Messages[i]
			index = i
			f.Reply = clip(m.Content, flagReplyMax)
			f.Asked = clip(asked, flagAskedMax)
			if m.Usage != nil {
				f.Model = m.Usage.Model
			}
			f.Earlier, f.ToolCalls = turnAround(sess.Messages, i)
			f.Earlier, f.TurnKept = clip(f.Earlier, flagReplyMax), true
		}
	}
	f.Model = replyguard.NormalizeModel(f.Model)
	T.DB.Set(replyFlagTable, f.ID, f)
	if index >= 0 {
		refFlag(T.DB, f, index)
	}
	Log("[orchestrate.flag] user=%q agent=%q model=%s marked a reply %s", user, f.Agent, f.Model, f.Verdict)
	msg := "Kept as a good example."
	if f.Verdict == flagDown {
		msg = "Sent to the admin for review."
	}
	writeJSON(w, map[string]any{"ok": true, "message": msg, "id": f.ID, "verdict": f.Verdict})
}

// findFlaggedReply finds the assistant message a flag is about, newest first:
// the one whose text matches, else the one that starts the same way (a bubble
// can differ from the stored text by trailing whitespace or a late edit). It
// returns the message the person sent before it too.
func findFlaggedReply(sess ChatSession, reply string) (int, string, bool) {
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
		return -1, "", false
	}
	asked := ""
	for i := match - 1; i >= 0; i-- {
		if sess.Messages[i].Role == "user" {
			asked = sess.Messages[i].Content
			break
		}
	}
	return match, asked, true
}

// turnAround is what the assistant said earlier in the turn that ends at
// index i (the rounds after the person's message), and how many tool calls
// the turn made.
func turnAround(msgs []ChatMessage, i int) (earlier string, toolCalls int) {
	toolCalls = len(msgs[i].ToolCalls)
	var parts []string
	for j := i - 1; j >= 0 && msgs[j].Role != "user"; j-- {
		if msgs[j].Role == "assistant" {
			toolCalls += len(msgs[j].ToolCalls)
			if strings.TrimSpace(msgs[j].Content) != "" {
				parts = append([]string{msgs[j].Content}, parts...)
			}
		}
	}
	return strings.Join(parts, "\n\n"), toolCalls
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
		unflag(T.DB, f)
	case flagKept, flagDismissed, flagNew:
		f.Status = status
		T.DB.Set(replyFlagTable, id, f)
	default:
		http.Error(w, "status must be kept, dismissed, new or delete", http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// replyGuardRow is one line of the guard table: a guard for all tiers, or on
// the lead or the worker tier.
type replyGuardRow struct {
	Key        string `json:"_id"`
	Guard      string `json:"guard"`
	Group      string `json:"group"`
	Desc       string `json:"desc,omitempty"`
	Scope      string `json:"scope"`
	ScopeLabel string `json:"scope_label"`
	Mode       string `json:"_mode"`
	Follows    string `json:"follows,omitempty"` // what a tier row takes from All tiers
	Retries    int    `json:"retries"`
	Note       string `json:"note_value,omitempty"`
	NoteOwn    bool   `json:"note_own,omitempty"`
	Editable   bool   `json:"_note_editable,omitempty"`
	RetriesOwn bool   `json:"retries_own,omitempty"`
	Checks     string `json:"checks_json,omitempty"` // a drafted guard's checks for this scope, as JSON
	ChecksOwn  bool   `json:"checks_own,omitempty"`
	Fires      string `json:"fires,omitempty"`
	Authored   bool   `json:"_authored,omitempty"`
	Override   bool   `json:"_override,omitempty"`   // this tier has settings of its own
	CanRevert  bool   `json:"_can_revert,omitempty"` // a customized built-in, on its All tiers row
	Warning    string `json:"warning,omitempty"`
	Acted      int    `json:"acted"`
	Shadowed   int    `json:"shadowed"`
	// Of the recent corrections whose turn has ended: how many got the work
	// done (a tool ran after), and how many look like misfires (the same
	// reply came back, or one answering the correction).
	Fixed    int    `json:"fixed"`
	Misfired int    `json:"misfired"`
	Last     string `json:"last,omitempty"`
	Samples  string `json:"samples,omitempty"`
}

// guardMisfireWarning is the row's warning when most of a guard's recent
// corrections whose turn has ended look like misfires, at least two of them.
func guardMisfireWarning(misfired, judged int) string {
	if misfired < 2 || misfired*2 < judged {
		return ""
	}
	return fmt.Sprintf("%d of its last %d corrections came back as the same reply, or as a reply to the correction itself: it may be firing on good replies. Read the recent replies in Detail, and consider Shadow.", misfired, judged)
}

// guardOutcomeLabel is how the page reads an outcome.
func guardOutcomeLabel(out string) string {
	switch out {
	case replyguard.OutcomeFixed:
		return "a tool ran after it"
	case replyguard.OutcomeUnchanged:
		return "the same reply came back (likely a misfire)"
	case replyguard.OutcomeAnswered:
		return "the reply answered the correction (likely a misfire)"
	case replyguard.OutcomeRewritten:
		return "rewritten, no tool"
	}
	return ""
}

// handleReplyGuards lists every guard on three rows: all tiers, the lead and
// the worker, each with the model the tier runs and what the guard caught
// there. GET.
func (T *OrchestrateApp) handleReplyGuards(w http.ResponseWriter, r *http.Request) {
	if !T.requireAdmin(w, r) {
		return
	}
	stats := replyguard.Stats()
	var rows []replyGuardRow
	for _, g := range replyguard.Guards() {
		group := g.Name
		if g.Judge {
			group += " (judge: a model call per check)"
		}
		if g.Authored {
			group += " (drafted)"
		}
		customized := replyguard.Customized(g.ID)
		if customized {
			group += " · customized"
		}
		var authoredChecks []replyguard.Check
		defNote := g.Note
		if g.Authored {
			if a, ok := replyguard.LoadAuthored(g.ID); ok {
				authoredChecks, defNote = a.Checks, a.Correction
			}
		}
		for _, scope := range replyguard.Scopes() {
			eff := replyguard.Resolve(g.ID, scope)
			own, hasOwn := replyguard.SettingFor(g.ID, scope)
			row := replyGuardRow{Key: g.ID + "|" + scope, Guard: g.ID, Group: group, Scope: scope,
				Mode: string(eff.Mode), Retries: eff.Retries, Editable: g.Editable(), Authored: g.Authored,
				Note: chFirst(eff.Note, defNote), NoteOwn: hasOwn && own.Note != "", RetriesOwn: hasOwn && own.Retries > 0,
				ChecksOwn: hasOwn && len(own.Checks) > 0}
			checks := authoredChecks
			if len(eff.Checks) > 0 {
				checks = eff.Checks
			}
			if g.Authored && len(checks) > 0 {
				cj, _ := json.MarshalIndent(checks, "", "  ")
				row.Checks, row.Fires = string(cj), replyguard.DescribeAll(checks)
			}
			model := ""
			if scope == replyguard.AllTiers {
				row.ScopeLabel, row.Desc = "All tiers", g.Desc
				row.CanRevert = customized && !g.Authored
			} else {
				model = replyguard.CurrentModel(scope)
				row.ScopeLabel = strings.ToUpper(scope[:1]) + scope[1:]
				if model != "" {
					row.ScopeLabel += ": " + model
				}
				row.Override = hasOwn
				if !hasOwn || own.Mode == "" {
					row.Follows = "mode follows All tiers"
				}
				if hasOwn && own.SetOn != "" && model != "" && own.SetOn != model {
					row.Warning = fmt.Sprintf("Set on %s; the %s is now %s. Review it, or Remove to follow All tiers.", own.SetOn, scope, model)
				}
			}
			var b strings.Builder
			var last time.Time
			judged := 0
			for _, st := range stats {
				if st.ID != g.ID || (scope != replyguard.AllTiers && st.Tier != scope) {
					continue
				}
				row.Acted += st.Acted
				row.Shadowed += st.Shadowed
				if st.Last.After(last) {
					last = st.Last
				}
				for _, smp := range st.Samples {
					tag := "corrected"
					if smp.Shadow {
						tag = "shadow: left as it was"
					}
					if l := guardOutcomeLabel(smp.Outcome); l != "" {
						tag += ": " + l
						judged++
						switch {
						case smp.Outcome == replyguard.OutcomeFixed:
							row.Fixed++
						case replyguard.Misfire(smp.Outcome):
							row.Misfired++
						}
					}
					fmt.Fprintf(&b, "[%s, %s, %s]\n%s\n", smp.At.Local().Format("Jan 2 15:04"), st.Model, tag, smp.Text)
					if smp.After != "" && smp.Outcome != replyguard.OutcomeUnchanged {
						fmt.Fprintf(&b, "  then sent: %s\n", smp.After)
					}
					b.WriteString("\n")
				}
			}
			if !last.IsZero() {
				row.Last = last.Format(time.RFC3339)
			}
			// Most of its recent corrections ended where a misfire ends: say so
			// on the row, where it is seen, rather than leave it to someone
			// reading the conversations the guard rewrote.
			if w := guardMisfireWarning(row.Misfired, judged); w != "" {
				row.Warning = strings.TrimSpace(row.Warning + " " + w)
			}
			row.Samples = strings.TrimSpace(b.String())
			rows = append(rows, row)
		}
	}
	writeJSON(w, map[string]any{"records": rows})
}

// guardScope reads "<guard>|<scope>" from ?id=.
func guardScope(r *http.Request) (string, string) {
	id, scope, _ := strings.Cut(strings.TrimSpace(r.URL.Query().Get("id")), "|")
	if scope == "" {
		scope = replyguard.AllTiers
	}
	return id, scope
}

// handleReplyGuardMode sets a guard's mode on one scope. POST
// ?id=<guard>|<scope> with {"_mode": on|shadow|off}.
func (T *OrchestrateApp) handleReplyGuardMode(w http.ResponseWriter, r *http.Request) {
	if !T.requireAdmin(w, r) {
		return
	}
	var body map[string]string
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body)
	id, scope := guardScope(r)
	if err := replyguard.Put(replyguard.Setting{ID: id, Scope: scope, Mode: replyguard.Mode(strings.TrimSpace(body["_mode"]))}); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	Log("[orchestrate.replyguard] %s on %s set to %q", id, scope, body["_mode"])
	writeJSON(w, map[string]any{"ok": true})
}

// handleReplyGuardSetting edits a scope's note, retries and, for a drafted
// guard, its checks. POST ?id=<guard>|<scope> with {note, retries, checks}.
// An empty note, zero retries or empty checks put that part back to what the
// scope above says.
func (T *OrchestrateApp) handleReplyGuardSetting(w http.ResponseWriter, r *http.Request) {
	if !T.requireAdmin(w, r) {
		return
	}
	var body struct {
		Note    string `json:"note"`
		Retries int    `json:"retries"`
		Checks  string `json:"checks"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	id, scope := guardScope(r)
	g, ok := replyguard.Lookup(id)
	if !ok {
		http.Error(w, "no such guard", http.StatusNotFound)
		return
	}
	set := replyguard.Setting{ID: id, Scope: scope, Retries: body.Retries}
	// A note that is the guard's own, word for word, is not a change.
	def := g.Note
	if a, ok := replyguard.LoadAuthored(id); ok {
		def = a.Correction
	}
	if n := strings.TrimSpace(body.Note); n != "" && n != strings.TrimSpace(def) {
		set.Note = n
	}
	if c := strings.TrimSpace(body.Checks); c != "" {
		if err := json.Unmarshal([]byte(c), &set.Checks); err != nil {
			http.Error(w, "the checks are not valid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if a, ok := replyguard.LoadAuthored(id); ok {
			if cur, _ := json.Marshal(a.Checks); string(cur) == mustJSON(set.Checks) && scope == replyguard.AllTiers {
				set.Checks = nil // unchanged from the guard's own
			}
		}
	}
	if err := replyguard.Put(set); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if set.Note == "" {
		replyguard.ClearPart(id, scope, "note")
	}
	if set.Retries == 0 {
		replyguard.ClearPart(id, scope, "retries")
	}
	if len(set.Checks) == 0 {
		replyguard.ClearPart(id, scope, "checks")
	}
	Log("[orchestrate.replyguard] %s on %s edited", id, scope)
	writeJSON(w, map[string]any{"ok": true})
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// handleReplyGuardClear removes a tier's own settings (Remove), or puts a
// built-in guard back as shipped (?revert=1). POST ?id=<guard>|<scope>.
func (T *OrchestrateApp) handleReplyGuardClear(w http.ResponseWriter, r *http.Request) {
	if !T.requireAdmin(w, r) {
		return
	}
	id, scope := guardScope(r)
	g, ok := replyguard.Lookup(id)
	if !ok {
		http.Error(w, "no such guard", http.StatusNotFound)
		return
	}
	if r.URL.Query().Get("revert") == "1" {
		if g.Authored {
			http.Error(w, "a drafted guard has no shipped version to revert to", http.StatusBadRequest)
			return
		}
		replyguard.Revert(id)
		Log("[orchestrate.replyguard] %s reverted to default", id)
	} else {
		replyguard.Clear(id, scope)
		Log("[orchestrate.replyguard] %s on %s cleared", id, scope)
	}
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

// subheading labels one table among several in a section: a table carries no
// heading of its own to say which question it answers.
func subheading(text string) ui.Card {
	return ui.Card{HTML: `<div style="font-size:var(--fs-xs, 0.74rem);letter-spacing:0.05em;text-transform:uppercase;color:var(--text-mute);margin:0.9rem 0 0.25rem">` +
		html.EscapeString(text) + `</div>`}
}

// replyGuardControls is the guard table, shown at the top of Correction
// checks (judge_records.go): every guard for all tiers and on the lead and the
// worker, what it caught there, and how it is set.
func replyGuardControls() []ui.Component {
	const guards = "/orchestrate/api/console/reply-guards"
	return []ui.Component{
		subheading("Guards, by tier"),
		ui.Table{Source: guards, RowKey: "_id", GroupBy: "group",
			Columns: []ui.Col{
				{Field: "scope_label", Label: "Tier", Flex: 2},
				{Field: "acted", Label: "Corrected", Format: "thousands"},
				{Field: "fixed", Label: "Done (recent)", Format: "thousands"},
				{Field: "misfired", Label: "Misfired (recent)", Format: "thousands"},
				{Field: "shadowed", Label: "Shadow", Format: "thousands"},
				{Field: "retries", Label: "Retries"},
				{Field: "last", Label: "Last", Format: "reltime", Mute: true},
				{Field: "follows", Label: "", Mute: true},
				{Field: "warning", Label: "", Flex: 3, Line: 2},
				{Field: "desc", Label: "", Flex: 4, Mute: true, Line: 2},
			},
			RowActions: []ui.RowAction{
				{Type: "segmented", Field: "_mode", PostTo: guards + "/mode?id={_id}", Options: replyGuardModeOptions()},
				{Type: "button", Label: "Edit", Compact: true, Method: "client", PostTo: "reply_guard_edit"},
				{Type: "button", Label: "Remove", Compact: true, OnlyIf: "_override", PostTo: guards + "/clear?id={_id}",
					Confirm: "Remove this tier's own settings? It follows All tiers again."},
				ui.Expand("Detail", ui.RecordView{Pairs: []ui.DisplayPair{
					{Label: "What the model is told", Field: "note_value", Block: true},
					{Label: "When it fires", Field: "fires", Block: true},
					{Label: "Recent replies it caught, and what each came to", Field: "samples", Block: true},
				}}),
			},
			// Under the guard's three rows, in its card, since it acts on the
			// whole guard (every tier) rather than on one row. Read against the
			// group's first row, which is the All tiers row.
			GroupActions: []ui.RowAction{
				{Type: "button", Label: "Revert to default", OnlyIf: "_can_revert", PostTo: guards + "/clear?id={_id}&revert=1",
					Confirm: "Put this guard back as it shipped? Every setting on it, for all tiers and each tier, is removed."},
			},
			EmptyText: "No reply guards are registered."},
	}
}

func replyGuardSections() []ui.Section {
	const flags = "/orchestrate/api/console/reply-flags"
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
						{Type: "button", Label: "Draft a guard", Compact: true, PostTo: "/orchestrate/api/console/reply-guards/draft?flag={id}",
							Confirm: "Draft a reply guard from this flagged reply? A model drafts it and it is tested against everything flagged; it appears under Drafted guards, and nothing runs until you enable it."},
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
