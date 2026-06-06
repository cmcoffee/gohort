package phantom

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

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

// attachFailure carries one failed [ATTACH:] marker through to the
// caller so phantom's reply path can feed the failure back to the
// LLM on the next turn (otherwise the model thinks its attach went
// through and may falsely claim "I sent you the video"). Reason is
// the error string from workspace.AttachWorkspaceFile.
type attachFailure struct {
	Name   string
	Reason string
}

// applyAttachMarkers consumes every [ATTACH: ...] marker in reply: each
// match calls workspace.AttachWorkspaceFile (the same primitive the
// workspace(action="attach") tool uses) to queue the file onto sess for
// delivery, then the marker is removed from the visible text. Failures
// (typo'd filename, missing file, file too large for iMessage) are
// logged AND returned so the caller can feed them back to the LLM via
// the next turn's history — partial delivery beats no reply, but the
// LLM needs to know what didn't make it so it doesn't lie on the
// follow-up turn or skip a recovery (e.g. transcode the video).
func applyAttachMarkers(sess *ToolSession, reply string) (string, []attachFailure) {
	if sess == nil || reply == "" {
		return reply, nil
	}
	matches := attachMarkerRe.FindAllStringSubmatchIndex(reply, -1)
	if len(matches) == 0 {
		return reply, nil
	}
	var failures []attachFailure
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
		// Tolerate the model inventing a filename (it often emits a semantic
		// name like "pride-celebration.jpg" instead of the find-<id>.jpg path
		// find_image actually returned): if the named file is missing, fall
		// back to the freshest workspace image so the picture delivers instead
		// of silently vanishing. Display name stays the model's original.
		target := resolveAttachName(sess, name)
		if summary, err := workspace.AttachWorkspaceFile(sess, target, name, cleanup); err != nil {
			Log("[phantom] attach marker %q failed: %v", name, err)
			failures = append(failures, attachFailure{Name: name, Reason: err.Error()})
		} else {
			Debug("[phantom] attach marker delivered: %s", summary)
		}
	}
	// Tail after the last marker.
	b.WriteString(reply[last:])
	// Markers commonly sit on their own line at the end of the reply;
	// collapse runs of blank lines they leave behind so the visible text
	// doesn't end in a void.
	clean := strings.TrimSpace(blankRunRe.ReplaceAllString(b.String(), "\n\n"))
	return clean, failures
}

// resolveAttachName tolerates the model inventing a filename. If `name`
// exists in the session workspace it's returned unchanged; otherwise, when a
// fresh image was saved there recently (find_image / generate_image write
// find-<id>.jpg, but the model frequently emits a semantic name), the
// most-recently-modified image is returned so the attach still delivers.
// Falls back to the original name (which then fails normally + surfaces to
// the LLM) when there's no plausible recent image.
func resolveAttachName(sess *ToolSession, name string) string {
	ws, err := EnsureSessionWorkspace(sess)
	if err != nil {
		return name
	}
	if abs, err := ResolveWorkspacePath(ws, name); err == nil {
		if _, err := os.Stat(abs); err == nil {
			return name // the named file exists — use it as-is
		}
	}
	entries, err := os.ReadDir(ws)
	if err != nil {
		return name
	}
	var best string
	var bestMod time.Time
	for _, e := range entries {
		if e.IsDir() || !isImageFilename(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if best == "" || info.ModTime().After(bestMod) {
			best, bestMod = e.Name(), info.ModTime()
		}
	}
	// Only substitute a genuinely fresh image — an old leftover isn't what
	// the model meant.
	if best != "" && time.Since(bestMod) < 10*time.Minute {
		Log("[phantom] attach %q not found — substituting freshest workspace image %q", name, best)
		return best
	}
	return name
}

func isImageFilename(n string) bool {
	switch strings.ToLower(filepath.Ext(n)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp":
		return true
	}
	return false
}

func isVideoFilename(n string) bool {
	switch strings.ToLower(filepath.Ext(n)) {
	case ".mp4", ".mov", ".m4v", ".webm", ".mkv", ".avi":
		return true
	}
	return false
}

// dropCoveredAttachFailures removes [ATTACH:] marker failures whose media
// is already being delivered to the user through the session this turn.
// Image tools (generate_image / find_image) auto-attach their output to
// the session; the model frequently ALSO emits an [ATTACH:] marker that
// points at a stale, semantic, or already-cleaned-up filename. That marker
// "fails" even though the picture is going out via the session — and the
// failure then drives an honest-recovery rewrite, so the user receives the
// image AND a contradictory "it didn't attach properly" apology. A failure
// is only real if the user won't otherwise receive that kind of media, so
// suppress image failures when an image is already queued and video
// failures when a video is. Unknown / non-media names are always kept.
func dropCoveredAttachFailures(failures []attachFailure, haveImages, haveVideos bool) []attachFailure {
	if len(failures) == 0 {
		return failures
	}
	var kept []attachFailure
	for _, f := range failures {
		if isImageFilename(f.Name) && haveImages {
			Log("[phantom] attach marker %q failed but an image is already being delivered this turn — treating as covered", f.Name)
			continue
		}
		if isVideoFilename(f.Name) && haveVideos {
			Log("[phantom] attach marker %q failed but a video is already being delivered this turn — treating as covered", f.Name)
			continue
		}
		kept = append(kept, f)
	}
	return kept
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
