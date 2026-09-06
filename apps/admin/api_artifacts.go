package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// registerArtifactsRoutes wires the artifacts API under the admin sub-mux.
func (a *AdminApp) registerArtifactsRoutes(sub *http.ServeMux) {
	// Connector export — download a portable, secret-free JSON pack. ?name=<n>
	// exports one connector; omit name to export ALL as one bundle. Auth
	// references (credential names) travel; secrets never do. Content-Disposition
	// makes the browser download it.
	sub.HandleFunc("/api/connectors/export", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		var names []string
		if name != "" {
			names = []string{name}
		}
		pack, err := ExportConnectorPack(RootDB, names...)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		filename := "connectors.connector.json"
		if name != "" {
			filename = name + ".connector.json"
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(pack)
	})

	// Connector import — accept a pack (the JSON produced by export) and
	// reconstitute its connectors as new DRAFTS owned by the admin. Governance
	// re-applies: remote_mcp / desktop_* land unapproved; an existing name is
	// skipped, never overwritten. Returns the import summary as JSON.
	sub.HandleFunc("/api/connectors/import", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Accept either a raw pack body or {"pack":"<json string>"} from a form.
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		data := bytes.TrimSpace(body)
		var wrap struct {
			Pack string `json:"pack"`
		}
		if json.Unmarshal(data, &wrap) == nil && strings.TrimSpace(wrap.Pack) != "" {
			data = []byte(strings.TrimSpace(wrap.Pack))
		}
		res, err := ImportConnectorPack(RootDB, data, AuthCurrentUser(r))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
	})

	// Artifact export — the UNIFIED, cross-type download. Builds a
	// gohort.bundle/v1 carrying any registered artifact (connector, tool, …).
	// An individual export is just a one-item bundle:
	//   ?type=<t>&name=<n>[&owner=<u>]  → one artifact (owner scopes tools)
	//   ?all=<t1,t2>                    → every artifact of those types
	//   (no params)                     → everything
	// Auth references travel; secrets never do. Content-Disposition downloads it.
	sub.HandleFunc("/api/artifacts/export", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		typ := strings.TrimSpace(q.Get("type"))
		name := strings.TrimSpace(q.Get("name"))
		// Dependency closure is ON by default — a 1-item export carries the
		// credentials/tools it references so it installs cleanly elsewhere. The
		// UI's "Include dependencies" checkbox sends deps=0 to opt out (a bare
		// export of exactly the selection, for a target that already has them).
		includeDeps := true
		switch strings.ToLower(strings.TrimSpace(q.Get("deps"))) {
		case "0", "false", "no", "none", "off":
			includeDeps = false
		}
		exportSels := func(sels []ArtifactSel) (ArtifactBundle, error) {
			if includeDeps {
				return ExportArtifactBundle(RootDB, sels)
			}
			return ExportArtifactBundleShallow(RootDB, sels)
		}
		var (
			bundle   ArtifactBundle
			err      error
			filename = "gohort-bundle.json"
		)
		switch {
		case typ != "" && name != "":
			// Per-user types (tools, skills) need an owner; when the query
			// omits it, default to the requesting admin — the per-row export
			// buttons on surfaces that only list the requester's own pool
			// (Skills) don't have an owner field to send. Global types ignore
			// Owner entirely, so the default is inert for them.
			owner := strings.TrimSpace(q.Get("owner"))
			if owner == "" {
				owner = AuthCurrentUser(r)
			}
			bundle, err = exportSels([]ArtifactSel{{Type: typ, Name: name, Owner: owner}})
			filename = name + ".gohort.json"
		case strings.TrimSpace(q.Get("all")) != "":
			bundle, err = exportSels(ArtifactSelectionForTypes(RootDB, strings.Split(q.Get("all"), ",")...))
		default:
			bundle, err = exportSels(ArtifactSelectionForTypes(RootDB))
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(bundle)
	})

	// Artifact import — accept a gohort.bundle/v1 (or a legacy connector pack, or
	// a bare single artifact) and reconstitute every artifact as a DRAFT owned by
	// the importing admin: connectors land unapproved, tools land in the pending
	// pool. Nothing goes live without a separate approval. Returns the per-
	// artifact import summary as JSON. POST-only.
	sub.HandleFunc("/api/artifacts/import", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		data, err := readArtifactBundleBody(r)
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		res, err := ImportArtifactBundle(RootDB, data, AuthCurrentUser(r))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// message drives the form's post-submit note: counts plus any
		// unmet-dependency warnings, so an import that leaves a tool wired to a
		// missing credential says so instead of looking like a clean success.
		_ = json.NewEncoder(w).Encode(struct {
			ArtifactImportResult
			Message string `json:"message"`
		}{res, res.Summary()})
	})

	// Artifact import PREVIEW — the dry-run twin of /api/artifacts/import.
	// Same body shapes, same auth, writes NOTHING: returns what the bundle
	// carries, what would import vs skip, and any unmet references, so the
	// admin sees exactly what a bundle does before committing to it.
	sub.HandleFunc("/api/artifacts/preview", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		data, err := readArtifactBundleBody(r)
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		res, err := PreviewArtifactBundle(RootDB, data, AuthCurrentUser(r))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
	})

	// Catalog — a curated, in-tree set of ready-made artifact bundles (the
	// offline precursor to a marketplace). GET lists entries (metadata + what
	// each installs); POST ?action=install&id=<id> installs one through the
	// unified importer, so its artifacts land as DRAFTS for review — connectors
	// unapproved, tools pending, credentials inert. Same governance as a file
	// import; nothing a catalog install brings in goes live unreviewed.
	sub.HandleFunc("/api/catalog", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ListCatalog())
		case http.MethodPost:
			if r.URL.Query().Get("action") != "install" {
				http.Error(w, "action must be install", http.StatusBadRequest)
				return
			}
			id := strings.TrimSpace(r.URL.Query().Get("id"))
			if id == "" {
				http.Error(w, "missing id", http.StatusBadRequest)
				return
			}
			res, err := InstallCatalogEntry(RootDB, id, AuthCurrentUser(r))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			// Same message/warnings envelope as a file import — a catalog install
			// runs the same importer, so an unmet reference surfaces the same way
			// (a modal on the Install button rather than a form note).
			_ = json.NewEncoder(w).Encode(struct {
				ArtifactImportResult
				Message string `json:"message"`
			}{res, res.Summary()})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

}

// readArtifactBundleBody reads an artifact-bundle request body, accepting
// either the raw bundle JSON or the {"pack":"<json string>"} wrapper a form
// file-field posts. Shared by the import and preview endpoints so both accept
// exactly the same shapes. The 64 MB cap exists for collection bundles: they
// carry a corpus's chunk text (recipe-only bundles are kilobytes), and the
// {"pack"} wrapper's JSON-string escaping roughly doubles the wire size.
func readArtifactBundleBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<26))
	if err != nil {
		return nil, err
	}
	data := bytes.TrimSpace(body)
	var wrap struct {
		Pack string `json:"pack"`
	}
	if json.Unmarshal(data, &wrap) == nil && strings.TrimSpace(wrap.Pack) != "" {
		data = []byte(strings.TrimSpace(wrap.Pack))
	}
	return data, nil
}
