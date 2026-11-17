// HTTP surface.
//
// The chat endpoint is the interesting one: it peeks at the request body to
// learn WHICH project this turn is about, builds that project's tool set, and
// then hands the untouched body on to orchestrate. Peeking rather than
// keeping an "active project" marker on the server means two browser tabs can
// hold two different projects open without one silently retargeting the
// other's next message — the project is a property of the message, not of the
// user.
package anvil

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/outbound"
)

// maxChatBody bounds the body we buffer in order to peek at it. The chat
// panel can attach documents, so this has to be generous; it exists to stop
// an unbounded read, not to police ordinary use.
const maxChatBody = 64 << 20

// notFoundJSON is http.NotFound for an endpoint whose only caller parses JSON.
//
// Every route under /api/ is read by fetch().then(r => r.json()), so answering
// a miss with net/http's plain-text "404 page not found" surfaces in the
// browser as "SyntaxError: The string did not match the expected pattern" —
// a message that names neither the request nor the reason, and sends whoever
// reads it looking at the JSON instead of at the 404. The status is the same;
// only the body becomes something the caller can render.
func notFoundJSON(w http.ResponseWriter, what string) {
	writeErr(w, http.StatusNotFound, what)
}

func (T *Anvil) handleAPI(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	isAdmin := callerIsAdmin(r)
	path := strings.TrimPrefix(r.URL.Path, "/api/")

	switch {
	case path == "grants":
		T.handleGrants(w, r, udb, user, isAdmin, "")
	case strings.HasPrefix(path, "grant/"):
		T.handleGrants(w, r, udb, user, isAdmin, strings.TrimPrefix(path, "grant/"))
	case path == "chat/send":
		T.handleChatSend(w, r, udb, user, isAdmin)
	case path == "chat/cancel":
		T.dispatchChat(w, r, "cancel", "")
	case path == "inject":
		// A note typed while the turn runs is the person's say on a halt
		// (halt.go), as their next message is.
		if r.Method == http.MethodPost {
			if body, err := io.ReadAll(io.LimitReader(r.Body, maxChatBody)); err == nil {
				r.Body = io.NopCloser(bytes.NewReader(body))
				var note struct {
					ID     string `json:"id"`
					Text   string `json:"text"`
					Action string `json:"action"`
				}
				if json.Unmarshal(body, &note) == nil && note.Action == "" && strings.TrimSpace(note.Text) != "" {
					liftHalt(note.ID)
				}
			}
		}
		// A message typed while a turn is running joins that turn's note queue
		// rather than starting a new one. Without this route the panel has
		// nowhere to put it and falls back to sending normally, which cancels
		// the run in progress and discards what it had already done.
		orch := agenthost.Get()
		if orch == nil {
			writeErr(w, http.StatusServiceUnavailable, "orchestrate is not initialized")
			return
		}
		orch.PublicHandleInject(w, r)
	case path == "chat/confirm":
		// Where an Allow/Deny click lands. Owner-checked inside orchestrate
		// against whoever parked the escalation, so this needs no gate of its
		// own beyond the RequireUser above.
		orch := agenthost.Get()
		if orch == nil {
			writeErr(w, http.StatusServiceUnavailable, "orchestrate is not initialized")
			return
		}
		orch.PublicHandleConfirm(w, r)
	case path == "chat/sessions":
		T.dispatchChat(w, r, "sessions", "")
	case path == "chat/sessions/rename":
		T.dispatchChat(w, r, "rename", "")
	case strings.HasPrefix(path, "chat/sessions/") && strings.HasSuffix(path, "/diagnostics"):
		sid := strings.TrimSuffix(strings.TrimPrefix(path, "chat/sessions/"), "/diagnostics")
		T.dispatchChat(w, r, "diagnostics", sid)
	case strings.HasPrefix(path, "chat/sessions/") && strings.HasSuffix(path, "/resolve"):
		// .../sessions/{sid}/blocks/{blockID}/resolve
		rest := strings.TrimSuffix(strings.TrimPrefix(path, "chat/sessions/"), "/resolve")
		sid, blockID, found := strings.Cut(rest, "/blocks/")
		if !found {
			notFoundJSON(w, "malformed block-resolve path")
			return
		}
		T.dispatchChat(w, r, "resolve-block:"+blockID, sid)
	case strings.HasPrefix(path, "chat/sessions/"):
		T.dispatchChat(w, r, "session-one", strings.TrimPrefix(path, "chat/sessions/"))
	case path == "runs/active":
		T.dispatchChat(w, r, "runs-active", "")
	case strings.HasPrefix(path, "runs/"):
		T.dispatchChat(w, r, "runs", "")
	default:
		notFoundJSON(w, "no such endpoint: "+path)
	}
}

