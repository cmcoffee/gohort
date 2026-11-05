package orchestrate

// The people outside gohort an agent has served, for its owner and for an
// administrator (docs/design/outside-audience.md).
//
// They have no login, so somebody has to be able to look after them: see
// what one was told and what they said, clear what the agent remembers about
// them, and remove them entirely when they ask to be forgotten. Their stores
// are named by key and person (agenthost.OutsideScope), so finding them would
// mean walking every store in the database; instead each outside turn leaves
// a small index row in the OWNER's store, one per agent and person.

import (
	"encoding/base64"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/teamsbots"
	"github.com/cmcoffee/gohort/core/ui"
)

// outsidePeopleTable is the index, in the owner's store, keyed
// agentID + "|" + the person's runtime user.
const outsidePeopleTable = "outside_people"

// outsidePerson is one person's index row for one agent.
type outsidePerson struct {
	Agent string    `json:"agent"`
	Scope string    `json:"scope"`
	First time.Time `json:"first"`
	Last  time.Time `json:"last"`
	Turns int       `json:"turns"`
}

// notePersonTurn records an outside turn in the owner's index.
func notePersonTurn(base Database, owner, agentID, scope string, now time.Time) {
	odb := UserDB(base, owner)
	if odb == nil || agentID == "" || !agenthost.IsOutsideScope(scope) {
		return
	}
	key := agentID + "|" + scope
	var p outsidePerson
	odb.Get(outsidePeopleTable, key, &p)
	if p.First.IsZero() {
		p.First = now
	}
	p.Agent, p.Scope, p.Last = agentID, scope, now
	p.Turns++
	odb.Set(outsidePeopleTable, key, p)
}

