// Files a workbench's records keep (core/appassets/record_files.go): what
// the app's agent made for the open record and kept on it with keep_file,
// such as each rendered take of a song. The record lists them in the field its
// workbench names (files_field), the viewer plays them from file?id=&name=,
// and the record's deletion takes them with it.
//
// The list is the server's: a save of the record (the whole-record save a
// form makes, or the editor's merge) keeps the list as stored, whatever it
// sends, so a form that never heard of the field cannot drop it and a page
// cannot point it at files that are not this record's.

package customapps

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appassets"
	"github.com/cmcoffee/gohort/core/filenotes"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// recordWriteMu holds one read-modify-write of a record at a time: a file
// kept by the agent mid-turn and the person's Save landing together would
// otherwise each write the record the other had not seen.
var recordWriteMu sync.Mutex

// workbenchFiles is the field the app's workbench records keep files in, what
// the page calls them, and what one record is called; field is "" when its
// records keep none.
func workbenchFiles(spec AppSpec) (field, label, noun string) {
	for _, s := range workbenchSections(spec) {
		f, _ := s["files_field"].(string)
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		label, _ = s["files_label"].(string)
		noun, _ = s["item_noun"].(string)
		return f, firstNonEmpty(strings.TrimSpace(label), "Files"), firstNonEmpty(strings.TrimSpace(noun), "document")
	}
	return "", "", ""
}

// firstNonEmpty is the first of vals that is not blank.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// recordFiles reads the kept files a record lists in field.
func recordFiles(rec map[string]any, field string) []appassets.RecordFile {
	raw, err := json.Marshal(rec[field])
	if err != nil {
		return nil
	}
	var out []appassets.RecordFile
	_ = json.Unmarshal(raw, &out)
	return out
}

// filesValue is a files list as a record holds it: plain maps, which the
// record store encodes (it cannot encode a Go type inside a record).
func filesValue(files []appassets.RecordFile) []any {
	out := make([]any, 0, len(files))
	for _, f := range files {
		out = append(out, map[string]any{"name": f.Name, "title": f.Title, "type": f.Type, "size": f.Size, "added": f.Added})
	}
	return out
}

// keepStoredFiles puts the files list the stored record has back on rec,
// which a save is about to write over it.
func keepStoredFiles(spec AppSpec, stored, rec map[string]any) {
	field, _, _ := workbenchFiles(spec)
	if field == "" {
		return
	}
	if v, ok := stored[field]; ok {
		rec[field] = v
	} else {
		delete(rec, field)
	}
}

