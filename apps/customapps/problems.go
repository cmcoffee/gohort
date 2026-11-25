package customapps

// What failed around an app's scripts in real use, filed on its run record
// (tools/appscript/runlog.go) beside the scripts' own runs, so Builder and
// Anvil, asked why an app does not work, read what actually went wrong: an
// error the app's page hit in someone's browser, and an ask its agent could
// not answer. Only the live app's: a preview's and a check's failures are
// their own reports.

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/tools/appscript"
)

// liveUse reports whether r is someone using the live app, not a preview or
// a page check.
func liveUse(r *http.Request, user, slug string) bool {
	if _, isCheck := checkScratchOf(r); isCheck {
		return false
	}
	_, inPreview := previewOf(r, user, slug)
	return !inPreview
}

// recordAskFailure files an ask the app's agent could not answer. live is
// liveUse of the request that asked, read while it was open: the ask runs as a
// job and may end after it.
func recordAskFailure(live bool, owner, user, slug string, err error) {
	if err == nil || !live {
		return
	}
	appscript.RecordProblem(owner, slug, appscript.AppRun{Kind: "ask", Name: "ask", Trigger: "page", Other: user != owner, Error: err.Error()})
}

// pageProblemsPerHour bounds what one person's browser files on one app, so
// a page that throws in a loop, or someone posting by hand, cannot fill it.
const pageProblemsPerHour = 30

var pageProblems = struct {
	sync.Mutex
	seen map[string][]time.Time
}{seen: map[string][]time.Time{}}

// pageProblemAllowed counts one report against key's hour.
func pageProblemAllowed(key string) bool {
	pageProblems.Lock()
	defer pageProblems.Unlock()
	cut := time.Now().Add(-time.Hour)
	kept := pageProblems.seen[key][:0]
	for _, t := range pageProblems.seen[key] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= pageProblemsPerHour {
		pageProblems.seen[key] = kept
		return false
	}
	pageProblems.seen[key] = append(kept, time.Now())
	return true
}

// handlePageProblem files an error the app's page hit in the viewer's
// browser: POST {error, where} from window.app's listener. Answered {}
// whatever came of it, since the page has nothing to do with the answer; not
// an empty 204, which the frame's fetch cannot hand back to the page.
func (T *CustomApps) handlePageProblem(w http.ResponseWriter, r *http.Request, owner, user string, spec AppSpec) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer jsonapi.Write(w, map[string]any{})
	if !liveUse(r, user, spec.Slug) || !pageProblemAllowed(owner+"/"+spec.Slug+"/"+user) {
		return
	}
	var in struct {
		Error string `json:"error"`
		Where string `json:"where"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&in) != nil {
		return
	}
	msg := strings.TrimSpace(in.Error)
	if msg == "" {
		return
	}
	if where := strings.TrimSpace(in.Where); where != "" {
		msg += " (" + where + ")"
	}
	appscript.RecordProblem(owner, spec.Slug, appscript.AppRun{Kind: "page", Name: "page", Trigger: "page", Other: user != owner, Error: msg})
}
