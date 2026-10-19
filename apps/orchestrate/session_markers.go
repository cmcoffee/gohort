package orchestrate

// Session recall: where an agent can look up an earlier conversation.
//
// [history] reaches what scrolled out of the cortex thread, and nothing
// reaches across sessions: an agent asked about "the session where we fixed
// the screener" had no way to find it. So each web session keeps a marker,
// and recall searches the markers as [session] hits. A marker says WHERE,
// the session says WHAT: recall with a hit's id reads the conversation
// itself, so what comes back is what was said, not a summary of it.
//
// A marker is built from what the session already records, with no model in
// the loop, so there is nothing to drift: its title and dates, the person's
// own messages (trimmed), what the session made or changed (the change
// approvals), and the files it delivered. It is rewritten after every turn,
// so it follows the session, and it goes when the session does.
//
// Markers are not the cortex. The cortex holds what arrives; an automatic
// line per web session made it read as a copy of the session list, which is
// why web sessions stopped writing there (retitleIfDue). Markers live
// in their own index and show only when recall finds one.
//
// On by default for an agent in chatbot memory mode, whose conversations are
// what it should remember; off for agent mode, a task specialist, where past
// sessions are mostly noise. AgentRecord.SessionRecall overrides either way.
// Never in an incognito session, never on a Clean turn, and never for an
// agent that answers from its corpus alone.

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	. "github.com/cmcoffee/gohort/core"
)

// sessionMarker is the record behind one session's marker. The searchable
// text is rebuilt from it plus the session's messages on every turn; what is
// kept here is what the session itself does not hold once a restart has
// cleared the in-memory change approvals.
type sessionMarker struct {
	Title   string    `json:"title"`
	Touched []string  `json:"touched,omitempty"` // "kind:name", from the change approvals
	Files   []string  `json:"files,omitempty"`   // names of the files the session delivered
	First   time.Time `json:"first"`
	Last    time.Time `json:"last"`
}

// Marker limits: enough of the person's own words to find the session by,
// not a second copy of it.
const (
	sessionMarkerAskRunes  = 240 // per message
	sessionMarkerHeadAsks  = 3   // the opening asks, which say what it was about
	sessionMarkerTailAsks  = 7   // and the latest ones, which say where it ended
	sessionMarkerReadChars = 8000
)

func sessionMarkerTable(agentID string) string { return "session_markers:" + agentID }

// sessionMarkerSource scopes one agent's markers in the chunk store. It ends
// in a colon so a prefix sweep for agent "a1" leaves agent "a10" alone.
func sessionMarkerSource(agentID string) string { return "smark:" + agentID + ":" }

func sessionMarkerReportID(agentID, sessionID string) string {
	return sessionMarkerSource(agentID) + sessionID
}

// sessionLink opens the session in the chat page.
func sessionLink(agentID, sessionID string) string {
	return "/orchestrate/?agent=" + url.QueryEscape(agentID) + "&session=" + url.QueryEscape(sessionID)
}

// sessionRecallOn reports whether an agent has session recall: its own
// setting, else on for chatbot memory mode and off for agent mode.
func sessionRecallOn(a AgentRecord) bool {
	switch strings.ToLower(strings.TrimSpace(a.SessionRecall)) {
	case "on":
		return true
	case "off":
		return false
	}
	return strings.EqualFold(strings.TrimSpace(a.MemoryMode), "chatbot")
}

// sessionRecallActive reports whether this turn can recall earlier sessions.
func (t *chatTurn) sessionRecallActive() bool {
	return t != nil && sessionRecallOn(t.agent) && !t.recallCorpusOnly() && !t.incognitoSession()
}

// noteSessionMarker rewrites this session's marker after a turn. Off the
// request: the reply is already saved, and the embed can take a moment.
func (t *chatTurn) noteSessionMarker() {
	if !t.sessionRecallActive() || t.inferredDisabled || t.session == nil || t.udb == nil {
		return
	}
	agentID, sessID := t.agent.ID, t.session.ID
	if agentID == "" || sessID == "" || sessID == cortexSessionID(agentID) {
		return
	}
	udb := t.udb
	touched := changeApprovalsFor(t.user, sessID).itemKeys()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				Log("[orchestrate.session_marker] panicked for session=%s: %v", sessID, r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		s, ok := loadChatSession(udb, agentID, sessID)
		if !ok {
			return
		}
		writeSessionMarker(ctx, udb, s, touched)
	}()
}