// keepFileTool is the agent's way to put a file it made on the open record:
// a copy in the record's folder, listed on the record, where the person
// plays or opens it beside the document. openID names the open record when
// the call runs.
func (T *CustomApps) keepFileTool(udb Database, spec AppSpec, owner, user string, openID func() string) AgentToolDef {
	field, label, noun := workbenchFiles(spec)
	return AgentToolDef{
		Tool: Tool{
			Name: "keep_file",
			Description: fmt.Sprintf("Keep a file you made on the %s the person has open, under its %s, where they play or open it beside the %s: a rendered song, a picture, a recording. "+
				"Pass the file's name in your working folder and a title that says which one it is, so two takes can be told apart. "+
				"Keep it before any attach that cleans the file up. This does not send it in the conversation: the person finds it under the %s.", noun, label, noun, noun),
			Parameters: map[string]ToolParam{
				"file":  {Type: "string", Description: "The file's name (or path) in your working folder."},
				"title": {Type: "string", Description: "Which one it is, in a few words. Defaults to the title the file was made with."},
			},
			Required: []string{"file"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			file := strings.TrimSpace(fmt.Sprint(args["file"]))
			if file == "" || file == "<nil>" {
				return "", fmt.Errorf("file is required: the name of the file in your working folder")
			}
			id := strings.TrimSpace(openID())
			if id == "" {
				return "", fmt.Errorf("no %s is open: ask the person to open one first", noun)
			}
			sess := ToolSessionFromContext(ctx)
			if sess == nil {
				return "", fmt.Errorf("keep_file needs a working folder, and this conversation has none")
			}
			if _, err := EnsureSessionWorkspace(sess); err != nil {
				return "", fmt.Errorf("working folder unavailable: %w", err)
			}
			abs, err := ResolveWorkspacePath(sess.WorkspaceDir, file)
			if err != nil {
				return "", err
			}
			title, _ := args["title"].(string)
			if strings.TrimSpace(title) == "" {
				if n, ok := filenotes.Get(abs); ok {
					title = n.Title
				}
			}
			recordWriteMu.Lock()
			defer recordWriteMu.Unlock()
			tbl := recTable(spec.Slug)
			var rec map[string]any
			if !udb.Get(tbl, id, &rec) || rec == nil {
				return "", fmt.Errorf("the open %s could not be loaded (it may have been deleted)", noun)
			}
			kept, err := appassets.KeepRecordFile(owner, spec.Slug, user, id, abs, title)
			if err != nil {
				return "", err
			}
			rec[field] = filesValue(append(recordFiles(rec, field), kept))
			udb.Set(tbl, id, rec)
			noteChange(recordsChangeKey(owner, spec.Slug, user))
			name, _ := rec[firstNonEmpty(workbenchLabelField(spec), "title")].(string)
			if strings.TrimSpace(name) == "" {
				name = "the " + noun
			}
			return fmt.Sprintf("Kept %q on %q under its %s (%d now). The person can play or open it there; it was not sent in this conversation.",
				kept.Title, name, label, len(recordFiles(rec, field))), nil
		},
	}
}

// workbenchLabelField is the field the workbench names its records by.
func workbenchLabelField(spec AppSpec) string {
	for _, s := range workbenchSections(spec) {
		if f, _ := s["item_label"].(string); strings.TrimSpace(f) != "" {
			return strings.TrimSpace(f)
		}
	}
	return ""
}

// handleRecordFile is file?id=&name=: GET plays or opens one file the record
// keeps, DELETE removes it from the record and the disk. Only a file the
// record lists is served, from the folder of the person asking.
func (T *CustomApps) handleRecordFile(w http.ResponseWriter, r *http.Request, appdb Database, spec AppSpec, owner, user string) {
	field, _, _ := workbenchFiles(spec)
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if field == "" || id == "" || name == "" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
	case http.MethodDelete:
		T.removeRecordFile(w, r, appdb, spec, owner, user, field, id, name)
		return
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var rec map[string]any
	if !appdb.Get(recTable(spec.Slug), id, &rec) || !listsFile(rec, field, name) {
		http.NotFound(w, r)
		return
	}
	path, ct, err := appassets.RecordFilePath(owner, spec.Slug, user, id, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	// Opened directly it is a page of this origin: sandboxed, like an asset.
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self'; media-src 'self'; object-src 'self'; style-src 'unsafe-inline'")
	// ServeContent answers range requests, which is how an audio player
	// seeks.
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// removeRecordFile takes one kept file off the record and the disk.
func (T *CustomApps) removeRecordFile(w http.ResponseWriter, r *http.Request, appdb Database, spec AppSpec, owner, user, field, id, name string) {
	recordWriteMu.Lock()
	defer recordWriteMu.Unlock()
	tbl := recTable(spec.Slug)
	var rec map[string]any
	if !appdb.Get(tbl, id, &rec) || !listsFile(rec, field, name) {
		http.NotFound(w, r)
		return
	}
	var left []appassets.RecordFile
	for _, f := range recordFiles(rec, field) {
		if f.Name != name {
			left = append(left, f)
		}
	}
	if err := appassets.DeleteRecordFile(owner, spec.Slug, user, id, name); err != nil && !os.IsNotExist(err) {
		jsonapi.Fail(w, r, err)
		return
	}
	rec[field] = filesValue(left)
	appdb.Set(tbl, id, rec)
	noteChange(recordsChangeKey(owner, spec.Slug, user))
	jsonapi.Write(w, map[string]any{"removed": name})
}

// listsFile reports whether the record lists a kept file by name.
func listsFile(rec map[string]any, field, name string) bool {
	for _, f := range recordFiles(rec, field) {
		if f.Name == name {
			return true
		}
	}
	return false
}