// --- projects ----------------------------------------------------------------

func (T *Anvil) handleProjects(c *jsonapi.Call) (any, error) {
	isAdmin := callerIsAdmin(c.R)
	list := T.listProjects(c.UDB, c.User, isAdmin)
	// CanRemove is whether the caller may take the project away (see
	// handleProjectDelete), so the list offers Remove only where it works.
	type row struct {
		Project
		CanRemove bool `json:"can_remove"`
	}
	out := make([]row, 0, len(list))
	for _, p := range list {
		out = append(out, row{Project: p.redacted(), CanRemove: writeAllowed(p, c.User, isAdmin)})
	}
	return out, nil
}

// handleProjectCards is the home page's projects, a card each: what it is,
// where it lives, how its tree stands, and what the caller may do with it.
func (T *Anvil) handleProjectCards(c *jsonapi.Call) (any, error) {
	isAdmin := callerIsAdmin(c.R)
	projects := T.listProjects(c.UDB, c.User, isAdmin)
	// Each card's state is a git run or two; four at a time, so the page
	// waits for the slowest few rather than for every project in turn.
	states := make([]string, len(projects))
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	for i, p := range projects {
		wg.Add(1)
		slots <- struct{}{}
		go func(i int, p Project) {
			defer func() { <-slots; wg.Done() }()
			states[i] = projectCardState(p)
		}(i, p)
	}
	wg.Wait()
	rows := []map[string]any{}
	opened := openedTimes(c.UDB)
	for i, p := range projects {
		kind, where := "Repository", p.RepoURL
		if p.Kind == KindRoot {
			kind, where = "Directory", p.Root
		}
		switch p.Source {
		case sourceBundle:
			kind, where = "Bundle", p.BundleName
		case sourceApp:
			kind, where = "App", "/apps/"+p.AppSlug+"/"
		case sourceNew:
			where = "Started here"
		}
		if p.PushURL != "" {
			where = p.PushURL
		}
		state := states[i]
		access := "Read it"
		if writeAllowed(p, c.User, isAdmin) {
			access = "Change it"
		}
		row := map[string]any{
			"id": p.ID, "name": p.Name, "kind": kind, "where": where, "state": state,
			"access": access, "can_change": writeAllowed(p, c.User, isAdmin),
			"open": "/anvil/?project=" + url.QueryEscape(p.ID),
		}
		if t, ok := opened[p.ID]; ok {
			row["opened"] = t.UTC().Format(time.RFC3339)
		}
		rows = append(rows, row)
	}
	// The ones this person opened last first, as a resume list is; never
	// opened after, by name.
	sort.SliceStable(rows, func(i, j int) bool {
		ti, _ := rows[i]["opened"].(string)
		tj, _ := rows[j]["opened"].(string)
		if ti != tj {
			return ti > tj
		}
		return strings.ToLower(rows[i]["name"].(string)) < strings.ToLower(rows[j]["name"].(string))
	})
	return rows, nil
}

// openedTable is when each person last opened each project, in their own
// store, for the home page's order: the project open now is the one most
// likely opened next.
const openedTable = "anvil_opened"

// noteOpened records that this person opened project id now.
func noteOpened(udb Database, id string) {
	if udb != nil && id != "" {
		udb.Set(openedTable, id, time.Now().UTC())
	}
}

// openedTimes is when this person last opened each project.
func openedTimes(udb Database) map[string]time.Time {
	out := map[string]time.Time{}
	if udb == nil {
		return out
	}
	for _, k := range udb.Keys(openedTable) {
		var t time.Time
		if udb.Get(openedTable, k, &t) {
			out[k] = t
		}
	}
	return out
}

// projectCardState is the line a project's card shows about its tree.
func projectCardState(p Project) string {
	if line, unpacking := bundleCardState(p); unpacking {
		return line
	}
	base, err := p.baseDir()
	if err != nil {
		return "Not reachable: " + err.Error()
	}
	info := inspectRepo(base)
	if !info.IsRepo {
		return "Not under version control"
	}
	branch := info.Branch
	if branch == "" {
		branch = "detached at " + info.Head
	}
	switch info.Changed {
	case 0:
		return branch + ", clean"
	case 1:
		return branch + ", 1 file changed"
	}
	return fmt.Sprintf("%s, %d files changed", branch, info.Changed)
}

// projectOf is the project {id} names, when the caller may open it.
func (T *Anvil) projectOf(c *jsonapi.Call) (Project, error) {
	id := c.R.PathValue("id")
	p, ok := T.findProject(c.UDB, c.User, callerIsAdmin(c.R), id)
	if !ok {
		return Project{}, jsonapi.NotFound("no project " + id + " you can open")
	}
	return p, nil
}

