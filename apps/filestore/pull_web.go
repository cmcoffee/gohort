package filestore

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/bgrun"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// The two ways a pull is started: an admin's click on the destination store's
// panel, and an agent's tool call on a store it has attached. Both go through
// the one registry, so an item being pulled shows as running to both, and the
// second asker joins the first rather than starting a second copy.

// pullJobFor builds the job for an item into dest, or says why it cannot run.
func (T *FileStoreApp) pullJobFor(dest Store, item, user string, replace bool) (*pullJob, error) {
	src, ok := dest.pullSource(T.DB)
	if !ok {
		return nil, Error(dest.Name + " has no store to pull from: set one in its Settings (Pull from another store)")
	}
	return &pullJob{dest: dest, src: src, item: strings.TrimSpace(item), replace: replace, user: user, db: T.DB}, nil
}

// pullItems is the source store's files and folders as the panel lists them:
// what each is (a folder, ready or not, or a file and its size), and whether
// it is already here.
func (T *FileStoreApp) pullItems(dest Store) []map[string]any {
	src, ok := dest.pullSource(T.DB)
	if !ok {
		return []map[string]any{}
	}
	out := []map[string]any{}
	here := func(name string) string {
		if dir, err := SubRoot(dest.Path, name); err == nil {
			if newest, err := bundleNewest(dir); err == nil {
				return "in " + dest.Name + ", last added to " + newest.Format("Jan 2 15:04") + "; pulling again adds new files and updates changed ones"
			}
		}
		if info, err := os.Stat(filepath.Join(dest.Path, name)); err == nil && info.Mode().IsRegular() {
			return "in " + dest.Name + " since " + info.ModTime().Format("Jan 2 15:04") + "; pulling again replaces it"
		}
		return "not in " + dest.Name + " yet"
	}
	sub := dest.pullSubpath()
	if folders, err := ListFolders(src.Path); err == nil {
		for _, f := range folders {
			desc := fmt.Sprintf("folder, %d files, %s", f.Files, HumanSize(f.Bytes))
			if sub != "" {
				desc = "folder, not prepared yet: no " + sub + "/ inside"
				if dir, err := SubRoot(src.Path, f.Name); err == nil {
					if _, err := SubRoot(dir, sub); err == nil {
						desc = "folder, " + sub + "/ ready to copy"
					}
				}
			}
			out = append(out, map[string]any{"item": f.Name, "desc": desc, "history": here(f.Name)})
		}
	}
	for _, f := range topFiles(src.Path) {
		out = append(out, map[string]any{"item": f.Name(), "desc": "file, " + HumanSize(fileSize(f)), "history": here(f.Name())})
	}
	return out
}

// topFiles is the regular files at the top of a store, dot-files skipped.
func topFiles(root string) []os.DirEntry {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []os.DirEntry
	for _, e := range entries {
		if e.Type().IsRegular() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e)
		}
	}
	return out
}

func fileSize(e os.DirEntry) int64 {
	if info, err := e.Info(); err == nil {
		return info.Size()
	}
	return 0
}

