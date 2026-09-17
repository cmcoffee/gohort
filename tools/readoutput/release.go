// release_output — letting go of a result that has served its purpose.
//
// read_output gave the agent a way to finish reading something too big to
// return whole. It did not give it a way to stop carrying what it had read.
// Trimming the conversation was the framework's decision alone, made on
// size and age, by a compactor that cannot know which of two results the
// agent is still working from.
//
// This is the other half. The agent names a capture it is done with, and
// what it read drops out of the conversation, leaving the handle behind.
// Nothing is destroyed — the capture is kept exactly as it was, and
// read_output on the same id brings back any part of it.
package readoutput

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func init() { RegisterChatTool(new(ReleaseOutputTool)) }

type ReleaseOutputTool struct{}

func (t *ReleaseOutputTool) Name() string { return ReleaseOutputToolName }

// IsFrameworkTool: the same round-shape plumbing read_output is. Nobody
// would grant "may let go of a result it already has" as a capability.
func (t *ReleaseOutputTool) IsFrameworkTool() bool { return true }

func (t *ReleaseOutputTool) Desc() string {
	return "Drop a large result you are finished with out of this conversation, to make room for the work you have left. Pass the output_id of a truncated reply you have already taken what you need from: every copy of it here becomes a one-line note. Nothing is lost — the capture is kept, and read_output on the same id brings back any part of it, so this is reversible. Use it after a long capture has served its purpose; there is no reason to keep carrying it."
}

// Caps: none. Releasing reads nothing and reaches nothing — it changes only
// what this conversation is still holding.
func (t *ReleaseOutputTool) Caps() []Capability { return nil }

func (t *ReleaseOutputTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"output_id": {Type: "string", Description: "The output_id of the capture you are done with, as the truncated reply printed it. Several may be given, separated by commas."},
	}
}

// Run validates and reports. The conversation itself is rewritten by the
// agent loop, which acts on this call by name — a tool is handed its
// arguments and its session, never the history it is part of.
func (t *ReleaseOutputTool) Run(args map[string]any) (string, error) {
	ids := ReleaseIDsFromArgs(args)
	if len(ids) == 0 {
		return "", fmt.Errorf("output_id is required — it is the id printed at the end of the truncated reply you are finished with")
	}
	var live, gone []string
	for _, id := range ids {
		if SpilledOutputExists(id) {
			live = append(live, id)
			continue
		}
		gone = append(gone, id)
	}
	// Every id was a typo or an expired capture. Refusing is the honest
	// answer: nothing was released, and reporting otherwise would leave the
	// agent believing it had made room it never made.
	if len(live) == 0 {
		return "", fmt.Errorf("no capture is kept under %s — check the id against the reply you are continuing, or it may have expired", strings.Join(gone, ", "))
	}
	msg := fmt.Sprintf("Released %s. What you read from %s is now a one-line note in this conversation, naming the id; the capture itself is untouched, so read_output on that id brings back any part of it whenever you need it again.",
		plural(live), oneOrThem(live))
	if len(gone) > 0 {
		msg += fmt.Sprintf(" Not released: %s — no capture is kept under %s (wrong id, or expired).", strings.Join(gone, ", "), theyIt(gone))
	}
	return msg, nil
}

// plural, oneOrThem and theyIt keep the reply reading like a sentence
// whether one id was given or four. The tool is called on the turn an agent
// is already short of room; a reply it has to parse is a poor use of it.
func plural(ids []string) string {
	if len(ids) == 1 {
		return "output id " + ids[0]
	}
	return "output ids " + strings.Join(ids, ", ")
}

func oneOrThem(ids []string) string {
	if len(ids) == 1 {
		return "it"
	}
	return "them"
}

func theyIt(ids []string) string {
	if len(ids) == 1 {
		return "it"
	}
	return "them"
}