func (T *Anvil) handleProject(c *jsonapi.Call) (any, error) {
	p, err := T.projectOf(c)
	if err != nil {
		return nil, err
	}
	return p.redacted(), nil
}

// handleProjectSave creates or updates a project.
//
// The admin gate lands on the KIND, not on the endpoint: anyone may add a
// clone of their own, only an admin may register a root. That is the whole
// tenancy decision in one branch — a root project points at somebody else's
// files, and the ability to name a path is the ability to read it.
func (T *Anvil) handleProjectSave(c *jsonapi.Call, in Project) (any, error) {
	udb, user, isAdmin := c.UDB, c.User, callerIsAdmin(c.R)
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return nil, jsonapi.BadRequest("a project needs a name")
	}
	if in.Kind != KindRoot {
		in.Kind = KindClone
	}
	if in.Kind == KindRoot && !isAdmin {
		return nil, jsonapi.Forbidden("registering a directory on this host is an admin action. Add a repository URL instead and Anvil will check it out into your own workspace.")
	}

	// An update must find the EXISTING record through the same lookup a read
	// would use, so a caller cannot claim someone else's id by sending it.
	var existing Project
	updating := false
	if in.ID != "" {
		if found, ok := T.findProject(udb, user, isAdmin, in.ID); ok {
			existing, updating = found, true
			if existing.Kind != in.Kind {
				return nil, jsonapi.BadRequest("a project's kind cannot change; delete it and add it again")
			}
		}
	}
	// Who works on what (docs/design/anvil-development.md): bringing
	// anything into gohort, a directory on this host or a repository from
	// elsewhere, is an administrator's. A user keeps the checkouts they
	// already have and may change how Anvil works on one, but not what it
	// is: pointing it at another repository is adding a project.
	if !isAdmin && !updating {
		return nil, jsonapi.Forbidden(addProjectAdminOnly)
	}
	// Where a project came from is the server's to say. A checkout made
	// from a bundle has no address, and keeps the branch it was made from.
	in.Source, in.BundleName, in.Unpack, in.UnpackError, in.AppSlug = "", "", "", "", ""
	if updating {
		in.Source, in.BundleName, in.Unpack, in.UnpackError, in.AppSlug = existing.Source, existing.BundleName, existing.Unpack, existing.UnpackError, existing.AppSlug
	}
	// A checkout from a bundle or of an app has no address, and keeps the
	// branch it was made with.
	if in.Source != "" {
		in.RepoURL, in.Token, in.Branch, in.FullHistory = "", "", existing.Branch, false
	}
	// Only a checkout runs its commands without asking (runsWithoutAsking).
	if in.Kind != KindClone {
		in.RunWithoutAsking = false
	}
	if in.Source != sourceApp {
		in.InstallOnSave, in.PreviewReal = false, false
	}
	// Where it is published is Publish's to set (publish.go).
	in.PushURL, in.PushBranch, in.PushToken, in.PushedAt, in.HasPushToken = existing.PushURL, existing.PushBranch, existing.PushToken, existing.PushedAt, false
	// Knowledge the person saving may read, and nothing else: attaching
	// grants no one anything (knowledge.go).
	in.Collections = readableCollections(user, in.Collections)
	prompts, err := validatePrompts(in.Prompts)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	in.Prompts = prompts
	if !isAdmin && updating && in.Kind == KindClone && in.Source == "" &&
		(strings.TrimSpace(in.RepoURL) != strings.TrimSpace(existing.RepoURL) || strings.TrimSpace(in.Branch) != strings.TrimSpace(existing.Branch)) {
		return nil, jsonapi.Forbidden("a checkout keeps the repository and branch it was made from: " + addProjectAdminOnly)
	}
	if !updating {
		in.ID = UUIDv4()
		in.CreatedAt = time.Now().UTC()
	} else {
		in.CreatedAt = existing.CreatedAt
	}

	switch in.Reasoning {
	case "", ReasoningStandard, ReasoningQuick, ReasoningLead:
		// fine
	default:
		return nil, jsonapi.BadRequest("unknown reasoning mode " + in.Reasoning)
	}

	// Defining an action is defining a command the host runs unsandboxed, so
	// it is an admin's to do; anybody else's save keeps what is there.
	if !isAdmin {
		in.Actions = existing.Actions
	}
	actions, err := validateActions(in.Actions)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	in.Actions = actions
	// The check runs in the sandbox, so whoever may save the project may
	// set it (a directory project is only saved by an administrator).
	in.CheckCommand = strings.TrimSpace(in.CheckCommand)
	if len(in.CheckCommand) > maxCheckCommand {
		return nil, jsonapi.BadRequest(fmt.Sprintf("the check command is over %d characters", maxCheckCommand))
	}

	switch in.Kind {
	case KindRoot:
		in.Owner = ""
		in.RepoURL, in.Token, in.Branch = "", "", ""
		if strings.TrimSpace(in.Root) == "" {
			return nil, jsonapi.BadRequest("a directory project needs a path")
		}
		if _, err := in.baseDir(); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
	case KindClone:
		in.Owner = user
		in.Root, in.ExtraRoots, in.AllowedUsers = "", nil, nil
		if in.Source != "" {
			break
		}
		if strings.TrimSpace(in.RepoURL) == "" {
			return nil, jsonapi.BadRequest("a repository project needs a URL")
		}
		if err := checkRepoURL(in.RepoURL, isAdmin); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
		if err := checkBranch(in.Branch); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
		// A blank token on an update means "leave it alone", not "clear it" —
		// the form never received the stored value to send back.
		if strings.TrimSpace(in.Token) == "" && updating {
			in.Token = existing.Token
		}
	}

	T.saveProject(udb, in)

	// Clone on first save, in the background: a first checkout of a large
	// repository outlives any reasonable request, and the alternative is a
	// form that appears to hang.
	if in.Kind == KindClone && !updating {
		go func(p Project, admin bool) {
			base, err := p.baseDir()
			if err != nil {
				Err(fmt.Sprintf("anvil: %s: %s", p.Name, err))
				return
			}
			if _, err := cloneProject(context.Background(), p, base, admin); err != nil {
				Err(fmt.Sprintf("anvil: cloning %s: %s", p.Name, err))
			}
		}(in, isAdmin)
	}
	return map[string]any{"ok": true, "id": in.ID}, nil
}

