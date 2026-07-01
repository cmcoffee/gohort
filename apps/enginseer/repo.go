package enginseer

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// repoTable stores Repo records per user (in the user's main-DB sub-store).
const repoTable = "repos"

// Repo is one registered repository. The file bodies live separately in
// RepoFilesDB (encrypted); this record is just the pointer + status.
type Repo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	URL      string `json:"url"`      // git remote (https://github.com/... etc.)
	Provider string `json:"provider"` // github | gitlab | git (informational for now)
	Branch   string `json:"branch"`   // branch to clone; "" = the remote default
	Token    string `json:"token,omitempty"`  // access token for private repos; never returned in lists
	Cloned   string `json:"cloned,omitempty"` // RFC3339 of last successful ingest
	Files    int    `json:"files,omitempty"`  // ingested file count
}

// repoScope maps a repo to its orchestrate scope user, so its investigation
// memory (graph/reference/explicit) is isolated per repository.
func repoScope(repoID string) string {
	return "app:enginseer:" + repoID
}

// loadRepo fetches one repo record from the user's store.
func loadRepo(udb Database, id string) (Repo, bool) {
	var r Repo
	if udb == nil || id == "" || !udb.Get(repoTable, id, &r) {
		return Repo{}, false
	}
	return r, true
}

// handleRepos: GET lists the user's repos (tokens stripped); POST registers a
// new one and kicks off the initial clone+ingest.
func (T *Enginseer) handleRepos(w http.ResponseWriter, r *http.Request) {
	userID, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		items := make([]Repo, 0, 8)
		if udb != nil {
			for _, key := range udb.Keys(repoTable) {
				var rec Repo
				if udb.Get(repoTable, key, &rec) {
					rec.Token = "" // never leak the token to the client
					items = append(items, rec)
				}
			}
		}
		writeJSON(w, items)

	case http.MethodPost:
		var req Repo
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		req.URL = strings.TrimSpace(req.URL)
		if req.URL == "" {
			http.Error(w, "url is required", http.StatusBadRequest)
			return
		}
		if req.ID == "" {
			req.ID = UUIDv4()
		}
		if req.Name == "" {
			req.Name = repoNameFromURL(req.URL)
		}
		if req.Provider == "" {
			req.Provider = providerFromURL(req.URL)
		}
		udb.Set(repoTable, req.ID, req)
		// Kick off the initial clone+ingest in the background.
		go T.cloneAndIngest(userID, udb, req.ID)
		out := req
		out.Token = ""
		writeJSON(w, out)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleRepoOne: DELETE <id> removes the repo + its files; POST <id>/refresh
// re-clones.
func (T *Enginseer) handleRepoOne(w http.ResponseWriter, r *http.Request) {
	userID, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/repos/")
	id, action, _ := strings.Cut(rest, "/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	if _, found := loadRepo(udb, id); !found {
		http.NotFound(w, r)
		return
	}
	switch {
	case action == "refresh" && r.Method == http.MethodPost:
		go T.cloneAndIngest(userID, udb, id)
		w.WriteHeader(http.StatusAccepted)
	case action == "" && r.Method == http.MethodDelete:
		udb.Unset(repoTable, id)
		wipeRepoFiles(userID, id)
		clearRepoScopedMemory(id)
		w.WriteHeader(http.StatusNoContent)
	case action != "":
		// /api/repos/<id>/<suffix> — the per-repo Memory modal endpoints
		// (graph/facts/inferred/...). Ownership already checked above.
		T.handleRepoMemory(w, r, id, action)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// repoNameFromURL derives a display name ("owner/repo") from a git URL.
func repoNameFromURL(url string) string {
	s := strings.TrimSuffix(strings.TrimSpace(url), ".git")
	s = strings.TrimSuffix(s, "/")
	parts := strings.Split(s, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	if len(parts) >= 1 {
		return parts[len(parts)-1]
	}
	return url
}

// providerFromURL guesses the provider from the host (informational).
func providerFromURL(url string) string {
	switch {
	case strings.Contains(url, "github."):
		return "github"
	case strings.Contains(url, "gitlab."):
		return "gitlab"
	default:
		return "git"
	}
}
