// recall_dispatch_result — LLM-facing tool to retrieve a previously-
// stored async dispatch report (full raw text) when the user asks
// for more detail than the initial summary contained.
//
// Pattern B (async dispatch storage): each dispatch's raw report is
// kept in side storage keyed by a short ID. The chat LLM normally
// works from the worker-LLM summary that was passed back into the
// conversation. This tool is for the second-order case: "tell me
// more about X from that lookup" — the LLM retrieves the raw and
// surfaces the additional detail without re-dispatching.

package phantom

import (
	"errors"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// buildRecallDispatchResultTool returns the AgentToolDef bound to the
// active chat. Lookup is scoped to chatID so different chats can't
// snoop each other's dispatch results.
func buildRecallDispatchResultTool(db Database, chatID string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "recall_dispatch_result",
			Description: "Retrieve the FULL raw report from a prior async agent dispatch in this chat. The chat normally sees only a compact summary of dispatch results (delivered directly from the dispatched agent — you didn't generate it); call this when the user asks for more detail (\"tell me more about X\", \"what was their phone number again?\", \"show me the full report\") and the summary doesn't cover it.\n\nPRIOR DISPATCH SUMMARIES ARE TAGGED in chat history with a trailing [#dispatch:<id>] marker on the assistant turn that delivered them. To answer a follow-up, find the most recent matching marker in the prior turns and call this tool with that id. Example: assistant turn ends with [#dispatch:a4f2b1c8] → call recall_dispatch_result(id=\"a4f2b1c8\"). Without an `id`, returns a list of available dispatches with their briefs so you can pick.\n\nThe marker itself is invisible to the user (stripped before delivery); only you see it in your conversation history. Don't echo the marker back into your reply text.",
			Parameters: map[string]ToolParam{
				"id": {
					Type:        "string",
					Description: "Optional dispatch ID (short hex string). When omitted, returns a list of all available dispatches in this chat with their briefs and IDs.",
				},
			},
			Caps: []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			id := strings.TrimSpace(StringArg(args, "id"))
			if id == "" {
				return listDispatchResults(db, chatID), nil
			}
			r, ok := loadDispatchResult(db, chatID, id)
			if !ok {
				return "", fmt.Errorf("no dispatch result found with id %q in this chat (call without id to list available)", id)
			}
			if r.Raw == "" {
				return "", errors.New("dispatch result has no raw content stored")
			}
			return fmt.Sprintf(
				"Dispatch %q (agent: %s) — brief: %s\n\n--- FULL REPORT ---\n\n%s",
				r.ID, r.Agent, r.Brief, r.Raw,
			), nil
		},
	}
}

// listDispatchResults formats the chat's stored dispatch results as
// an LLM-readable list. Used when recall_dispatch_result is called
// without an ID — lets the LLM pick which one the user is asking
// about.
func listDispatchResults(db Database, chatID string) string {
	results := listDispatchResultsForChat(db, chatID)
	if len(results) == 0 {
		return "No async dispatch results stored for this chat."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Available dispatch results (newest first, %d total):\n\n", len(results))
	now := time.Now()
	for _, r := range results {
		age := now.Sub(r.Created).Round(time.Minute)
		fmt.Fprintf(&b, "id=%s  agent=%s  age=%s\n  brief: %s\n\n",
			r.ID, r.Agent, age, truncateStr(r.Brief, 200))
	}
	b.WriteString("Call recall_dispatch_result with the chosen id to retrieve the full raw report.")
	return b.String()
}