// addProjectAdminOnly is what someone who may not add a project is told.
const addProjectAdminOnly = "adding a project is an administrator's. Ask one to add the repository or directory; they can let you open it."

// handleProjectDelete removes the record. Files are removed only for a CLONE,
// whose checkout Anvil created and therefore owns. A root project's tree
// belongs to whoever put it there; forgetting about it is the most a delete
// is allowed to mean.
//
// Removing a project takes what changing it takes: a checkout is its
// owner's, and a directory project an administrator's. Somebody a directory
// project is shared with may open it, which is not the same as taking it away
// from everyone else who can.
func (T *Anvil) handleProjectDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	p, err := T.projectOf(c)
	if err != nil {
		return nil, err
	}
	if !writeAllowed(p, c.User, callerIsAdmin(c.R)) {
		return nil, jsonapi.Forbidden("removing a directory project is an admin action")
	}
	udb := c.UDB
	if p.Kind == KindClone {
		if base, err := p.baseDir(); err == nil && base != "" {
			if err := os.RemoveAll(base); err != nil {
				return nil, jsonapi.Status(http.StatusInternalServerError, fmt.Sprintf("could not remove the checkout: %s", err))
			}
		}
	}
	T.deleteProject(udb, p)
	removeCache(p)
	return map[string]any{"ok": true}, nil
}

// handleProjectStatus answers "what am I looking at, and is it clean" for the
// open project: whether it is ready to work in, and what git makes of it.
//
// Reported for a registered DIRECTORY exactly as for a checkout Anvil made.
// A directory on this host is very often somebody's working tree — that is the
// whole reason root projects exist — so treating version control as a property
// of clone projects would leave the most consequential projects the least
// legible. A project that genuinely is not under git says so, which is a
// normal state rather than a failure.
func (T *Anvil) handleProjectStatus(c *jsonapi.Call) (any, error) {
	p, err := T.projectOf(c)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"id": p.ID, "name": p.Name, "kind": p.Kind}
	base, err := p.baseDir()
	if err != nil {
		out["ready"] = false
		out["detail"] = err.Error()
		return out, nil
	}
	out["path"] = base
	entries, err := os.ReadDir(base)
	switch {
	case err != nil:
		out["ready"] = false
		out["detail"] = fmt.Sprintf("%s is not readable: %s", base, err)
	case len(entries) == 0 && p.Kind == KindClone:
		out["ready"] = false
		out["detail"] = "the checkout is still running, or it failed; check the server log"
	default:
		out["ready"] = true
	}
	// Asked regardless of kind, and regardless of whether the directory is
	// "ready" — a half-finished clone still has a branch worth naming.
	out["git"] = inspectRepo(base)
	return out, nil
}

