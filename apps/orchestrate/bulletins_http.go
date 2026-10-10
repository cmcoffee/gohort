// The bulletin boards' HTTP side, for the Knowledge page's Bulletins section.
//
//	GET  /api/bulletins                     the boards, as table rows
//	POST /api/bulletins                     create {name, desc, ttl_hours}
//	GET  /api/bulletins/{name}              one board, for its Edit form
//	POST /api/bulletins/{name}              edit {desc, ttl_hours}
//	DELETE /api/bulletins/{name}            delete, and unfollow it everywhere
//	POST /api/bulletins/{name}/post         write a post {text}, as the owner
//	GET  /api/bulletins/{name}/followers?pills=1, POST {target, on}
//	GET  /api/bulletins/{name}/posters?pills=1,   POST {target, on}
package orchestrate

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/oddjob/core"
)

// bulletinRow is one board as the table shows it.
func bulletinRow(b bulletinBoard, agents []AgentRecord, loc *time.Location) map[string]any {
	now := time.Now()
	var followers, posters []string
	for _, a := range agents {
		if !b.AllAgents && agentFollows(a, b) {
			followers = append(followers, chFirst(a.Name, a.ID))
		}
		if b.canPost(a.ID) {
			posters = append(posters, chFirst(a.Name, a.ID))
		}
	}
	followText := "nobody yet"
	switch {
	case b.AllAgents:
		followText = "All agents"
	case len(followers) > 0:
		followText = strings.Join(followers, ", ")
	}
	postText := "No post yet"
	if strings.TrimSpace(b.Text) != "" {
		postText = "Posted " + bulletinAge(b.PostedAt, now, loc)
		if b.PostedBy != "" {
			postText += " by " + b.PostedBy
		}
		if !b.current(now) {
			postText += " (out of date: followers no longer see it)"
		}
	}
	latest := b.Text
	if r := []rune(latest); len(r) > 140 {
		latest = string(r[:137]) + "..."
	}
	ttl := "Never goes out of date"
	if b.TTLHours > 0 {
		ttl = fmt.Sprintf("Current for %d hour%s", b.TTLHours, plural(b.TTLHours))
	}
	return map[string]any{
		"name": b.Name, "desc": b.Desc, "latest": latest, "posted": postText,
		"followers": followText, "posters": chFirst(strings.Join(posters, ", "), "only you"),
		"ttl": ttl,
	}
}

// bulletinTargets are the agents a board can reach: the owner's own, visible
// agents, the same set the machine pills offer.
func bulletinTargets(udb Database, user string) []AgentRecord {
	var out []AgentRecord
	for _, a := range listAgents(udb, user) {
		if isAppAgent(a.ID) || a.Hidden {
			continue
		}
		out = append(out, a)
	}
	return out
}

