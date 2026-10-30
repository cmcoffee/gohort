package agenthost

// Proposing a change to a co-authored document: the server half of the
// client's uiCoauthor (core/ui/assets/runtime/47_coauthor.js). The person
// selects a passage, asks about it, and the agent answers with propose_edit:
// the passage and its replacement, which the person reviews as a diff of that
// passage and applies or not. Nothing here writes anything: the document lives
// in the browser until its own Save. See docs/design/coauthor-proposals.md.

import (
	"context"
	"fmt"
	"strings"

	"github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
)

// Selection is the passage a person pinned before sending: what the send
// carries as "selection" when the document is co-authored. Start and End are
// offsets into the document as it was on screen.
type Selection struct {
	Text  string `json:"text"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// SelectionContext is what a turn is told about a pinned passage, to go with
// whatever the app already says about the document: which passage the message
// is about, and how to propose a change to it. "" when nothing is pinned.
func SelectionContext(sel Selection) string {
	if strings.TrimSpace(sel.Text) == "" {
		return ""
	}
	f := docs.DocFence(sel.Text)
	return "The person selected this passage of the document and their message is about it:\n" +
		f + "\n" + sel.Text + "\n" + f + "\n" +
		"Answer about that passage. To change it, call propose_edit with find set to the text you are replacing, " +
		"copied exactly from the passage, and replace set to its new text. Change only what the message asks: " +
		"do not propose the whole document."
}

// HighlightContext is what a turn is told about a passage the person
// highlighted in a document its agent changes its own way (a rendered page
// with section tools, say), rather than by proposing: the passage, and to keep
// a change to it. "" when nothing was highlighted.
func HighlightContext(sel Selection) string {
	if strings.TrimSpace(sel.Text) == "" {
		return ""
	}
	f := docs.DocFence(sel.Text)
	return "The person highlighted this passage of the document, and their message is about it:\n" +
		f + "\n" + sel.Text + "\n" + f + "\n" +
		"Answer about that passage. When the message asks for a change, change that passage and leave the rest of the document as it is."
}

// ProposeEditTool is propose_edit: a passage of the co-authored document and
// its replacement, put in the conversation as a text_proposal card that opens
// a review of that passage. Nothing changes until the person applies it, and
// a passage edited since is not written over (the client finds find again at
// Apply). An empty find proposes the whole document.
func ProposeEditTool(pub *Publisher) core.AgentToolDef {
	return core.AgentToolDef{
		Tool: core.Tool{
			Name: "propose_edit",
			Description: "Propose a change to the document the person is writing. find is the exact text being replaced, " +
				"copied from the document (the selected passage, or part of it); replace is what goes there. " +
				"Leave find empty only to replace the whole document. The person sees a diff of that passage and " +
				"applies it or not: nothing changes until they do.",
			Parameters: map[string]core.ToolParam{
				"find":    {Type: "string", Description: "The exact text being replaced, copied from the document. Empty: the whole document."},
				"replace": {Type: "string", Description: "What goes in its place. Empty with a find removes that text."},
				"summary": {Type: "string", Description: "One line: what the change does."},
			},
			Required: []string{"replace", "summary"},
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			find, _ := args["find"].(string)
			replace, _ := args["replace"].(string)
			summary, _ := args["summary"].(string)
			if find == "" && strings.TrimSpace(replace) == "" {
				return "", fmt.Errorf("nothing proposed: give the new text in replace, and in find the text it replaces")
			}
			if find != "" && find == replace {
				return "", fmt.Errorf("find and replace are the same text: nothing would change")
			}
			pub.Publish(Block{
				Type: "text_proposal", ID: core.UUIDv4(),
				Text: strings.TrimSpace(summary),
				Data: map[string]string{"find": find, "replace": replace},
			})
			return "Proposed. The person sees a diff of that passage and applies it or not. Do not repeat the new text in your reply: say briefly what changed.", nil
		},
	}
}