// handleWorktree answers what currently differs from HEAD in this project —
// with ?path= set, the diff for one of those paths.
//
// This is the OTHER meaning of "changes", and both belong on the same screen.
// Anvil's journal answers "what did the assistant do", which is the question
// you have while reviewing a run. The working tree answers "what is different
// in this project right now", which is the question you have the rest of the
// time and which includes everything you edited yourself. Showing only the
// first is how a chip reading "12 changed" ends up beside a panel saying
// nothing has changed.
func (T *Anvil) handleWorktree(c *jsonapi.Call) (any, error) {
	p, err := T.projectOf(c)
	if err != nil {
		return nil, err
	}
	r := c.R
	base, err := p.baseDir()
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	if !isGitRepo(base) {
		// Not an error. A project outside version control has no working-tree
		// answer, and the caller renders the journal alone.
		return map[string]any{"is_repo": false, "changes": []WorkingChange{}}, nil
	}
	if rel := strings.TrimSpace(r.URL.Query().Get("path")); rel != "" {
		abs, err := p.resolvePath(base, rel)
		if err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
		diff, err := workingDiff(base, rel, abs, r.URL.Query().Get("untracked") == "1")
		if err != nil {
			return nil, jsonapi.Status(http.StatusBadGateway, err.Error())
		}
		return map[string]any{"path": rel, "diff": diff}, nil
	}
	changes, err := workingChanges(base)
	if err != nil {
		return nil, jsonapi.Status(http.StatusBadGateway, err.Error())
	}
	if changes == nil {
		changes = []WorkingChange{}
	}
	return map[string]any{"is_repo": true, "changes": changes}, nil
}

// handleDeepen fetches the history a shallow checkout was created without.
//
// The escape hatch for the default. Checking out shallow is right for the
// common case and wrong the moment somebody wants to know why a line is the
// way it is, and a default you cannot get out of without deleting and
// re-adding the project is not a default, it is a decision made for you.
func (T *Anvil) handleDeepen(c *jsonapi.Call, _ struct{}) (any, error) {
	p, err := T.projectOf(c)
	if err != nil {
		return nil, err
	}
	r, udb, user := c.R, c.UDB, c.User
	if p.Kind != KindClone {
		return nil, jsonapi.BadRequest("only a checked-out repository has a history to fetch; a directory on this host already has whatever history it has")
	}
	if !strings.EqualFold(p.Owner, user) {
		return nil, jsonapi.Forbidden("this checkout is not yours")
	}
	base, err := p.baseDir()
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	out, err := deepenRepo(r.Context(), p, base, callerIsAdmin(r))
	if err != nil {
		return nil, jsonapi.Status(http.StatusBadGateway, err.Error())
	}
	// Remember the choice, so a later re-clone of this project does not
	// silently go back to shallow after the user has said they want history.
	p.FullHistory = true
	T.saveProject(udb, p)
	return map[string]any{"ok": true, "detail": out}, nil
}

// handleRunAction fires one operator-defined action.
func (T *Anvil) handleRunAction(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	isAdmin := callerIsAdmin(r)
	p, found := T.findProject(udb, user, isAdmin, r.PathValue("id"))
	if !found {
		notFoundJSON(w, "no project "+r.PathValue("id")+" you can open")
		return
	}
	actionID := r.PathValue("action")
	if !actionsAllowed(p, user, isAdmin) {
		writeErr(w, http.StatusForbidden, "running this project's commands is an admin action")
		return
	}
	a, ok := p.findAction(actionID)
	if !ok {
		notFoundJSON(w, "no such command on this project")
		return
	}
	base, err := p.baseDir()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// A detached action replaces the server that is answering this request, so
	// the reply has to be flushed before the command starts. Anything written
	// after the process is gone reaches nobody, and the operator sees a dead
	// socket instead of "restarting".
	if a.Detached {
		res := ActionResult{ActionID: a.ID, Label: a.Label, Detached: true}
		if a.GateID != "" {
			gate, gok := p.findAction(a.GateID)
			if !gok {
				writeErr(w, http.StatusBadRequest, "this action is gated on an action that no longer exists")
				return
			}
			out, gerr := runShellIn(r.Context(), base, gate.Command, gate.timeout())
			if gerr != nil {
				jsonapi.Write(w, ActionResult{
					ActionID: a.ID, Label: a.Label, GateFailed: true, GateLabel: gate.Label,
					Output: fmt.Sprintf("%s failed, so %s did not run.\n\n%s\n%s", gate.Label, a.Label, gerr, out),
				})
				return
			}
			res.Output = strings.TrimSpace(out + "\n")
		}
		res.OK = true
		res.Output = strings.TrimSpace(res.Output + fmt.Sprintf("\n%s launched.", a.Label))
		jsonapi.Write(w, res)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if err := startShellIn(base, a.Command); err != nil {
			Err(fmt.Sprintf("anvil: launching %q: %s", a.Label, err))
		}
		return
	}
	jsonapi.Write(w, p.runAction(r.Context(), base, a))
}