// handlePull is the panel's half.
//
//	GET  /filestore/api/pull/items?slug=<dest>            the source's files and folders
//	POST /filestore/api/pull?slug=<dest>&item=<name>      start (a folder adds to a copy already here)
//	GET  /filestore/api/pull/progress?slug=<dest>&item=   {"progress"} or {"outcome"}
//	POST /filestore/api/pull/cancel?slug=<dest>&item=     stop it
//
// Admin-only, like the rest of the store's panel.
func (T *FileStoreApp) handlePull(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok || !adminOnly(w, r) {
		return
	}
	dest, ok := LoadStore(T.DB, r.URL.Query().Get("slug"))
	if !ok {
		http.Error(w, "no such store", http.StatusNotFound)
		return
	}
	item := strings.TrimSpace(r.URL.Query().Get("item"))
	switch {
	case strings.HasSuffix(r.URL.Path, "/items"):
		jsonapi.Write(w, T.pullItems(dest))
	case strings.HasSuffix(r.URL.Path, "/progress"):
		if run, ok := pulls.get(dest.Slug + "/" + item); ok {
			jsonapi.Write(w, run.status())
			return
		}
		jsonapi.Write(w, map[string]any{})
	case strings.HasSuffix(r.URL.Path, "/cancel"):
		if run, ok := pulls.get(dest.Slug + "/" + item); ok {
			run.cancel()
		}
		jsonapi.Write(w, map[string]any{"ok": true})
	default:
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		job, err := T.pullJobFor(dest, item, user, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		pulls.start(job)
		jsonapi.Write(w, map[string]any{"running": true, "message": "Copying " + item + " into " + dest.Name})
	}
}

// pullToolDef is the agent's half: pull_<store>, on a store with a pull link.
// It runs as a background-capable run, so a big case moves to the background
// and reports back, and Stop on the run stops the copy.
func (T *FileStoreApp) pullToolDef(sess *ToolSession, user string, dest Store) (AgentToolDef, bool) {
	// Pulling changes what the store holds: Full Access for the agent's
	// owner. The attachment's "allows changes" decides the rest (the tool
	// declares CapWrite).
	if !dest.FullAccessFor(user) {
		return AgentToolDef{}, false
	}
	src, ok := dest.pullSource(T.DB)
	if !ok {
		return AgentToolDef{}, false
	}
	sub := dest.pullSubpath()
	what := "a folder's " + sub + "/ part (what was prepared, not the original download)"
	if sub == "" {
		what = "a whole folder"
	}
	var names []string
	for _, row := range T.pullItems(dest) {
		if len(names) >= 30 {
			names = append(names, "…")
			break
		}
		names = append(names, row["item"].(string))
	}
	listed := "none right now"
	if len(names) > 0 {
		listed = strings.Join(names, ", ")
	}
	name := "pull_" + dest.Slug
	return AgentToolDef{
		Tool: Tool{
			Name: name,
			Description: fmt.Sprintf("Copy a file or folder into the %q file store from %q. For a folder it copies %s into %s/<name>; a file is copied whole. After that search_%s and read_%s can reach it. You see %s's file and folder names, not their contents: pull one to read it. There now, newest folders first: %s. A folder already here is added to, not replaced, so pulling it again brings in just what is new; a file already here is replaced with the newer copy. A folder not prepared yet is refused with what it is missing. A large pull moves to the background and reports back when it is copied.",
				dest.Name, src.Name, what, dest.Name, dest.Slug, dest.Slug, src.Name, listed),
			Parameters: map[string]ToolParam{
				"item":    {Type: "string", Description: "The file or folder name in " + src.Name + ", exactly as listed."},
				"replace": {Type: "boolean", Description: "For a folder: remove the copy already in " + dest.Name + " first. Normally leave it off: pulling a folder that is already there ADDS to it (new files arrive, changed ones update, nothing is removed). Only when the user wants it cleared and copied fresh."},
			},
			Required: []string{"item"},
			Caps:     []Capability{CapWrite},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			job, err := T.pullJobFor(dest, StringArg(args, "item"), user, BoolArg(args, "replace"))
			if err != nil {
				return "", err
			}
			return bgrun.BackgroundRun{Tool: name, Label: "copying " + job.item + " into " + dest.Name,
				Run: func(work *ToolSession) (string, error) {
					return waitForPull(work.Context(), job)
				}}.Start(sess)
		},
	}, true
}

// waitForPull starts the pull (or joins one already running for that item)
// and waits for it, telling the run's activity how far it has got every two
// seconds: that is its progress line, and what keeps a long single-file copy
// from reading as a stuck one. Stopping ctx stops the copy.
func waitForPull(ctx context.Context, job *pullJob) (string, error) {
	run, _ := pulls.start(job)
	act := bgrun.RunActivityFrom(ctx)
	if act != nil {
		act.WatchDir(job.dest.Path)
	}
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-run.done:
			pulls.mu.Lock()
			res, err := run.result, run.err
			pulls.mu.Unlock()
			return res, err
		case <-ctx.Done():
			run.cancel()
			<-run.done
			return "", Error("stopped; nothing was kept in " + job.dest.Name)
		case <-tick.C:
			if act != nil {
				fmt.Fprintln(act, run.job.progress())
			}
		}
	}
}
