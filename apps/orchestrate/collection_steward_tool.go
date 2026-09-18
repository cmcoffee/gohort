package orchestrate

// The narrow grant: an agent in charge of ONE collection.
//
// The shape this serves is two agents and one corpus. Agent A goes and gets
// things and keeps the collection current; Agent B attaches it and reads it,
// which is what attaching a collection has always done and needs nothing new.
// Only A's half was missing.
//
// It was missing rather than impossible. The corpus actions already exist on
// the `collections` tool — docs, add_url, add_text, remove_doc — but they ride
// in on the AUTHORING catalog, which is all-or-nothing: an agent that should
// keep one collection current would arrive holding create_agent, update_agent
// and tool_def, able to rewrite the whole fleet, and paying about a third of
// its prompt for tools it never calls. It would also be able to prune any OTHER
// collection the user owns, including the one Agent B reads.
//
// So this derives the four corpus actions from that same tool and binds them to
// the collections the agent is actually in charge of. Derived rather than
// reimplemented: a second copy of "ingest a URL into a collection" would be a
// second place for that to be subtly different, and the first divergence would
// show up as a curated collection whose documents are shaped unlike everybody
// else's.
//
// Who is in charge is read from Collection.CuratorAgent, the collection's own
// field, so the answer is the same one the collection page shows and there is
// no second place to keep it.

import (
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// stewardActions are the four an agent needs to KEEP a collection, and no more.
//
// list/get/create/update are deliberately absent. They are about collections as
// records — minting them, renaming them, finding their ids — which is authoring
// work and belongs to whoever authors. An agent in charge of a corpus needs to
// see what is in it, add to it, and prune it.
var stewardActions = []string{"docs", "add_url", "add_text", "remove_doc"}

// curatedCollectionsFor returns the collections this agent is in charge of,
// by id, sorted.
func curatedCollectionsFor(udb Database, user, agentID, agentName string) []Collection {
	var out []Collection
	for _, c := range ListCollections(udb, user) {
		who := strings.TrimSpace(c.CuratorAgent)
		if who == "" {
			continue
		}
		// Matched against BOTH id and name because that is what the dispatch
		// resolver accepts (findAgentByNameOrID), and a field that runs under
		// one spelling while reading as unset under another is the bug that
		// costs an afternoon.
		if strings.EqualFold(who, agentID) || (agentName != "" && strings.EqualFold(who, agentName)) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// collectionStewardTool builds the scoped tool for one agent's collections, or
// nil when it is in charge of none.
func collectionStewardTool(cols []Collection) ChatTool {
	if len(cols) == 0 {
		return nil
	}
	base := collectionsListTool()
	grouped, ok := base.(*GroupedTool)
	if !ok {
		return nil
	}

	allowed := make(map[string]string, len(cols)) // id -> name
	var names []string
	for _, c := range cols {
		allowed[c.ID] = c.Name
		names = append(names, fmt.Sprintf("%q (%s)", c.Name, c.ID))
	}
	only := cols[0].ID

	brief := "Keep the collection you are in charge of current: see what is in it, add to it, and prune it. " +
		"Scoped to " + strings.Join(names, ", ") + " and nothing else."
	gt := NewGroupedTool("collection", brief)

	for _, act := range stewardActions {
		a, found := grouped.Action(act)
		if !found {
			continue
		}
		inner := a.Handler
		if len(cols) == 1 {
			// One collection means there is nothing to choose, so there is no
			// parameter. A model cannot target the wrong corpus if it is never
			// asked which, and the id does not have to survive a round trip
			// through a prompt to come back correct.
			delete(a.Params, "id")
			a.Required = dropString(a.Required, "id")
		} else if p, has := a.Params["id"]; has {
			p.Description = "Which collection, one of: " + strings.Join(names, ", ")
			a.Params["id"] = p
		}
		a.Handler = func(args map[string]any, sess *ToolSession) (string, error) {
			if args == nil {
				args = map[string]any{}
			}
			id := strings.TrimSpace(stringArg(args, "id"))
			if id == "" {
				id = only
			}
			// The gate. An id outside the grant is REFUSED rather than
			// substituted: quietly redirecting the call to the right
			// collection would teach the agent that any id works, and the one
			// time the substitution guessed wrong it would have written to a
			// corpus nobody asked it to touch.
			if _, granted := allowed[id]; !granted {
				return "", fmt.Errorf("you are not in charge of collection %q, so you cannot change it; yours is %s", id, strings.Join(names, ", "))
			}
			args["id"] = id
			return inner(args, sess)
		}
		gt.AddAction(act, &a)
	}
	if len(gt.ActionNames()) == 0 {
		return nil
	}
	return gt
}

func dropString(ss []string, drop string) []string {
	out := ss[:0]
	for _, s := range ss {
		if s != drop {
			out = append(out, s)
		}
	}
	return out
}
