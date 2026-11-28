package orchestrate

// An app folder installed, or built for a preview, without a Builder chat
// (docs/design/apps-as-repositories.md). Anvil's Install and Preview work on
// an app's folder for the person who pressed them; there is no chat turn, no
// agent making the change, and no card to approve it on. Both go through the
// same app_def create or update Builder's publish runs, so every check and
// gate an app has applies however the change arrives: the refusals, the
// script checks, the shared-app approval (SaveAppSpecAs), and the page
// checks that put the previous version back.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appdraft"
)

// personTurn is the turn a person's own Install or Preview runs on: their
// store and name, and nothing of a chat. Every chat-only part of the app_def
// path reads absent as "not in a chat": no app-building session, no agent
// whose edits are limited, no session to note unrun actions against.
func (T *OrchestrateApp) personTurn(ctx context.Context, user string) *chatTurn {
	if ctx == nil {
		ctx = context.Background()
	}
	return &chatTurn{app: T, user: user, udb: UserDB(T.DB, user), ctx: ctx}
}

// InstallAppFolder installs the app in the folder dir as user: their own
// app, created or updated. It is refused when the live app changed since the
// folder was written or last installed (the folder would undo it), and by
// any check or gate a Builder publish would be refused by. The person
// pressing Install is the approval of the change. note goes in the app's
// history.
func (T *OrchestrateApp) InstallAppFolder(ctx context.Context, user, dir, note string) (string, error) {
	user = strings.TrimSpace(user)
	if user == "" {
		return "", fmt.Errorf("no one to install it as")
	}
	t := T.personTurn(ctx, user)
	// Installs are Anvil's: its assistant made the change, the person
	// pressing Install finished it.
	t.via = "Anvil"
	out, err := t.publishAppFolder(dir, filepath.Base(dir), folderPublish{note: note})
	if err == nil && t.savedBroken != "" {
		return out, AppSavedBroken{Problem: t.savedBroken, Report: out}
	}
	// An install that went in working is finished, tested work: it is
	// committed to the app's history now, with the install's message.
	if err == nil {
		if slug, _, serr := T.AppFolderSlug(ctx, user, dir); serr == nil {
			msg := strings.TrimSpace(note)
			if msg == "" {
				msg = "Install from Anvil"
			}
			if _, cerr := CommitAppHistory(user, slug, user, t.via, msg); cerr != nil && !errors.Is(cerr, ErrNoAppHistory) {
				Log("[app-history] %s/%s installed, but its history commit failed: %v", user, slug, cerr)
			}
		}
	}
	return out, err
}

// AppSavedBroken is an install that went through and left the app broken:
// it is live as installed, and Problem says what is wrong (its page fails in
// a real browser, a data source fails to run). Builder reads such a save's
// report and fixes it; a person pressing Install got "Installed" and nothing
// else, and the assistant was never sent back.
type AppSavedBroken struct {
	Problem string
	Report  string
}

func (e AppSavedBroken) Error() string {
	return "it was installed, but it does not work: " + e.Problem
}

// AppFolderSlug is the app the folder dir holds, read from its app.json: the
// slug an install of it creates or updates, and the app's name.
func (T *OrchestrateApp) AppFolderSlug(ctx context.Context, user, dir string) (slug, name string, err error) {
	_, m, err := T.personTurn(ctx, strings.TrimSpace(user)).appFolderRead(dir, filepath.Base(dir))
	if err != nil {
		return "", "", err
	}
	return m.Slug, m.Name, nil
}

// BuildAppFolder builds the app in the folder dir as it would be installed
// for user, and saves nothing: what a preview serves. An update starts from
// the live app, as an install would, so what the folder leaves out the live
// app still has.
func (T *OrchestrateApp) BuildAppFolder(ctx context.Context, user, dir string) (AppSpec, error) {
	user = strings.TrimSpace(user)
	if user == "" {
		return AppSpec{}, fmt.Errorf("no one to build it for")
	}
	t := T.personTurn(ctx, user)
	in, m, err := t.appFolderRead(dir, filepath.Base(dir))
	if err != nil {
		return AppSpec{}, err
	}
	_, exists := LoadAppSpec(user, m.Slug)
	if exists {
		in["id"] = m.Slug
	}
	aw := &appWrite{t: t, args: in, isUpdate: exists}
	if !aw.load() || !aw.readMeta() || !aw.readParts() {
		if aw.retErr != nil {
			return AppSpec{}, aw.retErr
		}
		return AppSpec{}, fmt.Errorf("%s", strings.TrimSpace(aw.ret))
	}
	if msg := t.appDelegationRefusal(aw.spec); msg != "" {
		return AppSpec{}, fmt.Errorf("%s", msg)
	}
	spec := aw.spec
	spec.Owner = user
	return spec, nil
}

