// Agents as a REFERENCE SOURCE — an agent offered to other apps as a knowledge
// base they can consult while drafting.
//
// The other sources in this registry expose a corpus: a servitor system's
// gathered facts, an MCP document space. An agent is a different kind of thing
// and a better one for this purpose, because it is not a pile of text — it is a
// reader of its own material. Fetch does not retrieve passages and hope they
// covered the question; it ASKS, and what comes back is an answer to the thing
// that was asked.
//
// That is what a drafting surface actually needs. "Write me the query for last
// quarter's unpaid invoices by region" turns on the exact table, the exact
// column holding the region, whether the amount is cents or decimal — details no
// summary written in advance can enumerate, because nobody knew which question
// was coming. A corpus answers that by luck of retrieval. An agent answers it
// by knowing.
//
// Registering here rather than teaching one app about agents means EVERY
// consumer gets it: the writer apps' Sources pickers list agents beside systems
// and doc spaces, with no consumer-side code at all, and nothing has to import
// orchestrate to do it.
package orchestrate

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/textutil"
)

// agentReferenceSource exposes the caller's own agents as reference items.
type agentReferenceSource struct{ app *OrchestrateApp }

func (s agentReferenceSource) Kind() string  { return AgentReferenceKind }
func (s agentReferenceSource) Label() string { return "Agents" }

// List offers the user's agents. Hidden agents are dropped — one kept out of
// orchestrate's own pickers should not reappear in another app's.
func (s agentReferenceSource) List(user string) []ReferenceItem {
	if s.app == nil || user == "" {
		return nil
	}
	var out []ReferenceItem
	for _, a := range s.app.AgentsForUser(user) {
		if a.Hidden {
			continue
		}
		out = append(out, ReferenceItem{ID: a.ID, Name: a.Name, Desc: a.Description})
	}
	return out
}

// Fetch asks the agent the consumer's current drafting question and returns its
// answer.
//
// The query is the WHOLE point here, where a corpus source may ignore it: an
// agent asked nothing has nothing to say, and an agent asked "the article topic"
// answers about the topic. So an empty query returns empty rather than
// dispatching a run that can only produce something generic at the cost of a
// full agent turn.
func (s agentReferenceSource) Fetch(ctx context.Context, user, itemID, query string) string {
	query = strings.TrimSpace(query)
	if s.app == nil || user == "" || itemID == "" || query == "" {
		return ""
	}
	// Framed as a consultation, not a task. Without this an agent reads a bare
	// question as a request to go DO something — and a drafting surface waiting
	// on a sentence of schema does not want a run that files a deliverable.
	ask := "A colleague is drafting something and needs to know this to get it right:\n\n" + query +
		"\n\nAnswer from what you actually know — the specific names, shapes, values and conventions involved. " +
		"If you don't have something, say which part you don't have rather than filling it in: they are about to write code or prose on top of this answer, " +
		"and an invented detail will look correct. Answer only; take no action and produce no deliverable."
	out, err := s.app.RunAgentSync(ctx, user, user, itemID, ask, "reference")
	if err != nil {
		Log("[orchestrate.reference] agent %q could not answer for %q: %v", itemID, user, err)
		return ""
	}
	return strings.TrimSpace(out)
}

// ItemTools gives the consumer a named tool per attached agent, so the model can
// ask FOLLOW-UP questions during a turn rather than living on the single answer
// the pre-fetch happened to pull.
//
// This is what makes an attached agent get used. The pre-fetch fires once, on
// the message as phrased; the second question — the one the first answer raises
// — has nowhere to go without this.
func (s agentReferenceSource) ItemTools(user, itemID string) []AgentToolDef {
	if s.app == nil || user == "" || itemID == "" {
		return nil
	}
	agent, ok := findAgentByNameOrID(UserDB(s.app.DB, user), user, itemID)
	if !ok {
		return nil
	}
	name := strings.TrimSpace(agent.Name)
	if name == "" {
		name = itemID
	}
	// Item-unique, per the ReferenceToolProvider contract: several attached
	// agents must not collide in one catalog.
	slug := textutil.SnakeFromDisplay(name)
	toolName := "consult_" + slug
	if slug == "" {
		// A name that slugs to nothing (punctuation, non-Latin script) would
		// mint a bare "consult_" — legal, but it collides with the next such
		// agent and tells the model nothing about who it is asking.
		toolName = "consult_agent"
	}
	return []AgentToolDef{{
		Tool: Tool{
			Name: toolName,
			Description: "Ask " + name + " about this domain — schemas, table and column names, interfaces, conventions, what a field holds, how something is done here. " +
				"Use it BEFORE writing anything whose correctness depends on a detail you would otherwise guess: a name, a type, a unit, a required parameter, an existing helper. " +
				"Guessing produces work that looks right and is wrong. Ask again when an answer raises another question — several small, specific questions beat one broad one.",
			Parameters: map[string]ToolParam{
				"question": {Type: "string", Description: "A specific question, as you would ask a colleague who knows this system. e.g. \"Which table holds invoice line items, and what column has the region?\""},
			},
			Required: []string{"question"},
			// Read, not network: whether answering reaches out is the AGENT's
			// business and its own tools' declarations, not this wrapper's.
			Caps: []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			q, _ := args["question"].(string)
			if strings.TrimSpace(q) == "" {
				return "", fmt.Errorf("question is required")
			}
			// Bounded: somebody is waiting on a draft. An agent that wanders off
			// investigating for minutes has failed this caller even if it
			// eventually replies.
			ctx, cancel := context.WithTimeout(context.Background(), agentConsultTimeout)
			defer cancel()
			out := s.Fetch(ctx, user, itemID, q)
			if strings.TrimSpace(out) == "" {
				// Reported, never swallowed: silence must not read to the model
				// as confirmation of whatever it was about to assume.
				return name + " returned no answer for that. Ask a narrower question, or tell the user this detail isn't covered — do not guess it.", nil
			}
			return out, nil
		},
	}}
}

// agentConsultTimeout bounds one consultation. Generous enough for an agent
// that has to search its own material, short enough that a drafting surface
// does not appear hung.
const agentConsultTimeout = 90 * time.Second
