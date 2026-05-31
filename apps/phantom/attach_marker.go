package phantom

import (
	"regexp"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/workspace"
)

// attachMarkerRe matches a single delivery marker the LLM emits to ship
// a file from its session workspace to the user:
//
//	[ATTACH: filename.ext]
//	[ATTACH: filename.ext, cleanup=true]
//
// Capture 1 is the workspace-relative filename; capture 2 is the
// optional cleanup flag ("true" or "false"). The filename token stops
// at the first comma or closing bracket so multiple markers in one
// reply each parse cleanly.
var attachMarkerRe = regexp.MustCompile(`\[ATTACH:\s*([^\],]+?)(?:\s*,\s*cleanup\s*=\s*(true|false))?\s*\]`)

// applyAttachMarkers consumes every [ATTACH: ...] marker in reply: each
// match calls workspace.AttachWorkspaceFile (the same primitive the
// workspace(action="attach") tool uses) to queue the file onto sess for
// delivery, then the marker is removed from the visible text. Failures
// (typo'd filename, missing file) are logged but never block the rest
// of the reply — partial delivery beats no reply.
func applyAttachMarkers(sess *ToolSession, reply string) string {
	if sess == nil || reply == "" {
		return reply
	}
	matches := attachMarkerRe.FindAllStringSubmatchIndex(reply, -1)
	if len(matches) == 0 {
		return reply
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		// Copy text up to this marker.
		b.WriteString(reply[last:m[0]])
		last = m[1]
		name := strings.TrimSpace(reply[m[2]:m[3]])
		cleanup := false
		if m[4] >= 0 && m[5] > m[4] {
			cleanup = strings.EqualFold(reply[m[4]:m[5]], "true")
		}
		if name == "" {
			continue
		}
		if summary, err := workspace.AttachWorkspaceFile(sess, name, "", cleanup); err != nil {
			Log("[phantom] attach marker %q failed: %v", name, err)
		} else {
			Debug("[phantom] attach marker delivered: %s", summary)
		}
	}
	// Tail after the last marker.
	b.WriteString(reply[last:])
	// Markers commonly sit on their own line at the end of the reply;
	// collapse runs of blank lines they leave behind so the visible text
	// doesn't end in a void.
	return strings.TrimSpace(blankRunRe.ReplaceAllString(b.String(), "\n\n"))
}

// blankRunRe collapses three-or-more consecutive newlines (left over
// after stripping markers) down to a paragraph break.
var blankRunRe = regexp.MustCompile(`\n{3,}`)

// workspaceCallProseRe matches a literal-text mimic of the
// workspace(action="...") tool call that some models echo verbatim from
// the tool's prose documentation, with or without an enclosing pair of
// square brackets:
//
//	[workspace(action="attach", path="x.jpg", cleanup=true)]
//	workspace(action="attach", path="x.jpg")
//
// The real tool call (if any) already fired through the structured
// path; this regex is defensive cleanup so the literal text doesn't
// reach the user even if the model emits it instead of, or alongside,
// the [ATTACH: ...] marker.
var workspaceCallProseRe = regexp.MustCompile(`\[?\s*workspace\(\s*action\s*=\s*"[a-zA-Z_]+"[^)]*\)\s*\]?`)

// stripWorkspaceCallProse removes any literal workspace(action="...")
// echoes from the reply, then collapses adjacent blank lines that the
// strip may have left behind.
func stripWorkspaceCallProse(reply string) string {
	if reply == "" {
		return reply
	}
	if !workspaceCallProseRe.MatchString(reply) {
		return reply
	}
	out := workspaceCallProseRe.ReplaceAllString(reply, "")
	return strings.TrimSpace(blankRunRe.ReplaceAllString(out, "\n\n"))
}
