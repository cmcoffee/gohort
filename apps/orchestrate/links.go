// Where this app's links point.
//
// One rule: a page at the app root (the chat page, /pipeline, /machine) may
// name its own endpoints relative ("api/..."), as every app does. Anything
// else is built from the app's mount path:
//
//   - a page below the root (the /agent/{id} family), where "api/..." would
//     resolve under the page's own folder;
//   - a section another app renders (the machines and pipelines lists on
//     Extensions, the console sections on Admin), where "api/..." would
//     resolve under THAT app;
//   - a link from one page to another, and every redirect.
//
// Inside a method on the app, T.WebPrefix() + "/..." is that. Elsewhere it is
// orchPath. Neither is "../api/...", which works on exactly one depth of page,
// nor a written-out "/orchestrate/...", which is wrong whenever the app is
// mounted anywhere else.

package orchestrate

import (
	"strconv"
	"strings"
)

// orchMount is the path the app is mounted at, recorded when its routes are
// registered. Until then it is the app's own WebPath, which is where it is
// mounted on every deployment.
var orchMount = "/orchestrate"

// orchPath is p within this app: "api/machines" is "<mount>/api/machines".
func orchPath(p string) string {
	return orchMount + "/" + strings.TrimPrefix(p, "/")
}

// orchJS is a script constant with its ORCH_BASE written in as the mount path,
// quoted: a constant cannot call orchPath, so its links read
// ORCH_BASE + '/api/...' and are completed where the script is handed out.
func orchJS(js string) string {
	return strings.ReplaceAll(js, "ORCH_BASE", strconv.Quote(orchMount))
}
