package orchestrate

// The per-type Import buttons (agents, pipelines, machines) and the unified
// bundle format grew up apart: each button took only its own bare recipe, and a
// bundle exported from the admin page, or a colleague's "export with
// dependencies", was refused at every one of them. These doors now take
// either. A bare recipe keeps its door's own handling (a same-named import
// makes a copy); a bundle goes through the user-scoped bundle importer, which
// lands every artifact the person may import inert and reports the rest.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// maxImportBytes bounds a pasted or uploaded recipe or bundle. A bundle can
// carry a knowledge collection's text, so it matches the admin importer's cap
// rather than a single recipe's size.
const maxImportBytes = 64 << 20

// readImportBody reads an import request body under maxImportBytes, answering
// 413 itself when the body is larger. ok=false means a response was written.
func readImportBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxImportBytes+1))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return nil, false
	}
	if len(body) > maxImportBytes {
		http.Error(w, fmt.Sprintf("that file is larger than the %d MB an import accepts", maxImportBytes>>20), http.StatusRequestEntityTooLarge)
		return nil, false
	}
	return body, true
}

// importBundleAtDoor handles a bundle posted to the import door for typ. It
// returns false, having written nothing, when body is not a bundle envelope,
// so the door carries on with its bare-recipe path.
//
// A door answers with the record it imported (the page navigates to it), so a
// bundle must hold at least one artifact of the door's type. One that holds
// none is refused BEFORE anything imports: landing a stranger's tools and
// skills from a button labelled "Import pipeline" with nothing to show for it
// would be the wrong surprise.
//
// load finds the imported record by name in the user's store.
func importBundleAtDoor(w http.ResponseWriter, user string, body []byte, typ string, load func(name string) (any, bool)) bool {
	if !IsArtifactEnvelope(body) {
		return false
	}
	bundle, err := ParseArtifactBundle(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return true
	}
	var holds []string
	has := false
	for _, a := range bundle.Artifacts {
		if strings.TrimSpace(a.Type) == typ {
			has = true
		}
		holds = append(holds, strings.TrimSpace(a.Type)+" "+strings.TrimSpace(a.Name))
	}
	if !has {
		http.Error(w, fmt.Sprintf("this bundle holds no %s, so nothing was imported. It contains: %s", typ, strings.Join(holds, ", ")), http.StatusBadRequest)
		return true
	}
	res, err := ImportArtifactBundleAsUser(RootDB, body, user)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return true
	}
	Log("[orchestrate.import] user=%q imported a bundle at the %s door: %d imported, %d skipped", user, typ, res.Imported, res.Skipped)
	for _, o := range res.Outcomes {
		if o.Type != typ || o.Status != "imported" {
			continue
		}
		rec, ok := load(o.Name)
		if !ok {
			continue
		}
		// The record the page navigates to, plus what happened to the rest of
		// the bundle, in the FormPanel message convention.
		raw, _ := json.Marshal(rec)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		if out == nil {
			out = map[string]any{}
		}
		out["message"] = res.Summary()
		if len(res.Warnings) > 0 {
			out["warnings"] = res.Warnings
		}
		out["outcomes"] = res.Outcomes
		if len(res.Checklist) > 0 {
			out["checklist"] = res.Checklist
		}
		writeJSON(w, out)
		return true
	}
	// The bundle's own item of this type did not land (a same-named one
	// exists, say). Other items may have: say exactly what happened.
	http.Error(w, "the "+typ+" in this bundle was not imported. "+res.Summary()+importSkipDetail(res, typ), http.StatusConflict)
	return true
}

// importSkipDetail names why each item of typ was skipped.
func importSkipDetail(res ArtifactImportResult, typ string) string {
	var b strings.Builder
	for _, o := range res.Outcomes {
		if o.Type == typ && o.Status == "skipped" {
			fmt.Fprintf(&b, "\n%s %q: %s", o.Type, o.Name, o.Detail)
		}
	}
	return b.String()
}
