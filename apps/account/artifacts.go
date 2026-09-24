package account

// A person's own export and import. The bundle format and its import-as-draft
// governance existed only behind the admin console, so the one thing an
// ordinary user could not do with their own agents, pipelines, skills and
// collections was take them somewhere, back them up, or bring them back.
//
// Everything here is the user-scoped half of the core bundle API: export
// resolves in the requester's namespace and carries only kinds a user owns;
// import takes only those kinds and lands them inert. Deployment-wide records
// (connectors, credentials, source hooks) stay the administrator's.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// maxAccountImportBytes matches the admin importer: a bundle can carry a
// knowledge collection's text.
const maxAccountImportBytes = 64 << 20

// unsafeFilenameRE is everything a download filename should not carry into a
// Content-Disposition header.
var unsafeFilenameRE = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func downloadName(base string) string {
	base = strings.Trim(unsafeFilenameRE.ReplaceAllString(base, "-"), "-.")
	if base == "" {
		base = "gohort"
	}
	return base + ".gohort.json"
}

// handleArtifactExport downloads the requester's own artifacts as a bundle:
//
//	?type=<t>&name=<n>   one artifact, by name or id, with what it depends on
//	    &include=<t1,t2>   only these kinds of dependency (the export dialog's ticks)
//	    &deps=0            none at all: exactly the one artifact
//	    &plan=1            no download: JSON list of what it depends on
//	    &file=<label>      the download's filename, when name is an id
//	?all=1               everything the requester owns
func (T *Account) handleArtifactExport(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	typ := strings.TrimSpace(q.Get("type"))
	name := strings.TrimSpace(q.Get("name"))
	opts := UserExportOptions{IncludeDeps: true}
	switch strings.ToLower(strings.TrimSpace(q.Get("deps"))) {
	case "0", "false", "no", "none", "off":
		opts.IncludeDeps = false
	}
	if inc := strings.TrimSpace(q.Get("include")); inc != "" {
		opts.DepTypes = strings.Split(inc, ",")
	}

	var (
		sels     []ArtifactSel
		filename string
	)
	switch {
	case typ != "" && name != "":
		sels = []ArtifactSel{{Type: typ, Name: name}}
		label := strings.TrimSpace(q.Get("file"))
		if label == "" {
			label = name
		}
		filename = downloadName(label)
	case q.Get("all") != "":
		sels = ArtifactSelectionForOwner(RootDB, user)
		if len(sels) == 0 {
			http.Error(w, "you have nothing of your own to export yet", http.StatusNotFound)
			return
		}
		// The whole set is already its own closure; the walk only confirms it.
		filename = downloadName("gohort-" + user + "-" + time.Now().Format("2006-01-02"))
	default:
		http.Error(w, "name what to export (type and name), or all=1 for everything you own", http.StatusBadRequest)
		return
	}
	if q.Get("plan") != "" {
		deps, err := ArtifactExportPlanAsUser(RootDB, user, sels)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if deps == nil {
			deps = []ArtifactSel{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"dependencies": deps})
		return
	}
	bundle, err := ExportArtifactBundleAsUser(RootDB, user, sels, opts)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	Log("[account.artifacts] user=%q exported %d artifact(s)", user, len(bundle.Artifacts))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(bundle)
}

// readAccountImportBody reads a bundle posted raw or in the {"pack": "<text>"}
// shape the shared import flow sends, answering 413 itself when it is too big.
func readAccountImportBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAccountImportBytes+1))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return nil, false
	}
	if len(body) > maxAccountImportBytes {
		http.Error(w, fmt.Sprintf("that file is larger than the %d MB an import accepts", maxAccountImportBytes>>20), http.StatusRequestEntityTooLarge)
		return nil, false
	}
	return UnwrapArtifactUpload(body), true
}

// handleArtifactPreview is the dry run: what the file would bring in, what it
// would skip and why, and what it references that is not here. Writes nothing.
func (T *Account) handleArtifactPreview(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	body, ok := readAccountImportBody(w, r)
	if !ok {
		return
	}
	res, err := PreviewArtifactBundleAsUser(RootDB, body, user)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

// handleArtifactImport brings a bundle into the requester's own namespace.
// Every kind lands inert (agents private, tools pending review, skills and
// apps disabled, monitors paused); kinds only an administrator may import are
// reported as skipped.
func (T *Account) handleArtifactImport(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	body, ok := readAccountImportBody(w, r)
	if !ok {
		return
	}
	res, err := ImportArtifactBundleAsUser(RootDB, body, user)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	Log("[account.artifacts] user=%q imported a bundle: %d imported, %d skipped", user, res.Imported, res.Skipped)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		ArtifactImportResult
		Message string `json:"message"`
	}{res, res.Summary()})
}

// artifactsHead points the shared bundle client (core ArtifactClientJS) at
// this account's own endpoints.
func artifactsHead() string {
	return ui.NewHead().
		JS(ArtifactClientJS).
		ClientAction("account_export_all", `function(){
  window.gohortArtifacts.download('/account/api/artifacts/export?all=1');
}`).
		ClientAction("account_import", `function(){
  window.gohortArtifacts.importFlow({
    previewURL: '/account/api/artifacts/preview',
    importURL: '/account/api/artifacts/import',
    subtitle: 'Everything lands in your own account for review: agents private, tools waiting for approval, skills and apps switched off, monitors paused. A name you already have is skipped.'
  });
}`).
		Render()
}

// artifactsSection is the Account page's "Your data" block.
func artifactsSection() ui.Section {
	return ui.Section{
		Title:    "Your data",
		Subtitle: "Export what you have built, or bring in a file somebody exported.",
		Detail: "Export everything downloads your agents, pipelines, machines, tools, skills, knowledge collections, custom apps, monitors, schedules, Scribe documents and CodeWriter modes as one file, together with your agents' memory and your apps' data, since it is a backup of your account. No secrets are in it: credentials travel by name only, and connectors and API credentials are an administrator's to export. A single item's Export leaves memory and app data out unless you tick them.\n\n" +
			"Import shows what a file would bring in before anything happens. It accepts a full bundle or a single agent, pipeline or machine file. Everything lands in your account inert: agents private, tools waiting for an administrator's approval, skills and apps switched off, monitors paused.",
		Body: ui.Toolbar{Actions: []ui.ToolbarAction{
			{Label: "Export everything", Title: "Download everything you own as one file", Method: "client", URL: "account_export_all", Variant: "primary"},
			{Label: "Import…", Title: "Preview a file, then import it into your account", Method: "client", URL: "account_import"},
		}},
	}
}