// --- standing approvals ------------------------------------------------------

// handleGrants lists and revokes the caller's standing approvals for the
// project named in the query string.
//
// Scoped to one project on purpose: a Permissions screen that mixed every
// project's grants together would make "what may this thing do to THIS tree"
// unanswerable, which is the only question the screen exists to answer.
func (T *Anvil) handleGrants(w http.ResponseWriter, r *http.Request, udb Database, user string, isAdmin bool, grantID string) {
	orch := agenthost.Get()
	if orch == nil {
		writeErr(w, http.StatusServiceUnavailable, "orchestrate is not initialized")
		return
	}
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID == "" {
		writeErr(w, http.StatusBadRequest, "no project was named in the request; reload the page and pick one")
		return
	}
	p, ok := T.findProject(udb, user, isAdmin, projectID)
	if !ok {
		notFoundJSON(w, "no project "+projectID+" you can open")
		return
	}
	// The scope string has to match what the tools granted under, or the
	// screen lists nothing and revokes nothing while looking like it works.
	scope := (&toolCtx{project: p}).grantScope()
	orch.PublicHandleGrants(w, r, scope, grantID)
}

// --- edit journal ------------------------------------------------------------

func (T *Anvil) handleEdits(c *jsonapi.Call) (any, error) {
	r, udb, user, isAdmin := c.R, c.UDB, c.User, callerIsAdmin(c.R)
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID == "" {
		return nil, jsonapi.BadRequest("no project was named in the request; reload the page and pick one")
	}
	if _, ok := T.findProject(udb, user, isAdmin, projectID); !ok {
		return nil, jsonapi.NotFound("no project " + projectID + " you can open")
	}
	return listEdits(udb, projectID, r.URL.Query().Get("session_id")), nil
}

func (T *Anvil) handleRevert(c *jsonapi.Call, _ struct{}) (any, error) {
	udb, user, isAdmin, id := c.UDB, c.User, callerIsAdmin(c.R), c.R.PathValue("id")
	var e EditEntry
	if !udb.Get(editTable, id, &e) {
		return nil, jsonapi.NotFound("no such change")
	}
	p, ok := T.findProject(udb, user, isAdmin, e.ProjectID)
	if !ok {
		return nil, jsonapi.NotFound("that change belongs to a project you cannot open")
	}
	base, err := p.baseDir()
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	if err := revertEdit(udb, p, base, e); err != nil {
		return nil, jsonapi.Status(http.StatusConflict, err.Error())
	}
	haltForRevert(e)
	return map[string]any{"ok": true, "path": e.Path}, nil
}

// --- working notes -----------------------------------------------------------

// handleNotes is the operator's window onto what the assistant has written
// about this project.
//
// Notes are the layer the model rewrites on its own, unprompted, and the one
// that renders nearest the top of the prompt — so a note that has gone wrong
// steers every turn on this project with nowhere to look. A build command that
// stopped being true is worse than no memory at all, because it is stated with
// the same confidence as one that works.
//
// Addressed through the SCOPED agent id, the same one handleChatSend runs
// under, so the panel reads exactly what the model reads. Not routed through
// orchestrate's own notes endpoint: that one resolves the agent through
// loadAgent, and Anvil's per-project id is a per-turn identity with no stored
// record behind it.
//
//	GET  → {text, from_seed, updated_at, cap}
//	POST → {text} replaces the block wholesale; empty clears it back to the
//	seed (handleNotesSave).
//
// Gated on WRITE access rather than read: seeing what the assistant believes
// about a tree is part of reading it, correcting it is changing how it behaves.
func (T *Anvil) handleNotes(c *jsonapi.Call) (any, error) {
	p, udb, ns, err := T.notesOf(c)
	if err != nil {
		return nil, err
	}
	user, isAdmin := c.User, callerIsAdmin(c.R)
	stored := LoadOperatingNotes(udb, ns)
	eff := ResolveOperatingNotes(udb, ns, anvilSeedNotes)
	return map[string]any{
		"text": eff.Text,
		// Distinguishes "the assistant wrote this" from "you are looking at
		// the starting shape" — clearing means nothing in the second case,
		// and the panel says which it is rather than implying the assistant
		// has learned something it has not.
		"from_seed":  strings.TrimSpace(stored.Text) == "",
		"updated_at": stored.UpdatedAt,
		"cap":        OperatingNotesCap,
		"can_edit":   writeAllowed(p, user, isAdmin),
	}, nil
}

