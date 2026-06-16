// Operator history drill-in tools — the retrieval half of the LCM-style
// continuity archive (see archiveOperatorSpan in operator_compaction.go).
//
// The Operator runs as one ongoing pinned thread that compacts into a lossy
// rolling summary. That summary is fragile: a flood of near-identical wakes can
// crystallize into it and dominate every later turn, and exact details (a
// number, a name, a past decision) get glossed. These tools give the Operator a
// way OUT of relying on the summary — it searches the real archived record on
// demand:
//
//   recall_history(query)   — semantic + keyword search over folded-away spans,
//                             plus durable facts. Returns snippets + span ids.
//   expand_history(span_id) — pull a full archived span back when a snippet is
//                             relevant but you need the surrounding exchange.
//
// Both are scoped to the Operator's own thread (the lcm:<agent>:<session>
// source) and read the same per-user db the fold writes to.

package orchestrate

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func operatorHistoryTools(sess *ToolSession, agentID string) []AgentToolDef {
	var db Database
	if sess != nil {
		db = sess.DB
	}
	source := operatorLCMSource(agentID, cortexSessionID(agentID))

	return []AgentToolDef{
		{
			Tool: Tool{
				Name:        "recall_history",
				Description: "Search your OWN earlier conversation with the user — the older exchanges that have aged out of the visible window and the running summary. Use it when the user refers to something from before that you don't see in the recent messages, or when you need an exact detail (a number, a name, a past decision) the summary glossed over. Returns matching snippets, each with a span id you can pass to expand_history for the full surrounding context. Trust this over your memory of the summary.",
				Parameters: map[string]ToolParam{
					"query": {Type: "string", Description: "What to look for — a topic, name, decision, or keyword from the earlier conversation."},
				},
				Required: []string{"query"},
			},
			Handler: func(args map[string]any) (string, error) {
				q := strings.TrimSpace(oArgStr(args, "query"))
				if q == "" {
					return "", fmt.Errorf("query is required")
				}
				if db == nil {
					return "", fmt.Errorf("history index unavailable")
				}
				hits := SearchRecall(db, source, q, 6)
				facts := SearchMemoryFacts(db, factsNamespace(agentID), q)

				if len(hits) == 0 && len(facts) == 0 {
					return "Nothing in the archived history matches that.", nil
				}
				var b strings.Builder
				if len(facts) > 0 {
					b.WriteString("Durable facts:\n")
					for i, f := range facts {
						if i >= 8 {
							break
						}
						fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(f.Note))
					}
					b.WriteString("\n")
				}
				if len(hits) > 0 {
					b.WriteString("History snippets (pass a span id to expand_history for the full exchange):\n")
					for _, h := range hits {
						snip := strings.TrimSpace(h.Text)
						if len(snip) > 300 {
							snip = snip[:300] + "…"
						}
						label := h.Title
						if label == "" {
							label = h.Section
						}
						fmt.Fprintf(&b, "- [span: %s] %s\n  %s\n", h.ReportID, label, snip)
					}
				}
				return strings.TrimSpace(b.String()), nil
			},
		},
		{
			Tool: Tool{
				Name:        "expand_history",
				Description: "Fetch the full text of an archived history span by its id (the value after 'span:' from recall_history). Use when a recall snippet is relevant but you need the surrounding exchange in full.",
				Parameters: map[string]ToolParam{
					"span_id": {Type: "string", Description: "The span id returned by recall_history."},
				},
				Required: []string{"span_id"},
			},
			Handler: func(args map[string]any) (string, error) {
				id := strings.TrimSpace(oArgStr(args, "span_id"))
				if id == "" {
					return "", fmt.Errorf("span_id is required")
				}
				if db == nil {
					return "", fmt.Errorf("history index unavailable")
				}
				chunks := FetchRecallSpanChunks(db, source, id)
				if len(chunks) == 0 {
					return "No such history span (it may have been cleared).", nil
				}
				doc := AssembleChunkDoc(chunks, 8000)
				if strings.TrimSpace(doc) == "" {
					return "That span is empty.", nil
				}
				return doc, nil
			},
		},
	}
}