// splitScope reads the key id and person back out of a runtime user.
func splitScope(scope string) (key, person string) {
	parts := strings.SplitN(strings.TrimPrefix(scope, "outside:"), ":", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

// personRef names one index row across owners: owner|agent|scope. The scope
// comes last since a person's id may hold "|"; an account name and an agent
// id never do.
func personRef(owner, agentID, scope string) string {
	return owner + "|" + agentID + "|" + scope
}

// personFor resolves a ref for the caller: its owner, or an administrator.
func (T *OrchestrateApp) personFor(c *jsonapi.Call, ref string) (owner string, p outsidePerson, err error) {
	parts := strings.SplitN(strings.TrimSpace(ref), "|", 3)
	if len(parts) != 3 || !agenthost.IsOutsideScope(parts[2]) {
		return "", p, jsonapi.BadRequest("not a person outside gohort")
	}
	owner = parts[0]
	if owner != c.User && !UserIsAdmin(c.User) {
		return "", p, jsonapi.Forbidden("only the agent's owner or an administrator looks after the people it serves")
	}
	odb := UserDB(T.DB, owner)
	if odb == nil || !odb.Get(outsidePeopleTable, parts[1]+"|"+parts[2], &p) {
		return "", p, jsonapi.NotFound("no such person on this agent")
	}
	return owner, p, nil
}

// personRows are the table rows for an owner's index, narrowed to one agent
// when agentID is set.
func (T *OrchestrateApp) personRows(owner, agentID string) []map[string]any {
	odb := UserDB(T.DB, owner)
	if odb == nil {
		return nil
	}
	keyNames := map[string]string{}
	for _, k := range ListAccountTokens(owner) {
		keyNames[k.ID] = k.Name
	}
	for _, b := range teamsbots.List(owner) {
		keyNames[b.OutsideSource()] = "Teams bot " + b.Name
	}
	loc := UserLocation(owner)
	var people []outsidePerson
	for _, k := range odb.Keys(outsidePeopleTable) {
		var p outsidePerson
		if odb.Get(outsidePeopleTable, k, &p) && (agentID == "" || p.Agent == agentID) {
			people = append(people, p)
		}
	}
	sort.Slice(people, func(i, j int) bool { return people[i].Last.After(people[j].Last) })
	rows := []map[string]any{}
	for _, p := range people {
		key, person := splitScope(p.Scope)
		keyName := keyNames[key]
		if keyName == "" {
			keyName = "(a key or bot since removed)"
		}
		pdb := UserDB(T.DB, p.Scope)
		agentName := p.Agent
		if a, ok := loadAgent(odb, p.Agent); ok {
			agentName = chFirst(a.Name, a.ID)
		}
		notes, convs := len(livePersonNotes(pdb, p.Agent)), len(listChatSessions(pdb, p.Agent))
		rows = append(rows, map[string]any{
			"id":            personRef(owner, p.Agent, p.Scope),
			"owner":         owner,
			"agent":         agentName,
			"person":        person,
			"key":           keyName,
			"first":         p.First.In(loc).Format("Jan 2, 2006"),
			"last":          p.Last.In(loc).Format("Jan 2 3:04 PM"),
			"turns":         p.Turns,
			"notes":         notes,
			"conversations": convs,
			"seen":          "Last seen " + p.Last.In(loc).Format("Jan 2, 3:04 PM"),
			"through":       "Through " + keyName,
			"activity":      countPhrase(p.Turns, "turn") + ", " + countPhrase(notes, "note") + ", " + countPhrase(convs, "conversation"),
		})
	}
	return rows
}

// countPhrase is "no notes", "1 note", "3 notes".
func countPhrase(n int, noun string) string {
	switch n {
	case 0:
		return "no " + noun + "s"
	case 1:
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// livePersonNotes are the saved notes a person's turns read on one agent.
func livePersonNotes(pdb Database, agentID string) []MemoryFact {
	var out []MemoryFact
	for _, f := range ListMemoryFacts(pdb, factsNamespace(agentID)) {
		if !f.Retired() {
			out = append(out, f)
		}
	}
	return out
}

// GET api/outside-people?agent=<id>: the people one of the caller's agents
// has served.
func (T *OrchestrateApp) handleOutsidePeople(c *jsonapi.Call) (any, error) {
	agentID := strings.TrimSpace(c.R.URL.Query().Get("agent"))
	if agentID == "" {
		return nil, jsonapi.BadRequest("agent is required")
	}
	if _, ok := loadAgent(c.UDB, agentID); !ok {
		return nil, jsonapi.NotFound("not one of your agents")
	}
	return T.personRows(c.User, agentID), nil
}

// GET api/outside-people/all: every owner's, for an administrator.
func (T *OrchestrateApp) handleAllOutsidePeople(c *jsonapi.Call) (any, error) {
	rows := []map[string]any{}
	if AuthDB == nil {
		return rows, nil
	}
	for _, u := range AuthListUsers(AuthDB()) {
		rows = append(rows, T.personRows(u.Username, "")...)
	}
	return rows, nil
}

// GET api/outside-people/notes?id=<ref>: what the agent saved about them.
func (T *OrchestrateApp) handleOutsidePersonNotes(c *jsonapi.Call) (any, error) {
	owner, p, err := T.personFor(c, c.R.URL.Query().Get("id"))
	if err != nil {
		return nil, err
	}
	loc := UserLocation(owner)
	rows := []map[string]any{}
	for _, f := range livePersonNotes(UserDB(T.DB, p.Scope), p.Agent) {
		rows = append(rows, map[string]any{"id": f.ID, "note": f.Note, "when": f.Created.In(loc).Format("Jan 2, 2006")})
	}
	return rows, nil
}

// conversationRef names one conversation of one person, for a row key that
// survives being put in a URL.
func conversationRef(ref, sessionID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(ref + "\n" + sessionID))
}

// GET api/outside-people/conversations?id=<ref>: their conversations.
func (T *OrchestrateApp) handleOutsidePersonConversations(c *jsonapi.Call) (any, error) {
	ref := c.R.URL.Query().Get("id")
	owner, p, err := T.personFor(c, ref)
	if err != nil {
		return nil, err
	}
	loc := UserLocation(owner)
	sessions := listChatSessions(UserDB(T.DB, p.Scope), p.Agent)
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].LastAt.After(sessions[j].LastAt) })
	rows := []map[string]any{}
	for _, s := range sessions {
		rows = append(rows, map[string]any{
			"ref":      conversationRef(ref, s.ID),
			"title":    chFirst(s.Title, "(untitled)"),
			"last":     s.LastAt.In(loc).Format("Jan 2 3:04 PM"),
			"messages": len(s.Messages),
		})
	}
	return rows, nil
}

// GET api/outside-people/conversation?ref=<conversation ref>: one transcript,
// read-only.
func (T *OrchestrateApp) handleOutsidePersonConversation(c *jsonapi.Call) (any, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(c.R.URL.Query().Get("ref")))
	if err != nil {
		return nil, jsonapi.BadRequest("not a conversation")
	}
	ref, sessionID, ok := strings.Cut(string(raw), "\n")
	if !ok {
		return nil, jsonapi.BadRequest("not a conversation")
	}
	_, p, err := T.personFor(c, ref)
	if err != nil {
		return nil, err
	}
	s, found := loadChatSession(UserDB(T.DB, p.Scope), p.Agent, sessionID)
	if !found {
		return nil, jsonapi.NotFound("no such conversation")
	}
	var b strings.Builder
	for _, m := range s.Messages {
		who := "Them"
		if m.Role == "assistant" {
			who = "Agent"
		} else if m.Role != "user" {
			continue
		}
		if text := strings.TrimSpace(m.Content); text != "" {
			b.WriteString(who + ": " + text + "\n\n")
		}
	}
	return map[string]any{"transcript": strings.TrimSpace(b.String())}, nil
}

