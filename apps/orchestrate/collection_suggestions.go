package orchestrate

// Suggestions: an agent that READS a collection says what it learned that the
// collection should know, and whoever looks after the collection decides.
//
// The reader does not write. An agent that wrote its own conclusions into the
// corpus it reads would retrieve them later as fact and build on them, with
// nobody between what it believed and what it reads next. So a reader files a
// suggestion, naming its source, and the change is made, or not, by the
// collection's curator agent (Collection.CuratorAgent), which checks it, or by
// the owner on the collection's page when nobody is in charge. The owner can
// turn suggestions off (Collection.SuggestionsOff).
//
// Who may suggest is who may already add: the owner and the contributors,
// through their own agents on their own runs. A suggestion carries text out of
// somebody's chat to whoever looks after the collection, so it is offered only
// where adding the text directly would already be allowed.
//
// Suggestions live beside the collection's record, one row each, rather than
// on it: an auto-fill run saves the record as it goes, and a suggestion filed
// meanwhile must not be lost to that.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// suggestCollectionUpdateName is the reading agent's tool. Reserved, as every
// tool minted per agent is, so a custom tool cannot take its name and stand in
// for it (core/reserved_tools.go).
const suggestCollectionUpdateName = "suggest_collection_update"

func init() { RegisterReservedToolName(suggestCollectionUpdateName) }

// collectionSuggestionsTable holds pending suggestions, keyed "<collection>/<id>".
const collectionSuggestionsTable = "collection_suggestions"

// maxPendingSuggestions bounds one collection's queue. A queue that long is not
// being read, and more would only bury the ones that are.
const maxPendingSuggestions = 50

// maxSuggestionChars bounds one suggestion: what should change and why, not the
// document itself.
const maxSuggestionChars = 2000

// collectionSuggestion is one pending suggestion.
type collectionSuggestion struct {
	ID         string    `json:"id"`
	Collection string    `json:"collection"`
	Text       string    `json:"text"`
	Source     string    `json:"source,omitempty"`
	Agent      string    `json:"agent,omitempty"` // the suggesting agent's id
	AgentName  string    `json:"agent_name,omitempty"`
	User       string    `json:"user,omitempty"` // whose run it was
	Session    string    `json:"session,omitempty"`
	Created    time.Time `json:"created"`
}

// suggestionsDB is where a collection's suggestions live: its owner's store, or
// the deployment's for a collection with no owner or a deployment-wide one.
func (T *OrchestrateApp) suggestionsDB(c Collection) Database {
	if IsDeploymentScope(c) || c.Owner == "" {
		return T.DB
	}
	return UserDB(T.DB, c.Owner)
}