// writeSessionMarker saves the marker for s and indexes its text.
func writeSessionMarker(ctx context.Context, db Database, s ChatSession, touched []string) {
	var m sessionMarker
	db.Get(sessionMarkerTable(s.AgentID), s.ID, &m)
	m = updatedSessionMarker(m, s, touched)
	body := sessionMarkerText(m, s)
	if strings.TrimSpace(body) == "" {
		return
	}
	db.Set(sessionMarkerTable(s.AgentID), s.ID, m)
	if err := IngestRecallSpan(ctx, db, sessionMarkerSource(s.AgentID), sessionMarkerReportID(s.AgentID, s.ID),
		m.Title, body, "session_marker"); err != nil {
		Debug("[orchestrate.session_marker] session=%s: %v", s.ID, err)
	}
}

// updatedSessionMarker folds the session as it now stands into m: a title,
// the dates, and what it made, changed and delivered, kept from earlier
// turns.
func updatedSessionMarker(m sessionMarker, s ChatSession, touched []string) sessionMarker {
	m.Title = strings.TrimSpace(s.Title)
	if m.Title == "" {
		for _, ask := range sessionAsks(s) {
			m.Title = trimRunes(ask, 60)
			break
		}
	}
	m.First = s.Created
	m.Last = s.LastAt
	m.Touched = mergeSorted(m.Touched, touched)
	var files []string
	for _, msg := range s.Messages {
		for _, f := range msg.Files {
			if n := strings.TrimSpace(f.Name); n != "" {
				files = append(files, n)
			}
		}
	}
	m.Files = mergeSorted(m.Files, files)
	return m
}