// AppFolderInstallable is why the app in the folder dir would not install
// for user, as far as can be told without saving: the folder read and the app
// built from it (what app.json names, the scripts, the page), and its checks
// able to run (each names a script the app has). nil when nothing stands in
// the way that is knowable here; a check that fails, or the page's own check,
// is found by installing. Anvil runs it as a turn ends, so a refusal reaches
// the assistant that can fix it, not the person pressing Install.
func (T *OrchestrateApp) AppFolderInstallable(ctx context.Context, user, dir string) error {
	spec, err := T.BuildAppFolder(ctx, user, dir)
	if err != nil {
		return err
	}
	if err := validateAppChecks(spec); err != nil {
		return fmt.Errorf("%s", strings.TrimPrefix(err.Error(), "NOT SAVED: "))
	}
	return nil
}

// CheckAppFolder runs the checks of the app in the folder dir, as built for
// user, and saves nothing: what Anvil's assistant runs on its working copy.
// Without it the assistant had no way to run them: one wrote its own runner
// for checks.json, another spent its whole turn looking for one. Script
// checks run as on a save; page checks drive the working copy in the
// browser, each through a preview made for its one load, on its own records.
func (T *OrchestrateApp) CheckAppFolder(ctx context.Context, user, dir string) (string, error) {
	spec, err := T.BuildAppFolder(ctx, user, dir)
	if err != nil {
		return "", err
	}
	if len(spec.Checks) == 0 {
		return "It builds, and nothing found would stop it installing. It has no checks yet: add them to checks.json, one for each thing the app is supposed to do.", nil
	}
	if err := validateAppChecks(spec); err != nil {
		return "", fmt.Errorf("%s", strings.TrimPrefix(err.Error(), "NOT SAVED: "))
	}
	t := T.personTurn(ctx, user)
	t.folderChecks = true
	return appChecksReport(t.runAppChecks(spec, true)), nil
}

// AppChecksScore is how the checks of an app folder went, counted: what a
// measure scores a task by, where CheckAppFolder's report is for reading.
type AppChecksScore struct {
	Report               string
	Passed, Failed, Skip int
}

// ScoreAppFolder runs the checks of the app in dir as CheckAppFolder does and
// counts them. An app that does not build, or whose checks cannot run, is an
// error.
func (T *OrchestrateApp) ScoreAppFolder(ctx context.Context, user, dir string) (AppChecksScore, error) {
	spec, err := T.BuildAppFolder(ctx, user, dir)
	if err != nil {
		return AppChecksScore{}, err
	}
	if err := validateAppChecks(spec); err != nil {
		return AppChecksScore{}, fmt.Errorf("%s", strings.TrimPrefix(err.Error(), "NOT SAVED: "))
	}
	t := T.personTurn(ctx, user)
	t.folderChecks = true
	results := t.runAppChecks(spec, true)
	s := AppChecksScore{Report: appChecksReport(results)}
	for _, r := range results {
		switch {
		case !r.Ran:
			s.Skip++
		case r.Pass:
			s.Passed++
		default:
			s.Failed++
		}
	}
	return s, nil
}

// WriteAppFolder writes user's app slug into dir as its folder (app.json,
// page, scripts, shared modules, assets, notes, checks), marked as holding
// the live app as it is, so an Install of it is not taken for undoing a
// change made elsewhere. It is user's own app; dir is made if it is missing.
func WriteAppFolder(user, slug, dir string) error {
	spec, ok := LoadAppSpec(strings.TrimSpace(user), strings.TrimSpace(slug))
	if !ok {
		return fmt.Errorf("you have no app %q", slug)
	}
	return writeAppFolderOf(spec, dir, nil)
}