// notesOf is the project ?project_id= names and the namespace its notes
// live under.
func (T *Anvil) notesOf(c *jsonapi.Call) (Project, Database, string, error) {
	p, found := T.findProject(c.UDB, c.User, callerIsAdmin(c.R), strings.TrimSpace(c.R.URL.Query().Get("project_id")))
	if !found {
		return Project{}, nil, "", jsonapi.NotFound("no such project")
	}
	host := agenthost.Get()
	if host == nil {
		return Project{}, nil, "", jsonapi.Status(http.StatusServiceUnavailable, "the Anvil agent is unavailable")
	}
	return p, c.UDB, host.AgentStateNamespace(scopedAgentID(p)), nil
}

// handleNotesSave replaces the notes: see handleNotes.
func (T *Anvil) handleNotesSave(c *jsonapi.Call, body struct {
	Text string `json:"text"`
}) (any, error) {
	p, udb, ns, err := T.notesOf(c)
	if err != nil {
		return nil, err
	}
	user, isAdmin := c.User, callerIsAdmin(c.R)
	if !writeAllowed(p, user, isAdmin) {
		return nil, jsonapi.Forbidden("you can open this project but not change it")
	}
	// An empty POST is a CLEAR, not a save of nothing: the store row goes
	// away and the seed comes back, which is what "start it over" means
	// here. SaveOperatingNotes enforces the cap.
	saved, _ := SaveOperatingNotes(udb, ns, strings.TrimSpace(body.Text))
	return map[string]any{"text": saved.Text, "updated_at": saved.UpdatedAt}, nil
}

// --- chat --------------------------------------------------------------------

func (T *Anvil) handleChatSend(w http.ResponseWriter, r *http.Request, udb Database, user string, isAdmin bool) {
	orch := agenthost.Get()
	if orch == nil {
		writeErr(w, http.StatusServiceUnavailable, "orchestrate is not initialized")
		return
	}
	agent, ok := orch.LookupAppAgent(user, anvilAgentID)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "the Anvil agent is unavailable")
		return
	}

	// Peek at the body for the project and session, then put it back intact —
	// orchestrate parses the same bytes for everything else.
	body, err := io.ReadAll(io.LimitReader(r.Body, maxChatBody))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))

	var peek struct {
		ProjectID string `json:"project_id"`
		SessionID string `json:"session_id"`
		PlanMode  bool   `json:"plan_mode"`
	}
	_ = json.Unmarshal(body, &peek)
	// The person's next message is their say on a halt (halt.go).
	liftHalt(peek.SessionID)

	p, found := T.findProject(udb, user, isAdmin, peek.ProjectID)
	if !found {
		// No project selected: run with no tools rather than refusing. The
		// agent can still answer a question, and its prompt tells it to say it
		// has nothing open — which is a better first experience than an error
		// card on a page the user just arrived at.
		orch.PublicHandleSendWithAppTools(w, r, agent, nil)
		return
	}
	base, err := p.baseDir()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// Conversations, and the memory that accumulates alongside them, belong to
	// the project rather than to the app.
	agent.ID = scopedAgentID(p)

	// Working notes, scoped by the id above like every other per-agent state.
	//
	// This is the one memory layer a coding assistant actually needs and the
	// only one it could not reach. The project's own Instructions and its
	// CLAUDE.md are always in prompt but OPERATOR-owned — the agent reads them
	// and cannot write. Its two writable layers are both the wrong shape here:
	// store_fact under MemoryMode "agent" is for generalized lessons and steers
	// away from the specifics that matter (this tree's build command), and
	// Reference Memory only surfaces when a query happens to match, while a
	// build command has to be present on every turn. Notes are bounded,
	// rewritable and always in prompt, which is the shape of what is true about
	// working in one tree.
	//
	// Set on the per-turn record rather than the spec because AppAgentSpec has
	// no field for it, and because the seed is a per-project answer anyway.
	agent.EnableNotes = true
	agent.SeedNotes = anvilSeedNotes
	// Only knowledge attached to it. With none, an agent searches every
	// deployment-wide collection, whatever other apps gathered about
	// anything: noise beside the code it is working on.
	agent.KnowledgeAttachedOnly = true
	// The project's knowledge, as far as the person asking may read it
	// (knowledge.go).
	agent.AttachedCollections = readableCollections(user, p.Collections)

	// Linked dependencies, resolved against THIS caller: a link to a project
	// they may not open resolves to nothing, which is what makes linking safe
	// without a check of its own.
	deps := T.resolveDependencies(udb, user, isAdmin, p)

	// How hard to think on this project, and what it should know about it.
	agent.LeadModel, agent.ThinkBudget = p.applyReasoning(agent.LeadModel, agent.ThinkBudget)
	agent.OrchestratorPrompt += T.projectContext(p, base) + depSummary(deps)
	// Plan mode is told only where it changes something: a person who may
	// not change the project has no edits to hold back.
	planning := peek.PlanMode && writeAllowed(p, user, isAdmin)
	if planning {
		agent.OrchestratorPrompt += planModePrompt
	}

	pub := &agenthost.Publisher{}
	ctx := &toolCtx{
		udb:       udb,
		user:      user,
		isAdmin:   isAdmin,
		project:   p,
		base:      base,
		sessionID: peek.SessionID,
		readOnly:  !writeAllowed(p, user, isAdmin),
		planning:  planning,
		deps:      deps,
		emit: func(kind string, e EditEntry) {
			pub.Publish(editBlock(kind, p, e))
		},
		emitBlock: pub.Publish,
	}
	// After the agent's edits the check has to pass before the turn ends.
	pub.FinishCheck = ctx.finishCheck
	// What names this project, so the turn's web searches are checked for it.
	r = r.WithContext(outbound.WithPrivateTerms(r.Context(), searchPrivateTerms(p)...))
	orch.PublicHandleSendWithAppToolsPublishing(w, r, agent, ctx.codeTools(), pub)
}

