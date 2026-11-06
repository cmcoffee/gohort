// The bulletin boards' HTTP side, for the Knowledge page's Bulletins section.
//
//	GET  /api/bulletins                     the boards, as table rows
//	POST /api/bulletins                     create {name, desc, ttl_hours}
//	GET  /api/bulletins/{name}              one board, for its Edit form
//	POST /api/bulletins/{name}              edit {desc, ttl_hours}
//	DELETE /api/bulletins/{name}            delete, and unfollow it everywhere
//	POST /api/bulletins/{name}/post         write a post {text}, as the owner
//	POST /api/bulletins/{name}/clear        empty the board, as the owner
//	GET  /api/bulletins/{name}/followers?pills=1, POST {target, on}
//	GET  /api/bulletins/{name}/posters?pills=1,   POST {target, on}
package orchestrate

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
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
	followText := "Nobody yet"
	switch {
	case b.AllAgents:
		followText = "All agents"
	case len(followers) > 0:
		followText = strings.Join(followers, ", ")
	}
	// What the board's card is washed with and says in its pill: whether its
	// followers see anything from it right now, and if not, why not.
	state := "empty"
	switch {
	case strings.TrimSpace(b.Text) != "" && b.current(now):
		state = "current"
	case strings.TrimSpace(b.Text) != "":
		state = "stale"
	case b.Cleared:
		state = "cleared"
	}
	postText := "No post yet"
	if b.Cleared && strings.TrimSpace(b.Text) == "" {
		postText = "Cleared " + bulletinAge(b.PostedAt, now, loc)
		if b.PostedBy != "" {
			postText += " by " + b.PostedBy
		}
	}
	if strings.TrimSpace(b.Text) != "" {
		postText = "Posted " + bulletinAge(b.PostedAt, now, loc)
		if b.PostedBy != "" {
			postText += " by " + b.PostedBy
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
	// Whether the followers see the post, and until when: the card's answer to
	// "is this board doing anything right now".
	seen := "Followers see nothing from it."
	switch state {
	case "current":
		seen = "Followers see it on every turn. It never goes out of date."
		if b.TTLHours > 0 {
			until := b.PostedAt.Add(time.Duration(b.TTLHours) * time.Hour)
			seen = fmt.Sprintf("Followers see it until %s (each post is current for %d hour%s).",
				bulletinWhen(until, now, loc), b.TTLHours, plural(b.TTLHours))
		}
	case "stale":
		until := b.PostedAt.Add(time.Duration(b.TTLHours) * time.Hour)
		seen = "Out of date since " + bulletinWhen(until, now, loc) + ": followers no longer see it."
	}
	return map[string]any{
		"name": b.Name, "desc": b.Desc, "latest": latest, "posted": postText,
		"followers": followText, "seen": seen, "state": state,
		"posters": chFirst(strings.Join(posters, ", "), "Only you"), "ttl": ttl,
		"size": bulletinSize(b.Text), "_has_post": strings.TrimSpace(b.Text) != "",
	}
}

// bulletinWhen says a time near now, past or to come, in the reader's zone:
// today, tomorrow or yesterday with the time, a weekday within the week either
// way, a date beyond it (with the year when it is not this one).
func bulletinWhen(at, now time.Time, loc *time.Location) string {
	at, now = at.In(loc), now.In(loc)
	day := func(t time.Time) string { return t.Format("2006-01-02") }
	switch day(at) {
	case day(now):
		return "today " + at.Format("3:04 PM")
	case day(now.AddDate(0, 0, 1)):
		return "tomorrow " + at.Format("3:04 PM")
	case day(now.AddDate(0, 0, -1)):
		return "yesterday " + at.Format("3:04 PM")
	}
	if d := at.Sub(now); d < 6*24*time.Hour && d > -6*24*time.Hour {
		return at.Format("Mon 3:04 PM")
	}
	if at.Year() != now.Year() {
		return at.Format("Jan 2, 2006")
	}
	return at.Format("Jan 2")
}

// bulletinSize is how much of a post's room it uses, "312 of 1,200
// characters", or nothing for an empty board. The room matters because the
// post rides on every turn of every follower.
func bulletinSize(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return groupThousands(len([]rune(text))) + " of " + groupThousands(maxBulletinChars) + " characters"
}

// groupThousands writes n with commas between each group of three digits.
func groupThousands(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0 && s[i-1] != '-'; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
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

// bulletinsList is the caller's boards, each as its row.
func (T *OrchestrateApp) bulletinsList(c *jsonapi.Call) (any, error) {
	agents := bulletinTargets(c.UDB, c.User)
	rows := []map[string]any{}
	for _, b := range listBulletins(c.UDB) {
		rows = append(rows, bulletinRow(b, agents, UserLocation(c.User)))
	}
	return rows, nil
}

// bulletinCreate makes a new, empty board.
func (T *OrchestrateApp) bulletinCreate(c *jsonapi.Call, body struct {
	Name     string `json:"name"`
	Desc     string `json:"desc"`
	TTLHours int    `json:"ttl_hours"`
}) (any, error) {
	name := bulletinName(body.Name)
	if name == "" {
		return nil, jsonapi.BadRequest("a board needs a name: letters, digits, - or _")
	}
	if _, exists := loadBulletin(c.UDB, name); exists {
		return nil, jsonapi.Status(http.StatusConflict, fmt.Sprintf("a board named %q already exists", name))
	}
	b := bulletinBoard{Name: name, Desc: strings.TrimSpace(body.Desc), TTLHours: body.TTLHours, Created: time.Now()}
	if err := saveBulletin(c.UDB, b); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	Log("[orchestrate.bulletins] user=%q created board %q", c.User, name)
	return map[string]any{"name": name}, nil
}

func (T *OrchestrateApp) registerBulletinRoutes() {
	T.HandleFunc("/api/bulletins/{name}", jsonapi.Methods{
		"GET":    jsonapi.Get(T.DB, T.bulletinGet),
		"POST":   jsonapi.Post(T.DB, T.bulletinSave, jsonapi.MaxBody(1<<16)),
		"PUT":    jsonapi.Put(T.DB, T.bulletinSave, jsonapi.MaxBody(1<<16)),
		"DELETE": jsonapi.Delete(T.DB, T.bulletinDelete),
	}.Serve)
	T.HandleFunc("/api/bulletins/{name}/post", jsonapi.Post(T.DB, T.bulletinPost, jsonapi.MaxBody(1<<16)))
	T.HandleFunc("/api/bulletins/{name}/clear", jsonapi.Post(T.DB, T.bulletinClear))
	for _, side := range []string{"followers", "posters"} {
		followers := side == "followers"
		T.HandleFunc("/api/bulletins/{name}/"+side, jsonapi.Methods{
			"GET": jsonapi.Get(T.DB, func(c *jsonapi.Call) (any, error) { return T.bulletinPills(c, followers) }),
			"POST": jsonapi.Post(T.DB, func(c *jsonapi.Call, one bulletinPill) (any, error) {
				return T.bulletinPillSave(c, followers, one)
			}, jsonapi.MaxBody(1<<16)),
		}.Serve)
	}
}

// bulletinAt is the caller's board the path names.
func bulletinAt(c *jsonapi.Call) (bulletinBoard, error) {
	b, found := loadBulletin(c.UDB, c.R.PathValue("name"))
	if !found {
		return bulletinBoard{}, errNotFound
	}
	return b, nil
}

// bulletinPost writes a post, as the owner.
func (T *OrchestrateApp) bulletinPost(c *jsonapi.Call, body struct {
	Text string `json:"text"`
}) (any, error) {
	b, err := bulletinAt(c)
	if err != nil {
		return nil, err
	}
	if _, err := postBulletin(c.UDB, b.Name, body.Text, "you"); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return map[string]any{"name": b.Name}, nil
}

// bulletinClear empties the board, as the owner.
func (T *OrchestrateApp) bulletinClear(c *jsonapi.Call, _ struct{}) (any, error) {
	b, err := bulletinAt(c)
	if err != nil {
		return nil, err
	}
	if _, err := clearBulletin(c.UDB, b.Name, "you"); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	Log("[orchestrate.bulletins] user=%q cleared board %q", c.User, b.Name)
	return map[string]any{"name": b.Name}, nil
}

// bulletinGet reads one board, for its Edit form.
func (T *OrchestrateApp) bulletinGet(c *jsonapi.Call) (any, error) {
	b, err := bulletinAt(c)
	if err != nil {
		return nil, err
	}
	return map[string]any{"name": b.Name, "desc": b.Desc, "ttl_hours": b.TTLHours, "text": b.Text}, nil
}

// bulletinSave edits the board's description and how long a post stays fresh.
func (T *OrchestrateApp) bulletinSave(c *jsonapi.Call, body struct {
	Desc     *string `json:"desc"`
	TTLHours *int    `json:"ttl_hours"`
}) (any, error) {
	b, err := bulletinAt(c)
	if err != nil {
		return nil, err
	}
	if body.Desc != nil {
		b.Desc = strings.TrimSpace(*body.Desc)
	}
	if body.TTLHours != nil {
		b.TTLHours = *body.TTLHours
	}
	if err := saveBulletin(c.UDB, b); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return map[string]any{"name": b.Name}, nil
}

// bulletinDelete deletes the board, unfollowed everywhere as it goes, so no
// agent keeps a name that points at nothing.
func (T *OrchestrateApp) bulletinDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	b, err := bulletinAt(c)
	if err != nil {
		return nil, err
	}
	for _, a := range bulletinTargets(c.UDB, c.User) {
		if !containsString(a.Bulletins, b.Name) {
			continue
		}
		a.Bulletins = removeString(a.Bulletins, b.Name)
		_, _ = saveAgent(c.UDB, a)
	}
	deleteBulletin(c.UDB, b.Name)
	Log("[orchestrate.bulletins] user=%q deleted board %q", c.User, b.Name)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// bulletinPills serves the follower or poster pills: the shape
// uiRenderScopePills takes. Followers have a primary "All agents" pill.
func (T *OrchestrateApp) bulletinPills(c *jsonapi.Call, followers bool) (any, error) {
	b, err := bulletinAt(c)
	if err != nil {
		return nil, err
	}
	items := []map[string]any{}
	for _, a := range bulletinTargets(c.UDB, c.User) {
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
		out["note"] = "Agents allowed to post here get a bulletin_board tool naming this board, which posts and clears. Anyone may read a board they follow; only these may write to it."
	}
	return out, nil
}

// bulletinPill is one pill's switch: an agent, on or off.
type bulletinPill struct {
	Target string `json:"target"`
	On     bool   `json:"on"`
}

// bulletinPillSave adds or removes one follower or poster.
func (T *OrchestrateApp) bulletinPillSave(c *jsonapi.Call, followers bool, one bulletinPill) (any, error) {
	b, err := bulletinAt(c)
	if err != nil {
		return nil, err
	}
	user, udb := c.User, c.UDB
	if strings.TrimSpace(one.Target) == "" {
		return nil, jsonapi.BadRequest("bad request")
	}
	if followers && one.Target == "__primary__" {
		b.AllAgents = one.On
		_ = saveBulletin(udb, b)
		return jsonapi.Empty(http.StatusNoContent), nil
	}
	ag, ok := loadAgent(udb, strings.TrimSpace(one.Target))
	if !ok || (ag.Owner != user && !isSeedID(ag.ID)) {
		return nil, jsonapi.NotFound("no such agent")
	}
	if followers {
		ag.Bulletins = removeString(ag.Bulletins, b.Name)
		if one.On {
			ag.Bulletins = append(ag.Bulletins, b.Name)
		}
		ag.Owner = user
		if _, err := saveAgent(udb, ag); err != nil {
			return nil, err
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
	return jsonapi.Empty(http.StatusNoContent), nil
}
