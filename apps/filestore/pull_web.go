package filestore

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/bgrun"
)

// The two ways a pull is started: an admin's click on the destination store's
// panel, and an agent's tool call on a store it has attached. Both go through
// the one registry, so a case being pulled shows as running to both, and the
// second asker joins the first rather than starting a second copy.

// pullJobFor builds the job for a case into dest, or says why it cannot run.
func (T *FileStoreApp) pullJobFor(dest Store, caseName, user string, replace bool) (*pullJob, error) {
	src, ok := dest.pullSource(T.DB)
	if !ok {
		return nil, Error(dest.Name + " has no store to pull from: set one in its Settings (Pull cases from)")
	}
	return &pullJob{dest: dest, src: src, caseName: strings.TrimSpace(caseName), replace: replace, user: user}, nil
}

// pullCases is the source store's cases as the panel lists them: what each
// holds of the part a pull takes, and whether it is already here.
func (T *FileStoreApp) pullCases(dest Store) []map[string]any {
	src, ok := dest.pullSource(T.DB)
	if !ok {
		return []map[string]any{}
	}
	folders, err := ListFolders(src.Path)
	if err != nil {
		return []map[string]any{}
	}
	sub := dest.pullSubpath()
	out := []map[string]any{}
	for _, f := range folders {
		desc := "not prepared yet: no " + sub + "/ folder"
		if sub == "" {
			desc = fmt.Sprintf("%d files, %s", f.Files, HumanSize(f.Bytes))
		} else if dir, err := SubRoot(src.Path, f.Name); err == nil {
			if _, err := SubRoot(dir, sub); err == nil {
				desc = sub + "/ ready to copy"
			}
		}
		history := "not in " + dest.Name + " yet"
		if dir, err := SubRoot(dest.Path, f.Name); err == nil {
			if newest, err := bundleNewest(dir); err == nil {
				history = "in " + dest.Name + " since " + newest.Format("Jan 2 15:04") + "; pulling again replaces it"
			}
		}
		out = append(out, map[string]any{"case": f.Name, "desc": desc, "history": history})
	}
	return out
}

// handlePull is the panel's half.
//
//	GET  /filestore/api/pull/cases?slug=<dest>            the source's cases
//	POST /filestore/api/pull?slug=<dest>&case=<name>      start (replaces a copy already here)
//	GET  /filestore/api/pull/progress?slug=<dest>&case=   {"progress"} or {"outcome"}
//	POST /filestore/api/pull/cancel?slug=<dest>&case=     stop it
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
	caseName := strings.TrimSpace(r.URL.Query().Get("case"))
	switch {
	case strings.HasSuffix(r.URL.Path, "/cases"):
		writeJSON(w, T.pullCases(dest))
	case strings.HasSuffix(r.URL.Path, "/progress"):
		if run, ok := pulls.get(dest.Slug + "/" + caseName); ok {
			writeJSON(w, run.status())
			return
		}
		writeJSON(w, map[string]any{})
	case strings.HasSuffix(r.URL.Path, "/cancel"):
		if run, ok := pulls.get(dest.Slug + "/" + caseName); ok {
			run.cancel()
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		job, err := T.pullJobFor(dest, caseName, user, true)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		pulls.start(job)
		writeJSON(w, map[string]any{"running": true, "message": "Copying " + caseName + " into " + dest.Name})
	}
}

// pullToolDef is the agent's half: pull_<store>, on a store with a pull link.
// It runs as a background-capable run, so a big case moves to the background
// and reports back, and Stop on the run stops the copy.
func (T *FileStoreApp) pullToolDef(sess *ToolSession, user string, dest Store) (AgentToolDef, bool) {
	src, ok := dest.pullSource(T.DB)
	if !ok {
		return AgentToolDef{}, false
	}
	sub := dest.pullSubpath()
	what := "each case's " + sub + "/ folder (the prepared part, not the raw download)"
	if sub == "" {
		what = "the whole case folder"
	}
	var names []string
	if folders, err := ListFolders(src.Path); err == nil {
		for i, f := range folders {
			if i >= 30 {
				names = append(names, "…")
				break
			}
			names = append(names, f.Name)
		}
	}
	listed := "none right now"
	if len(names) > 0 {
		listed = strings.Join(names, ", ")
	}
	name := "pull_" + dest.Slug
	return AgentToolDef{
		Tool: Tool{
			Name: name,
			Description: fmt.Sprintf("Copy a case into the %q file store from %q, where cases arrive and are prepared. It copies %s into %s/<case>, after which search_%s and read_%s can reach it. You see %s's case names, not their contents: pull one to read it. Cases there now, newest first: %s. A case that is not prepared yet is refused with what it is missing. A large case moves to the background and reports back when it is copied.",
				dest.Name, src.Name, what, dest.Name, dest.Slug, dest.Slug, src.Name, listed),
			Parameters: map[string]ToolParam{
				"case":    {Type: "string", Description: "The case's folder name in " + src.Name + ", exactly as listed."},
				"replace": {Type: "boolean", Description: "Overwrite a copy already in " + dest.Name + ". Only when the user wants it refreshed."},
			},
			Required: []string{"case"},
			Caps:     []Capability{CapWrite},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			job, err := T.pullJobFor(dest, StringArg(args, "case"), user, BoolArg(args, "replace"))
			if err != nil {
				return "", err
			}
			return bgrun.BackgroundRun{Tool: name, Label: "copying " + job.caseName + " into " + dest.Name,
				Run: func(work *ToolSession) (string, error) {
					return waitForPull(work.Context(), job)
				}}.Start(sess)
		},
	}, true
}

// waitForPull starts the pull (or joins one already running for that case)
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