// writeAllowed answers whether this caller may CHANGE the project, which is a
// different question from whether they may open it. A root project shared
// with a named non-admin is readable by them; changing the operator's tree
// stays with the operator.
func writeAllowed(p Project, user string, isAdmin bool) bool {
	if p.Kind == KindRoot {
		return isAdmin
	}
	return strings.EqualFold(p.Owner, user)
}

// editBlock renders one journal entry as a card for the conversation.
func editBlock(kind string, p Project, e EditEntry) agenthost.Block {
	verb := map[string]string{EditCreate: "Created", EditDelete: "Deleted", EditModify: "Edited"}[e.Kind]
	if verb == "" {
		verb = "Changed"
	}
	return agenthost.Block{
		Type:  kind,
		ID:    "anvil-edit-" + e.ID,
		Title: e.Path,
		Text:  fmt.Sprintf("%s · +%d −%d", verb, e.Added, e.Removed),
		Data: map[string]string{
			"edit_id":    e.ID,
			"project_id": p.ID,
			"kind":       e.Kind,
			"diff":       e.Diff,
			"revert_url": fmt.Sprintf("/anvil/api/edit/%s/revert", e.ID),
		},
	}
}

// dispatchChat serves the session rail, scoped to the project the request
// names. The panel substitutes {project_id} into its list / load / delete /
// truncate URLs from the picker, so every one of these arrives carrying the
// project whose threads it means.
//
// A request with no project resolves to nothing rather than to everything: an
// unscoped listing would show a different set than the one the user is looking
// at, which is worse than an empty one.
func (T *Anvil) dispatchChat(w http.ResponseWriter, r *http.Request, kind, sid string) {
	orch := agenthost.Get()
	if orch == nil {
		writeErr(w, http.StatusServiceUnavailable, "orchestrate is not initialized")
		return
	}
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agent, ok := orch.LookupAppAgent(user, anvilAgentID)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "the Anvil agent is unavailable")
		return
	}
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID == "" {
		// Cancel is the exception: it acts on a run already in flight and has
		// no list to scope, so refusing it for want of a project would strand
		// a turn nobody can stop.
		if kind != "cancel" && kind != "runs" && kind != "runs-active" {
			jsonapi.Write(w, []any{})
			return
		}
	} else {
		p, found := T.findProject(udb, user, callerIsAdmin(r), projectID)
		if !found {
			notFoundJSON(w, "no project "+projectID+" you can open")
			return
		}
		agent.ID = scopedAgentID(p)
	}
	switch {
	case kind == "cancel":
		orch.PublicHandleCancel(w, r, agent)
	case kind == "sessions":
		orch.PublicHandleSessionList(w, r, agent.ID)
	case kind == "session-one":
		orch.PublicHandleSessionOne(w, r, agent.ID, sid)
	case kind == "rename":
		orch.PublicHandleSessionRename(w, r, agent.ID)
	case kind == "diagnostics":
		orch.PublicHandleSessionDiag(w, r, agent.ID, sid)
	case strings.HasPrefix(kind, "resolve-block:"):
		orch.PublicHandleBlockResolve(w, r, agent.ID, sid, strings.TrimPrefix(kind, "resolve-block:"))
	case kind == "runs-active":
		orch.PublicHandleRunsActive(w, r)
	case kind == "runs":
		// Stream / cancel for a run in flight. Not project-scoped: a run id is
		// already unique and the dispatcher checks the caller owns it, so
		// demanding a project here would only break reconnecting to a run
		// whose page was reloaded before the picker had rendered.
		orch.PublicHandleRunsDispatch(w, r)
	default:
		notFoundJSON(w, "no such chat endpoint")
	}
}

// --- helpers -----------------------------------------------------------------

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