func (T *OrchestrateApp) handleBulletins(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		agents := bulletinTargets(udb, user)
		rows := []map[string]any{}
		for _, b := range listBulletins(udb) {
			rows = append(rows, bulletinRow(b, agents, UserLocation(user)))
		}
		writeJSON(w, rows)
	case http.MethodPost:
		var body struct {
			Name     string `json:"name"`
			Desc     string `json:"desc"`
			TTLHours int    `json:"ttl_hours"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		name := bulletinName(body.Name)
		if name == "" {
			http.Error(w, "a board needs a name: letters, digits, - or _", http.StatusBadRequest)
			return
		}
		if _, exists := loadBulletin(udb, name); exists {
			http.Error(w, fmt.Sprintf("a board named %q already exists", name), http.StatusConflict)
			return
		}
		b := bulletinBoard{Name: name, Desc: strings.TrimSpace(body.Desc), TTLHours: body.TTLHours, Created: time.Now()}
		if err := saveBulletin(udb, b); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		Log("[orchestrate.bulletins] user=%q created board %q", user, name)
		writeJSON(w, map[string]any{"name": name})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (T *OrchestrateApp) handleBulletinOne(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/bulletins/")
	name, action, _ := strings.Cut(rest, "/")
	b, found := loadBulletin(udb, name)
	if !found {
		http.NotFound(w, r)
		return
	}
	switch action {
	case "":
		T.bulletinRecord(w, r, udb, user, b)
	case "post":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if _, err := postBulletin(udb, b.Name, body.Text, "you"); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"name": b.Name})
	case "followers", "posters":
		T.bulletinPills(w, r, udb, user, b, action == "followers")
	default:
		http.NotFound(w, r)
	}
}

// bulletinRecord reads, edits or deletes one board.
func (T *OrchestrateApp) bulletinRecord(w http.ResponseWriter, r *http.Request, udb Database, user string, b bulletinBoard) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"name": b.Name, "desc": b.Desc, "ttl_hours": b.TTLHours, "text": b.Text})
	case http.MethodPost, http.MethodPut:
		var body struct {
			Desc     *string `json:"desc"`
			TTLHours *int    `json:"ttl_hours"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if body.Desc != nil {
			b.Desc = strings.TrimSpace(*body.Desc)
		}
		if body.TTLHours != nil {
			b.TTLHours = *body.TTLHours
		}
		if err := saveBulletin(udb, b); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"name": b.Name})
	case http.MethodDelete:
		// Unfollowed everywhere as it goes, so no agent keeps a name that
		// points at nothing.
		for _, a := range bulletinTargets(udb, user) {
			if !containsString(a.Bulletins, b.Name) {
				continue
			}
			a.Bulletins = removeString(a.Bulletins, b.Name)
			_, _ = saveAgent(udb, a)
		}
		deleteBulletin(udb, b.Name)
		Log("[orchestrate.bulletins] user=%q deleted board %q", user, b.Name)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// bulletinPills serves and applies the follower or poster pills: the shape
// uiRenderScopePills takes. Followers have a primary "All agents" pill.
func (T *OrchestrateApp) bulletinPills(w http.ResponseWriter, r *http.Request, udb Database, user string, b bulletinBoard, followers bool) {
	switch r.Method {
	case http.MethodGet:
		items := []map[string]any{}
		for _, a := range bulletinTargets(udb, user) {
			on := b.canPost(a.ID)
			if followers {
				on = containsString(a.Bulletins, b.Name)
			}
			items = append(items, map[string]any{"key": a.ID, "label": chFirst(a.Name, a.ID), "on": on})
		}
		out := map[string]any{"items": items}
		if followers {
			out["primary"] = map[string]any{"label": "All agents", "on": b.AllAgents}
			out["note"] = "An agent that follows this board sees its latest post on every turn it takes. All agents covers every agent, including ones you make later."
		} else {
			out["note"] = "Agents allowed to post here get a post_bulletin tool naming this board. Anyone may read a board they follow; only these may write to it."
		}
		writeJSON(w, out)
	case http.MethodPost:
		var one struct {
			Target string `json:"target"`
			On     bool   `json:"on"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&one); err != nil || strings.TrimSpace(one.Target) == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if followers && one.Target == "__primary__" {
			b.AllAgents = one.On
			_ = saveBulletin(udb, b)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		ag, ok := loadAgent(udb, strings.TrimSpace(one.Target))
		if !ok || (ag.Owner != user && !isSeedID(ag.ID)) {
			http.Error(w, "no such agent", http.StatusNotFound)
			return
		}
		if followers {
			ag.Bulletins = removeString(ag.Bulletins, b.Name)
			if one.On {
				ag.Bulletins = append(ag.Bulletins, b.Name)
			}
			ag.Owner = user
			if _, err := saveAgent(udb, ag); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		} else {
			b.Posters = removeString(b.Posters, ag.ID)
			if one.On {
				b.Posters = append(b.Posters, ag.ID)
			}
			_ = saveBulletin(udb, b)
		}
		Log("[orchestrate.bulletins] user=%q board %q: %s %s %q", user, b.Name,
			chIf(followers, "follower", "poster"), chIf(one.On, "added", "removed"), chFirst(ag.Name, ag.ID))
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