// writeAppFolderOf writes spec, with its assets, into the folder dir: what
// the app owns as owned has it, nil for as it is live.
func writeAppFolderOf(spec AppSpec, dir string, owned map[string][]byte) error {
	assets := map[string][]byte{}
	if names, err := ListAppAssets(spec.Owner, spec.Slug); err == nil {
		for _, n := range names {
			if data, _, err := ReadAppAsset(spec.Owner, spec.Slug, n); err == nil {
				assets[n] = data
			}
		}
	}
	_, err := writeAppFolderOwned(dir, spec, assets, appLiveFingerprint(spec), owned)
	return err
}

// MeasureSlugPrefix starts the slug of every app a measure builds. The run
// below refuses any other slug, and deletes only an app it made, so a
// measure can never remove a person's own app.
const MeasureSlugPrefix = "anvil-eval-"

// BuilderAppTask is one app task for Builder to do, for a measure: the app's
// folder (Folder, named <Slug>.app, never published) and what to change in
// it.
type BuilderAppTask struct {
	User, Folder, Slug, Request string
	// NoScript runs the turn without tool_script, to measure what it adds.
	NoScript bool
}

// BuilderAppRun is how Builder's turn on a task went.
type BuilderAppRun struct {
	Reply                 string
	Steps, Rounds, Tokens int
	RanOn                 string // the tier that served it ("lead", "worker", or both)
	// Published is whether the turn published the app (its draft, or live);
	// the folder was then rewritten from that, which is what Builder
	// delivered.
	Published bool
}

// RunBuilderOnAppFolder runs one Builder turn on a task's app folder as a
// person's would run: the same send the chat page posts, the intake answered
// Change, App and the request, so the turn has Builder's chat prompt, its
// app mode, its tools and its drafts, in Builder's own workspace. What it
// publishes is new to the session, so nothing in it waits for an approval.
// When it ends, the task's folder holds the app as Builder left it, and
// everything the turn made outside it (its copy in Builder's workspace, the
// app it published with its drafts, history and owned parts, and the
// conversation) is deleted.
func (T *OrchestrateApp) RunBuilderOnAppFolder(ctx context.Context, task BuilderAppTask) (BuilderAppRun, error) {
	var out BuilderAppRun
	user, slug := strings.TrimSpace(task.User), strings.TrimSpace(task.Slug)
	if user == "" || !strings.HasPrefix(slug, MeasureSlugPrefix) || slugify(slug) != slug {
		return out, fmt.Errorf("a measured app's slug starts %q (got %q)", MeasureSlugPrefix, slug)
	}
	if filepath.Base(task.Folder) != slug+".app" {
		return out, fmt.Errorf("the task's folder is named %s.app (got %s)", slug, filepath.Base(task.Folder))
	}
	if _, ok := LoadAppSpec(user, slug); ok {
		return out, fmt.Errorf("%s already has an app %q; a measure builds only apps that do not exist yet", user, slug)
	}
	udb := UserDB(T.DB, user)
	builder, ok := loadAgent(udb, "seed-builder")
	if !ok {
		return out, fmt.Errorf("there is no Builder for %s", user)
	}
	ws := agentWorkspace(udb, user, builder)
	if ws == "" {
		return out, fmt.Errorf("Builder has no workspace for %s", user)
	}
	work := filepath.Join(ws, slug+".app")
	if _, err := os.Stat(work); err == nil {
		return out, fmt.Errorf("Builder's workspace already holds %s.app; a measure works only in a folder it puts there itself", slug)
	}
	if err := copyAppFolder(task.Folder, work); err != nil {
		os.RemoveAll(work)
		return out, err
	}
	sessionID := "measure-" + UUIDv4()
	rounds := &atomic.Int64{}
	measuredRounds.Store(sessionID, rounds)
	if task.NoScript {
		measureWithoutScript.Store(sessionID, true)
	}
	defer func() {
		measuredRounds.Delete(sessionID)
		measureWithoutScript.Delete(sessionID)
		DeleteAppOwned(user, slug)
		DeleteAppSpec(user, slug)
		deleteChatSession(udb, builder.ID, sessionID)
		os.RemoveAll(work)
	}()

	intake := map[string]string{"action": "Change", "kind": "App", "change": task.Request}
	msg := "**What do you want to do?:** Change\n\n**What kind of thing?:** App\n\n**What should change?:** " + task.Request +
		"\n\nThe app is the folder " + slug + ".app/ in your workspace (app.json, page.html, its scripts and checks.json). It has not been published yet."
	body, _ := json.Marshal(sendRequest{Message: msg, SessionID: sessionID, IntakeValues: intake})
	ctx, usage := WithRequestUsage(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/api/send", bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	// The turn's life is its own, not the request's (detachContext): stop it
	// the way the page's Stop does when the measure is stopped or times out.
	ended := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if c, ok := inflightCancels.Load(runSessKey(user, sessionID)); ok {
				c.(context.CancelFunc)()
			}
		case <-ended:
		}
	}()
	w := &measureWriter{header: http.Header{}}
	T.handleSendWithAppToolsPublishing(w, req, udb, user, builder, nil, nil)
	close(ended)

	if sess, ok := loadChatSession(udb, builder.ID, sessionID); ok {
		for _, m := range sess.Messages {
			if m.Role == "assistant" {
				out.Steps += len(m.ToolCalls)
				if strings.TrimSpace(m.Content) != "" {
					out.Reply = m.Content
				}
			}
		}
	}
	if out.Reply == "" {
		out.Reply = w.lastError()
	}
	out.Rounds = int(rounds.Load())
	u := usage.Diff(UsageSnapshot{})
	out.Tokens = int(u.WorkerInput + u.WorkerOutput + u.LeadInput + u.LeadOutput)
	var tiers []string
	if u.LeadInput+u.LeadOutput > 0 {
		tiers = append(tiers, "lead")
	}
	if u.WorkerInput+u.WorkerOutput > 0 {
		tiers = append(tiers, "worker")
	}
	out.RanOn = strings.Join(tiers, "+")
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	// What Builder delivered: its draft of the app when it published one
	// (a chat's publish is a draft until committed, app_draft.go), else the
	// app as live when it committed, else its folder.
	spec, ok := LoadAppSpec(user, slug)
	if d, isDraft := appdraft.Load(user, slug, user); isDraft {
		spec, ok = d.Spec, true
	}
	if ok {
		out.Published = true
		if werr := writeAppFolderOf(spec, work, nil); werr != nil && err == nil {
			err = fmt.Errorf("the published app could not be written to its folder: %w", werr)
		}
	}
	if cerr := replaceAppFolder(work, task.Folder); cerr != nil && err == nil {
		err = cerr
	}
	return out, err
}