// sessionAsks is what the person said in the session, oldest first, one
// line each.
func sessionAsks(s ChatSession) []string {
	var out []string
	for _, msg := range s.Messages {
		if msg.Role != "user" || msg.Hidden {
			continue
		}
		if line := strings.Join(strings.Fields(msg.Content), " "); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// sessionMarkerText is the searchable text of a marker.
func sessionMarkerText(m sessionMarker, s ChatSession) string {
	asks := sessionAsks(s)
	if len(asks) == 0 && len(m.Touched) == 0 && len(m.Files) == 0 {
		return ""
	}
	if len(asks) > sessionMarkerHeadAsks+sessionMarkerTailAsks {
		asks = append(asks[:sessionMarkerHeadAsks:sessionMarkerHeadAsks], asks[len(asks)-sessionMarkerTailAsks:]...)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Session %q, %s\n", m.Title, sessionDates(m))
	if len(asks) > 0 {
		b.WriteString("Asked:\n")
		for _, a := range asks {
			fmt.Fprintf(&b, "- %s\n", trimRunes(a, sessionMarkerAskRunes))
		}
	}
	if len(m.Touched) > 0 {
		var items []string
		for _, k := range m.Touched {
			items = append(items, strings.Replace(k, ":", " ", 1))
		}
		fmt.Fprintf(&b, "Made or changed: %s\n", strings.Join(items, ", "))
	}
	if len(m.Files) > 0 {
		fmt.Fprintf(&b, "Files delivered: %s\n", strings.Join(m.Files, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

// sessionDates is a marker's span, "2026-10-03" or "2026-10-03 to 2026-10-09".
func sessionDates(m sessionMarker) string {
	first, last := m.First.Format("2006-01-02"), m.Last.Format("2006-01-02")
	if m.First.IsZero() {
		return last
	}
	if first == last || m.Last.IsZero() {
		return first
	}
	return first + " to " + last
}

// recallSessionHits searches the agent's other sessions, newest first among
// equals, one hit per session.
func (t *chatTurn) recallSessionHits(query string, qVec []float32, k int) string {
	hits := SearchRecallVec(t.udb, sessionMarkerSource(t.agent.ID), query, qVec, k*3)
	prefix := sessionMarkerSource(t.agent.ID)
	seen := map[string]bool{}
	var b strings.Builder
	scores := recallScoreLog{query: query, layer: "session"}
	defer scores.emit()
	shown := 0
	for _, h := range hits {
		sessID := strings.TrimPrefix(h.ReportID, prefix)
		switch {
		case h.Score < RelevanceFloor:
			scores.add(h, "below floor")
			continue
		case t.session != nil && sessID == t.session.ID:
			scores.add(h, "this session")
			continue
		case seen[sessID]:
			continue
		case shown >= k:
			scores.add(h, "past the per-layer cap")
			continue
		}
		var m sessionMarker
		if !t.udb.Get(sessionMarkerTable(t.agent.ID), sessID, &m) {
			scores.add(h, "marker gone")
			continue
		}
		seen[sessID] = true
		shown++
		scores.add(h, "shown")
		fmt.Fprintf(&b, "- [session] %q, %s\n  id: sess:%s\n  link: %s\n  %s\n",
			m.Title, sessionDates(m), sessID, sessionLink(t.agent.ID, sessID), recallSnippet(h.Text))
	}
	return strings.TrimRight(b.String(), "\n")
}

// readSessionForRecall is the conversation behind a [session] hit: the
// person's messages and the replies, the opening and as much of the end as
// fits.
func (t *chatTurn) readSessionForRecall(sessID string) string {
	s, ok := loadChatSession(t.udb, t.agent.ID, sessID)
	if !ok {
		dropSessionMarker(t.udb, t.agent.ID, sessID)
		return "That session no longer exists (it was deleted), so there is nothing to read."
	}
	var m sessionMarker
	t.udb.Get(sessionMarkerTable(t.agent.ID), sessID, &m)
	m = updatedSessionMarker(m, s, nil)
	var lines []string
	for _, msg := range s.Messages {
		if msg.Hidden || strings.TrimSpace(msg.Content) == "" {
			continue
		}
		who := "Them"
		switch msg.Role {
		case "user":
		case "assistant":
			who = "You"
		default:
			continue
		}
		lines = append(lines, who+": "+trimRunes(strings.TrimSpace(msg.Content), 1200))
	}
	head := fmt.Sprintf("An earlier session, %q (%s). Link for the person: %s\n"+
		"It is a record of what was said then. Anything since may have changed it: where it disagrees with something newer, the newer one holds.\n\n",
		m.Title, sessionDates(m), sessionLink(t.agent.ID, sessID))
	return head + fitSessionLines(lines, sessionMarkerReadChars)
}

// fitSessionLines keeps the first two lines and as many of the last as fit
// in budget, saying how many were left out between them.
func fitSessionLines(lines []string, budget int) string {
	total := 0
	for _, l := range lines {
		total += len(l) + 1
	}
	if total <= budget || len(lines) <= 3 {
		return strings.Join(lines, "\n")
	}
	head := lines[:2]
	used := len(head[0]) + len(head[1]) + 2
	start := len(lines)
	for start > 2 && used+len(lines[start-1])+1 <= budget {
		start--
		used += len(lines[start]) + 1
	}
	out := append([]string{}, head...)
	if skipped := start - 2; skipped > 0 {
		out = append(out, fmt.Sprintf("[... %d messages left out ...]", skipped))
	}
	return strings.Join(append(out, lines[start:]...), "\n")
}

// normalizeSessionRecall is a SessionRecall value as stored: "on", "off", or
// "" for the default. Anything else, "default" included, reads as the
// default.
func normalizeSessionRecall(v string) string {
	switch v = strings.ToLower(strings.TrimSpace(v)); v {
	case "on", "off":
		return v
	}
	return ""
}

// dropSessionMarker removes one session's marker, with the session.
func dropSessionMarker(db Database, agentID, sessionID string) {
	if db == nil || agentID == "" || sessionID == "" {
		return
	}
	db.Unset(sessionMarkerTable(agentID), sessionID)
	DeleteReportChunks(db, sessionMarkerReportID(agentID, sessionID))
}

// dropAgentSessionMarkers removes every marker of an agent, with its
// sessions.
func dropAgentSessionMarkers(db Database, agentID string) {
	if db == nil || agentID == "" {
		return
	}
	db.Drop(sessionMarkerTable(agentID))
	WipeChunksBySourcePrefix(db, sessionMarkerSource(agentID))
}

// itemKeys is every item the session made or was approved to change.
func (s *changeApprovalSet) itemKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for k, on := range s.items {
		if on {
			out = append(out, k)
		}
	}
	return out
}

// mergeSorted is the sorted union of a and b, without blanks or repeats.
func mergeSorted(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range append(append([]string{}, a...), b...) {
		if v = strings.TrimSpace(v); v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// trimRunes cuts s to n runes, marking the cut.
func trimRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n])) + "…"
}