// POST api/outside-people/clear?id=<ref>: forget what the agent saved about
// them (notes, findings, the graph). Their conversations stay.
func (T *OrchestrateApp) handleOutsidePersonClear(c *jsonapi.Call, _ struct{}) (any, error) {
	owner, p, err := T.personFor(c, c.R.URL.Query().Get("id"))
	if err != nil {
		return nil, err
	}
	if err := T.WipeScopedMemory(AgentScope{AgentID: p.Agent, ScopeUser: p.Scope}); err != nil {
		return nil, err
	}
	Log("[outside] %s cleared what %s's agent %s remembers about %s", c.User, owner, p.Agent, p.Scope)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// POST api/outside-people/delete?id=<ref>: remove the person entirely, from
// every agent of the owner's they reached: their store, their findings, their
// pictures, and their rows in the index.
func (T *OrchestrateApp) handleOutsidePersonDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	owner, p, err := T.personFor(c, c.R.URL.Query().Get("id"))
	if err != nil {
		return nil, err
	}
	T.deleteOutsidePerson(owner, p.Scope)
	Log("[outside] %s removed %s from %s's agents", c.User, p.Scope, owner)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// deleteOutsidePerson removes everything one outside person left.
func (T *OrchestrateApp) deleteOutsidePerson(owner, scope string) {
	if !agenthost.IsOutsideScope(scope) {
		return // never an account's store, whatever the caller passed
	}
	for _, h := range RegisteredUserDataHandlers() {
		if err := h.Purge(scope); err != nil {
			Log("[outside] removing %s: %s: %v", scope, h.Describe(scope).AppName, err)
		}
	}
	if pdb := UserDB(T.DB, scope); pdb != nil {
		for _, tbl := range pdb.Tables() {
			pdb.Drop(tbl)
		}
	}
	if VectorDB != nil {
		prefix := agentKnowledgePrefix(scope, "")
		DeleteChunksWhere(VectorDB, func(x EmbeddedChunk) bool { return strings.HasPrefix(x.Source, prefix) })
	}
	if odb := UserDB(T.DB, owner); odb != nil {
		for _, k := range odb.Keys(outsidePeopleTable) {
			if strings.HasSuffix(k, "|"+scope) {
				odb.Unset(outsidePeopleTable, k)
			}
		}
	}
}

// outsidePeopleTableUI is the People table: on an agent's Access page with
// source narrowed to it, and on the admin page across every owner.
func outsidePeopleTableUI(prefix, source string, admin bool) ui.Table {
	cols := []ui.Col{
		{Field: "person", Label: "Person", Flex: 2},
		{Field: "through", Label: "Through", Flex: 1},
		{Field: "seen", Label: "Last seen", Flex: 2},
		{Field: "activity", Label: "Activity", Flex: 2},
	}
	if admin {
		cols = append([]ui.Col{{Field: "owner", Label: "Owner", Flex: 1}, {Field: "agent", Label: "Agent", Flex: 1}}, cols...)
	}
	view := ui.Stack{Children: []ui.Component{
		ui.Table{
			Source:    prefix + "/api/outside-people/notes?id={id}",
			RowKey:    "id",
			EmptyText: "The agent has saved nothing about them.",
			Columns:   []ui.Col{{Field: "note", Label: "What the agent saved", Flex: 4}, {Field: "when", Label: "Saved", Flex: 1}},
		},
		ui.Table{
			Source:    prefix + "/api/outside-people/conversations?id={id}",
			RowKey:    "ref",
			EmptyText: "No conversations.",
			Columns:   []ui.Col{{Field: "title", Label: "Conversation", Flex: 3}, {Field: "last", Label: "Last", Flex: 1}, {Field: "messages", Label: "Messages", Flex: 1}},
			RowActions: []ui.RowAction{ui.Expand("Read", ui.DisplayPanel{
				Source: prefix + "/api/outside-people/conversation?ref={ref}",
				Pairs:  []ui.DisplayPair{{Label: "Transcript", Field: "transcript", Block: true}},
			})},
		},
	}}
	return ui.Table{
		Source:    source,
		RowKey:    "id",
		EmptyText: "Nobody outside gohort has talked to it yet.",
		Columns:   cols,
		RowActions: []ui.RowAction{
			ui.ModalAction("View", view),
			{Type: "button", Label: "Clear memory", PostTo: prefix + "/api/outside-people/clear?id={id}",
				Confirm: "Clear what the agent remembers about this person? Their conversations stay."},
			{Type: "button", Label: "Delete", Variant: "danger", PostTo: prefix + "/api/outside-people/delete?id={id}",
				Confirm: "Remove this person entirely: their memory and conversations, from every one of your agents they reached. This cannot be undone."},
		},
	}
}