// measuredRounds is, for each conversation a measure is running, how many
// rounds its turn took (addMeasuredRounds, from the turn's telemetry).
var measuredRounds sync.Map // session id -> *atomic.Int64

// addMeasuredRounds counts a finished loop's rounds for a measured
// conversation; any other conversation is not counted.
func addMeasuredRounds(sessionID string, n int) {
	if v, ok := measuredRounds.Load(sessionID); ok && n > 0 {
		v.(*atomic.Int64).Add(int64(n))
	}
}

// measureWriter is where a measured turn's page events go: kept only to say
// why a turn that wrote nothing ended.
type measureWriter struct {
	header http.Header
	mu     sync.Mutex
	buf    bytes.Buffer
}

func (m *measureWriter) Header() http.Header { return m.header }
func (m *measureWriter) WriteHeader(int)     {}
func (m *measureWriter) Flush()              {}
func (m *measureWriter) Write(b []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.buf.Len() < 1<<20 {
		m.buf.Write(b)
	}
	return len(b), nil
}

// lastError is the last error event the turn sent the page, or "".
func (m *measureWriter) lastError() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	last := ""
	for _, line := range strings.Split(m.buf.String(), "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev struct{ Kind, Text string }
		if json.Unmarshal([]byte(data), &ev) == nil && ev.Kind == "error" {
			last = "error: " + ev.Text
		}
	}
	return last
}

// copyAppFolder copies the folder from into to, which must not exist yet.
func copyAppFolder(from, to string) error {
	return filepath.WalkDir(from, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil // a link or a socket is not part of an app
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
}

// replaceAppFolder makes to a copy of from, whatever it held before.
func replaceAppFolder(from, to string) error {
	if err := os.RemoveAll(to); err != nil {
		return err
	}
	return copyAppFolder(from, to)
}
