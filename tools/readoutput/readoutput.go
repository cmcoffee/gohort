// read_output — the way back to a result that was too big to return whole.
//
// A tool reply is capped so one call cannot fill the context. Two tools page
// their own overflow with their own parameters (fetch_knowledge_doc by
// offset, run_command by output_id), but every other capped reply simply
// ended in "[truncated]" and the rest was unreachable: a sub-agent's findings,
// a delegated run's report, an API response body. The agent could see that
// something was cut and had no move except to ask for the work again.
//
// Rather than teach five tools their own paging arguments, anything that
// overflows keeps its capture (core.SpillOutput) and names this tool. One
// tool, one id, and the same offset/grep the other two already speak.
package readoutput

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func init() { RegisterChatTool(new(ReadOutputTool)) }

type ReadOutputTool struct{}

func (t *ReadOutputTool) Name() string { return "read_output" }

// IsFrameworkTool: this is round-shape plumbing, not a capability anybody
// would group or grant — it exists only to finish reading something a tool
// already returned.
func (t *ReadOutputTool) IsFrameworkTool() bool { return true }

func (t *ReadOutputTool) Desc() string {
	return "Read the rest of a result that was truncated. When a tool's reply ends with an output_id, pass it here to continue: offset reads the next window, grep returns only the matching lines (each with its line number and @offset, so offset then reads around a hit). Nothing is re-run — the full result was kept when it overflowed."
}

func (t *ReadOutputTool) Caps() []Capability { return []Capability{CapRead} }

func (t *ReadOutputTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"output_id": {Type: "string", Description: "The output_id from a truncated reply."},
		"offset":    {Type: "number", Description: "Character offset to read from — the value the truncated reply told you to pass, or the @offset on a grep hit. 0 (default) starts at the beginning."},
		"grep":      {Type: "string", Description: "Return only the lines matching this pattern (case-insensitive; a regular expression when it compiles as one, else a substring) instead of a window. Use this to find one thing in a long result rather than paging through it."},
		"context":   {Type: "number", Description: "With grep: lines of context before and after each match (default 0)."},
		"max_chars": {Type: "number", Description: "Window size (default 10000, ceiling 30000)."},
	}
}

// readOutputDefaultMax and readOutputCap mirror the other paged surfaces, so
// a window is the same size whichever tool the agent came from.
const (
	readOutputDefaultMax = 10000
	readOutputCap        = 30000
)

func (t *ReadOutputTool) Run(args map[string]any) (string, error) {
	id := strings.TrimSpace(fmt.Sprint(args["output_id"]))
	if id == "" || id == "<nil>" {
		return "", fmt.Errorf("output_id is required — it is printed at the end of the truncated reply you are continuing")
	}
	offset := 0
	if v, ok := args["offset"].(float64); ok && v > 0 {
		offset = int(v)
	}
	max := readOutputDefaultMax
	if v, ok := args["max_chars"].(float64); ok && v > 0 {
		max = int(v)
		if max > readOutputCap {
			max = readOutputCap
		}
	}
	context := 0
	if v, ok := args["context"].(float64); ok && v > 0 {
		context = int(v)
	}
	grep, _ := args["grep"].(string)
	return OutputPage{
		ID: id, Offset: offset, Max: max, Tool: "read_output",
		Grep: grep, Context: context,
	}.Read()
}
