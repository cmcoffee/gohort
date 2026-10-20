package orchestrate

import (
	"encoding/json"
	"path"
	"strings"
)

// Which paths a live artifact may read.
//
// An authored document is the model's, and the model is not trusted. The pane
// fetches each declared data_urls path with the VIEWER's cookies and hands the
// body to the document, which can carry it off the machine: a sandboxed frame
// is free to navigate itself, and no CSP closes that. So "any same-origin GET"
// meant a prompt injection could read anything the viewer can, up to
// /api/desktop/key, which mints the key to their desktop machine's tools.
//
// What a live dashboard is for is an app's data, and that is all it may name:
// a custom app's data source, its shared collection, or its records, each read
// as the viewer and admitted by the app's own access check. Checked where the
// call is made (a refusal the model can act on) and again on every way a
// stored block goes out (UIBlock.MarshalJSON), because a session saved before
// this check replays its old list straight to the pane.

const artifactDataPathsHelp = "data_urls may name a custom app's data only: /apps/<slug>/data/<source>, /apps/<slug>/shared/<collection>, or /apps/<slug>/records"

// artifactDataPathProblem says why a data_urls entry is refused, or "" when it
// is one of the shapes above.
func artifactDataPathProblem(p string) string {
	switch {
	case !strings.HasPrefix(p, "/apps/"):
		return artifactDataPathsHelp
	case strings.ContainsAny(p, "\\%?#") || path.Clean(p) != p:
		// No encodings, no query, no dot segments: the pane matches the
		// declared path exactly, so anything but its plain form is a trick.
		return "a data_urls entry is a plain path, with no query, encoding or dot segments"
	}
	seg := strings.Split(strings.TrimPrefix(p, "/apps/"), "/")
	if seg[0] == "" || strings.HasPrefix(seg[0], "_") {
		// "_app", "_apps", "_admin": the apps index's own routes, not an app.
		return artifactDataPathsHelp
	}
	switch {
	case len(seg) == 2 && seg[1] == "records":
		return ""
	case len(seg) == 3 && (seg[1] == "data" || seg[1] == "shared") && seg[2] != "":
		return ""
	}
	return artifactDataPathsHelp
}

// allowedArtifactDataURLs is in without the entries artifactDataPathProblem
// refuses.
func allowedArtifactDataURLs(in []string) []string {
	var out []string
	for _, p := range in {
		if artifactDataPathProblem(p) == "" {
			out = append(out, p)
		}
	}
	return out
}

// MarshalJSON sends a block with only the data paths a live artifact may
// still read. A session stored before the check carries whatever its model
// declared, and the client replays the list as given.
func (b UIBlock) MarshalJSON() ([]byte, error) {
	type plain UIBlock // no methods, so no recursion
	p := plain(b)
	p.DataURLs = allowedArtifactDataURLs(b.DataURLs)
	return json.Marshal(p)
}