// listSuggestions returns a collection's pending suggestions, oldest first.
func (T *OrchestrateApp) listSuggestions(c Collection) []collectionSuggestion {
	db := T.suggestionsDB(c)
	if db == nil {
		return nil
	}
	prefix := c.ID + "/"
	var out []collectionSuggestion
	for _, k := range db.Keys(collectionSuggestionsTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		var s collectionSuggestion
		if db.Get(collectionSuggestionsTable, k, &s) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// addSuggestion files one, refusing a repeat of one still waiting and one past
// the queue's cap.
func (T *OrchestrateApp) addSuggestion(c Collection, s collectionSuggestion) (collectionSuggestion, error) {
	db := T.suggestionsDB(c)
	if db == nil {
		return s, errors.New("suggestions are unavailable: the database is not open")
	}
	pending := T.listSuggestions(c)
	for _, p := range pending {
		if strings.EqualFold(strings.TrimSpace(p.Text), strings.TrimSpace(s.Text)) {
			return p, errAlreadySuggested
		}
	}
	if len(pending) >= maxPendingSuggestions {
		return s, fmt.Errorf("%d suggestions are already waiting on %q, the most it holds: nothing more can be suggested until some are dealt with", len(pending), c.Name)
	}
	s.ID = UUIDv4()
	s.Collection = c.ID
	s.Created = time.Now()
	db.Set(collectionSuggestionsTable, c.ID+"/"+s.ID, s)
	return s, nil
}

var errAlreadySuggested = errors.New("already suggested")

// findSuggestion returns one pending suggestion of this collection.
func (T *OrchestrateApp) findSuggestion(c Collection, id string) (collectionSuggestion, bool) {
	db := T.suggestionsDB(c)
	id = strings.TrimSpace(id)
	if db == nil || id == "" {
		return collectionSuggestion{}, false
	}
	var s collectionSuggestion
	ok := db.Get(collectionSuggestionsTable, c.ID+"/"+id, &s)
	return s, ok
}

// dropSuggestion removes one; it has been acted on or declined.
func (T *OrchestrateApp) dropSuggestion(c Collection, id string) bool {
	if _, ok := T.findSuggestion(c, id); !ok {
		return false
	}
	T.suggestionsDB(c).Unset(collectionSuggestionsTable, c.ID+"/"+id)
	return true
}

// dropAllSuggestions removes a collection's whole queue, with the collection.
func (T *OrchestrateApp) dropAllSuggestions(c Collection) {
	for _, s := range T.listSuggestions(c) {
		T.suggestionsDB(c).Unset(collectionSuggestionsTable, c.ID+"/"+s.ID)
	}
}

// suggestionsGoTo names who acts on this collection's suggestions, for the
// suggesting agent and for the page: its curator agent, or its owner.
// The curator is looked up among the OWNER's agents, which is where the
// collection page chose it from, whoever is asking.
func (T *OrchestrateApp) suggestionsGoTo(user string, c Collection) string {
	if who := strings.TrimSpace(c.CuratorAgent); who != "" && c.Owner != "" {
		if a, ok := findAgentByNameOrID(UserDB(T.DB, c.Owner), c.Owner, who); ok {
			return "the agent in charge of it, " + chFirst(a.Name, who)
		}
	}
	if c.Owner == "" || c.Owner == user {
		return "you, on the collection's page"
	}
	return "its owner, " + c.Owner
}

// --- the reading agent's tool ------------------------------------------------

// suggestableCollectionsFor returns the collections this agent may suggest
// updates to: attached to it, taking suggestions, ones its user may already add
// to, and not ones it is in charge of, which it changes directly.
func (T *OrchestrateApp) suggestableCollectionsFor(user string, a AgentRecord) []Collection {
	udb := UserDB(T.DB, user)
	curated := map[string]bool{}
	for _, c := range curatedCollectionsFor(udb, user, a.ID) {
		curated[c.ID] = true
	}
	admin := UserIsAdmin(user)
	var out []Collection
	seen := map[string]bool{}
	for _, id := range a.AttachedCollections {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] || curated[id] {
			continue
		}
		seen[id] = true
		c, ok := loadCollection(udb, user, id)
		if !ok || c.SuggestionsOff {
			continue
		}
		if collectionWriteRefusal(c, user, "suggest", http.MethodPost, admin) != "" {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// suggestCollectionUpdateTool builds the reading agent's tool, or reports
// false when it has no collection to suggest to.
func (t *chatTurn) suggestCollectionUpdateTool() (AgentToolDef, bool) {
	T := t.app
	cols := T.suggestableCollectionsFor(t.user, t.agent)
	if len(cols) == 0 {
		return AgentToolDef{}, false
	}
	byID := map[string]Collection{}
	var names []string
	for _, c := range cols {
		byID[c.ID] = c
		names = append(names, fmt.Sprintf("%q (%s), whose suggestions go to %s", c.Name, c.ID, T.suggestionsGoTo(t.user, c)))
	}
	params := map[string]ToolParam{
		"suggestion": {Type: "string", Description: "What should change and why: which document is wrong or out of date and what is true now, or what is missing. Specific enough to act on without this conversation."},
		"source":     {Type: "string", Description: "Where you learned it: the link you read, the tool whose result showed it, or that the user told you. A suggestion with no source is a guess, and is not worth filing."},
	}
	required := []string{"suggestion", "source"}
	if len(cols) > 1 {
		params["collection"] = ToolParam{Type: "string", Description: "Which collection, by id."}
		required = append(required, "collection")
	}
	desc := "Suggest an update to a knowledge collection you read, when you learn that a document in it is wrong or out of date, or that something it should hold is missing. " +
		"It changes nothing by itself: whoever looks after the collection checks the suggestion and decides. " +
		"Suggest only what you learned from a source you can name, never your own conclusion or a guess, and nothing private from this conversation: everybody who reads the collection reads what ends up in it. " +
		"Collections: " + strings.Join(names, "; ") + "."
	return AgentToolDef{
		Tool: Tool{
			Name:        suggestCollectionUpdateName,
			Description: desc,
			Parameters:  params,
			Required:    required,
			Caps:        []Capability{CapWrite},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			text := strings.TrimSpace(StringArg(args, "suggestion"))
			source := strings.TrimSpace(StringArg(args, "source"))
			if text == "" {
				return "", errors.New("suggestion is required: say what should change and why")
			}
			if source == "" {
				return "", errors.New("source is required: name where you learned it (a link, a tool result, or the user). Without one, do not suggest it")
			}
			if len([]rune(text)) > maxSuggestionChars {
				return "", fmt.Errorf("suggestion is %d characters, more than %d: say what should change and point at the source, rather than including the document", len([]rune(text)), maxSuggestionChars)
			}
			id := strings.TrimSpace(StringArg(args, "collection"))
			if id == "" && len(cols) == 1 {
				id = cols[0].ID
			}
			c, ok := byID[id]
			if !ok {
				return "", fmt.Errorf("you cannot suggest updates to collection %q; you can to: %s", id, strings.Join(names, "; "))
			}
			session := ""
			if t.session != nil {
				session = t.session.ID
			}
			s, err := T.addSuggestion(c, collectionSuggestion{
				Text: text, Source: source,
				Agent: t.agent.ID, AgentName: t.agent.Name,
				User: t.user, Session: session,
			})
			goesTo := T.suggestionsGoTo(t.user, c)
			if errors.Is(err, errAlreadySuggested) {
				return fmt.Sprintf("That is already suggested and waiting for %s. Nothing more to do.", goesTo), nil
			}
			if err != nil {
				return "", err
			}
			Log("[orchestrate.collections] agent %q (%s) for %q suggested an update to collection %q (%s): suggestion %s",
				t.agent.Name, t.agent.ID, t.user, c.Name, c.ID, s.ID)
			return fmt.Sprintf("Suggested. It goes to %s, who checks it and decides; nothing in %q has changed. Do not tell the user the collection is updated.", goesTo, c.Name), nil
		},
	}, true
}

// --- the curator's half --------------------------------------------------------

// addStewardSuggestionActions gives the agent in charge its queue: the waiting
// suggestions, and a way to close one once it has acted on it or declined it.
func (T *OrchestrateApp) addStewardSuggestionActions(gt *GroupedTool, cols []Collection) {
	allowed := map[string]Collection{}
	for _, c := range cols {
		allowed[c.ID] = c
	}
	pick := func(args map[string]any) (Collection, error) {
		id := strings.TrimSpace(stringArg(args, "id"))
		if id == "" && len(cols) == 1 {
			id = cols[0].ID
		}
		c, ok := allowed[id]
		if !ok {
			return Collection{}, fmt.Errorf("you are not in charge of collection %q", id)
		}
		return c, nil
	}
	idParam := func(p map[string]ToolParam) ([]string, map[string]ToolParam) {
		if len(cols) == 1 {
			return nil, p
		}
		p["id"] = ToolParam{Type: "string", Description: "Which collection, by id."}
		return []string{"id"}, p
	}
	sugReq, sugParams := idParam(map[string]ToolParam{})
	gt.AddAction("suggestions", &GroupedToolAction{
		Description: "List the suggestions agents reading this collection have filed: what they say should change, their source, and who filed them. Check each against its source before changing anything, then close it with resolve_suggestion.",
		Params:      sugParams,
		Required:    sugReq,
		Handler: func(args map[string]any, _ *ToolSession) (string, error) {
			c, err := pick(args)
			if err != nil {
				return "", err
			}
			list := T.listSuggestions(c)
			if len(list) == 0 {
				return "No suggestions are waiting.", nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%d suggestion(s) waiting on %q. Each is a claim from an agent that READ this collection: verify it at its source before acting.\n", len(list), c.Name)
			for _, s := range list {
				fmt.Fprintf(&b, "\n- suggestion_id %s, from %s on %s\n  Suggests: %s\n  Source: %s\n",
					s.ID, chFirst(s.AgentName, "an agent"), s.Created.Format("Jan 2"), s.Text, chFirst(s.Source, "(none given)"))
			}
			return b.String(), nil
		},
	})
	resReq, resParams := idParam(map[string]ToolParam{
		"suggestion_id": {Type: "string", Description: "The suggestion to close, from action=suggestions."},
		"outcome":       {Type: "string", Description: "applied (you changed the collection for it) or declined (its source did not hold up, or it does not belong here)."},
		"note":          {Type: "string", Description: "One line on what you did or why not."},
	})
	gt.AddAction("resolve_suggestion", &GroupedToolAction{
		Description: "Close a suggestion once you have acted on it or decided not to. It leaves the queue either way.",
		Params:      resParams,
		Required:    append(resReq, "suggestion_id", "outcome"),
		Caps:        []Capability{CapWrite},
		Handler: func(args map[string]any, _ *ToolSession) (string, error) {
			c, err := pick(args)
			if err != nil {
				return "", err
			}
			outcome := strings.ToLower(strings.TrimSpace(stringArg(args, "outcome")))
			if outcome != "applied" && outcome != "declined" {
				return "", errors.New("outcome must be applied or declined")
			}
			sid := strings.TrimSpace(stringArg(args, "suggestion_id"))
			s, ok := T.findSuggestion(c, sid)
			if !ok {
				return "", fmt.Errorf("no suggestion %q is waiting on %q: list them with action=suggestions", sid, c.Name)
			}
			T.dropSuggestion(c, sid)
			Log("[orchestrate.collections] suggestion %s on collection %q %s by its curator (from %q): %s",
				sid, c.ID, outcome, s.AgentName, strings.TrimSpace(stringArg(args, "note")))
			return fmt.Sprintf("Suggestion %s closed as %s.", sid, outcome), nil
		},
	})
}

// stewardWaitingNote is the line the curator's tool carries about its queue,
// so a waiting suggestion is seen without the curator having to look.
func (T *OrchestrateApp) stewardWaitingNote(cols []Collection) string {
	n := 0
	for _, c := range cols {
		n += len(T.listSuggestions(c))
	}
	switch n {
	case 0:
		return ""
	case 1:
		return " 1 suggestion from an agent that reads it is waiting: see action=suggestions."
	default:
		return fmt.Sprintf(" %d suggestions from agents that read it are waiting: see action=suggestions.", n)
	}
}

// --- the owner's half: the collection page -------------------------------------

// collectionSuggestions lists the queue for the page, with the switch.
func (T *OrchestrateApp) collectionSuggestions(call *jsonapi.Call) (any, error) {
	c, err := T.collectionAt(call, "suggestions")
	if err != nil {
		return nil, err
	}
	rows := []map[string]any{}
	for _, s := range T.listSuggestions(c) {
		link := ""
		if isArticleLink(s.Source) {
			link = s.Source
		}
		from := chFirst(s.AgentName, "an agent")
		if s.User != "" && s.User != call.User {
			from += ", for " + s.User
		}
		rows = append(rows, map[string]any{
			"id":          s.ID,
			"text":        s.Text,
			"source":      chFirst(s.Source, "(none given)"),
			"source_link": link,
			"from":        from,
			"created":     s.Created,
		})
	}
	return map[string]any{
		"suggestions":        rows,
		"accept_suggestions": !c.SuggestionsOff,
	}, nil
}

// collectionSuggestionsSettings saves the switch.
func (T *OrchestrateApp) collectionSuggestionsSettings(call *jsonapi.Call, body struct {
	AcceptSuggestions bool `json:"accept_suggestions"`
}) (any, error) {
	c, err := T.collectionAt(call, "suggestions")
	if err != nil {
		return nil, err
	}
	c.SuggestionsOff = !body.AcceptSuggestions
	saveCollection(T.collectionHome(call.UDB, c), c)
	return map[string]any{"ok": true}, nil
}

// collectionSuggestionDismiss closes one from the page.
func (T *OrchestrateApp) collectionSuggestionDismiss(call *jsonapi.Call, _ struct{}) (any, error) {
	c, err := T.collectionAt(call, "suggestions")
	if err != nil {
		return nil, err
	}
	if !T.dropSuggestion(c, call.R.URL.Query().Get("sid")) {
		return nil, errNotFound
	}
	return map[string]any{"ok": true}, nil
}

// collectionSuggestionFetch fetches the page a suggestion names into the
// collection and closes it: the one action a suggestion's source makes
// mechanical. Anything else a suggestion asks for is the owner's to do by hand.
func (T *OrchestrateApp) collectionSuggestionFetch(call *jsonapi.Call, _ struct{}) (any, error) {
	c, err := T.collectionAt(call, "suggestions")
	if err != nil {
		return nil, err
	}
	sid := call.R.URL.Query().Get("sid")
	s, ok := T.findSuggestion(c, sid)
	if !ok {
		return nil, errNotFound
	}
	if !isArticleLink(s.Source) {
		return nil, jsonapi.BadRequest("this suggestion's source is not a link, so there is nothing to fetch")
	}
	if collectionHasURL(c, s.Source) {
		T.dropSuggestion(c, sid)
		return map[string]any{"ok": true, "already": true}, nil
	}
	// Browser fetch can run 5-20s; give the headless fallback room.
	ctx, cancel := context.WithTimeout(call.R.Context(), 90*time.Second)
	defer cancel()
	name, text, raw, mime, ferr := fetchAndExtractForIngest(ctx, s.Source)
	if ferr != nil {
		return nil, jsonapi.BadRequest("could not fetch it: " + ferr.Error())
	}
	if len(text) > maxAutofillDocChars {
		return nil, jsonapi.BadRequest(fmt.Sprintf("that page is %d characters of text, past the %d a document may have", len(text), maxAutofillDocChars))
	}
	chunks := T.ingestAutofillDoc(ctx, c, name, text, raw, mime)
	T.recordIngest(call.UDB, &c, s.Source, "")
	T.dropSuggestion(c, sid)
	Log("[orchestrate.collections] user=%q fetched suggestion %s's link %q into collection %q (%d chunks)", call.User, sid, s.Source, c.ID, chunks)
	return map[string]any{"ok": true, "chunks": chunks}, nil
}
