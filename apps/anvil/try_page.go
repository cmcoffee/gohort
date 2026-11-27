package anvil

// Looking at the app being built (docs/design/anvil-sees-its-app.md).
// preview shows the person the working copy; the assistant got its address
// and nothing else, so a page that threw on load, a section that rendered
// empty or a button that did nothing was found by the person. try_page loads
// the working copy in the headless browser as the person, optionally takes
// the steps a page check takes, and reports what happened: errors, what each
// part of the page shows, how the steps went, and a screenshot, which goes
// on a card for the person and in front of the assistant.
//
// Each call is one load, through a preview made for it (appdraft.CheckPreview,
// the custom-apps host's) on a fresh copy of the person's records held to a page
// check's rules, so a Delete the assistant clicks deletes nothing real and
// the person's own preview is left as it was.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/appcheck"
	"github.com/cmcoffee/gohort/core/appdraft"
)

const (
	// tryPageText caps what the report says the page shows, and
	// tryPageErrors how many of each kind of error it lists.
	tryPageText   = 6000
	tryPageErrors = 8
	// maxShots is how many screenshots the server keeps for cards.
	maxShots = 40
)

func (c *toolCtx) tryPageTool() AgentToolDef {
	step := ToolParam{Type: "object", Description: "One step, exactly one of: {fill, with}, {click}, {choose, option}, {see, in?}, {not_see, in?}, {rows, in}.",
		Properties: map[string]ToolParam{
			"fill":    {Type: "string", Description: "A field, by its label or placeholder."},
			"with":    {Type: "string", Description: "What to type into the field."},
			"click":   {Type: "string", Description: "A button or link, by its text."},
			"choose":  {Type: "string", Description: "A select, by its label."},
			"option":  {Type: "string", Description: "The option to pick, by its text."},
			"see":     {Type: "string", Description: "Text the page must show."},
			"not_see": {Type: "string", Description: "Text the page must not show."},
			"in":      {Type: "string", Description: "The title of the section to look in."},
			"rows":    {Type: "integer", Description: "How many rows the table or list named by in must hold."},
		}}
	return AgentToolDef{
		Tool: Tool{
			Name: "try_page",
			Description: "Load this app's page as your working copy has it, in a real browser, as the person, and report what happened: errors (uncaught, console, failed requests), what each part of the page shows, and a screenshot you see on your next step. " +
				"Give steps to try a flow the way a person would (fill a field, click a button, see the result). " +
				"It runs on a copy of the person's records that is thrown away after, so clicking Delete or Save changes nothing real, and nothing is installed. " +
				"Use it after changing the page, before you say the work is done. A load takes 5 to 20 seconds.",
			Parameters: map[string]ToolParam{
				"steps":    {Type: "array", Description: "Optional steps, in order. The first that does not hold stops the run and says what the page showed there.", Items: &step},
				"look_for": {Type: "string", Description: "Optional question about the screenshot, answered by a model that reads images, e.g. whether the form overlaps the table."},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return c.tryPage(ctx, args)
		},
	}
}

// tryPageResult is what one load found, for the report and the card.
type tryPageResult struct {
	steps    []appcheck.Step
	stepsRes *appcheck.StepsResult
	errs     []string
	shows    string
	shot     []byte
}

func (c *toolCtx) tryPage(ctx context.Context, args map[string]any) (string, error) {
	var steps []appcheck.Step
	if raw, ok := args["steps"]; ok && raw != nil {
		b, _ := json.Marshal(raw)
		if err := json.Unmarshal(b, &steps); err != nil {
			return "", fmt.Errorf("steps must be a list of steps such as {\"click\": \"Save\"}: %v", err)
		}
	}
	if len(steps) > 0 {
		if err := appcheck.Validate([]appcheck.Check{{Name: "try_page", Steps: steps}}, nil); err != nil {
			return "", fmt.Errorf("%s", strings.TrimPrefix(err.Error(), `check "try_page", `))
		}
	}
	if BrowserCheckPage == nil {
		return "", fmt.Errorf("there is no browser in this build, so the page cannot be loaded")
	}
	o, err := appHost()
	if err != nil {
		return "", err
	}
	spec, err := o.BuildAppFolder(ctx, c.user, c.base)
	if err != nil {
		return "", folderError(c.base, err)
	}

	c.checkMu.Lock()
	c.checkSeq++
	id := fmt.Sprintf("anvil-page-%s-%d", c.sessionID, c.checkSeq)
	c.checkMu.Unlock()
	title := spec.Name
	if title == "" {
		title = spec.Slug
	}
	start := time.Now()
	c.publishPage(id, title, "running", "", "", start, 0)

	r, err := loadWorkingCopy(c.user, spec, steps)
	if err != nil {
		c.publishPage(id, title, "failed", err.Error(), "", start, time.Since(start))
		return "", err
	}
	report := r.report(title)
	if look := strings.TrimSpace(strArg(args, "look_for")); look != "" && len(r.shot) > 0 {
		report += "\n\n" + lookAt(ctx, r.shot, look)
	}
	if sess := ToolSessionFromContext(ctx); sess != nil && len(r.shot) > 0 {
		sess.AppendViewImageAs(r.shot, "the page of "+title+" as try_page loaded it")
		report += "\n\nThe screenshot is in front of you on your next step."
	}
	state := "passed"
	if len(r.errs) > 0 || (r.stepsRes != nil && !r.stepsRes.OK) {
		state = "failed"
	}
	c.publishPage(id, title, state, r.summary(), keepShot(c.user, r.shot), start, time.Since(start))
	return report, nil
}

// loadWorkingCopy loads spec's page as user through a preview made for this
// load, on a copy of their records, taking steps.
func loadWorkingCopy(user string, spec AppSpec, steps []appcheck.Step) (tryPageResult, error) {
	if appdraft.CheckPreview == nil {
		return tryPageResult{}, fmt.Errorf("the working copy cannot be previewed here: the custom apps host is not running")
	}
	cookie, end, err := appdraft.CheckPreview(user, spec, true)
	if err != nil {
		return tryPageResult{}, fmt.Errorf("the working copy could not be previewed: %w", err)
	}
	defer end()
	probe := ""
	if len(steps) > 0 {
		probe = appcheck.StepsProbe(steps)
	}
	rep, err := CheckPageAsUser(RootDB, user, cookie.Path, probe, cookie)
	if err != nil {
		return tryPageResult{}, fmt.Errorf("the browser could not load the page: %w", err)
	}
	r := tryPageResult{steps: steps, shot: rep.Screenshot}
	if len(steps) > 0 {
		var sr appcheck.StepsResult
		if json.Unmarshal([]byte(rep.ProbeJSON), &sr) != nil {
			sr = appcheck.StepsResult{Saw: "the browser did not report on the steps"}
		}
		r.stepsRes = &sr
	}
	add := func(kind string, list []string) {
		for i, e := range list {
			if i == tryPageErrors {
				r.errs = append(r.errs, fmt.Sprintf("and %d more %s", len(list)-i, kind))
				return
			}
			r.errs = append(r.errs, kind+": "+oneLine(e, 300))
		}
	}
	add("uncaught exception", rep.PageErrors)
	add("console error", rep.ConsoleErrors)
	var failed []string
	for _, f := range rep.FailedRequests {
		// A missing favicon is the browser's, not the app's.
		if !strings.Contains(f, "favicon") {
			failed = append(failed, f)
		}
	}
	add("failed request", failed)
	// A request still open when the page settled is a slow one, not an
	// error, and the page's live-update feeds (the framework's /api/changes
	// and the app's own .../changes) stay open by design: listed under
	// Errors, they sent the assistant after a problem the page did not have.
	var slow []string
	for _, u := range rep.PendingRequests {
		if !isLiveFeed(u) {
			slow = append(slow, u)
		}
	}
	add("request still waiting (slow, not failed)", slow)

	var shows strings.Builder
	shows.WriteString(strings.TrimSpace(rep.BodyText))
	for i, f := range rep.FrameTexts {
		if f = strings.TrimSpace(f); f != "" {
			fmt.Fprintf(&shows, "\n\n[section frame %d]\n%s", i+1, f)
		}
	}
	r.shows = shows.String()
	if len(r.shows) > tryPageText {
		r.shows = r.shows[:tryPageText] + "\n...(the rest of the page's text is cut)"
	}
	return r, nil
}

// stepsLine says how the steps went, "" with none.
func (r tryPageResult) stepsLine() string {
	switch {
	case r.stepsRes == nil:
		return ""
	case r.stepsRes.OK:
		return fmt.Sprintf("All %d steps held.", len(r.steps))
	}
	what := "a step"
	if n := r.stepsRes.Step; n >= 1 && n <= len(r.steps) {
		what = fmt.Sprintf("Step %d (%s)", n, r.steps[n-1].Describe())
	}
	return what + " did not hold: " + oneLine(r.stepsRes.Saw, 400)
}

// summary is the card's text: the steps and the errors.
func (r tryPageResult) summary() string {
	var b strings.Builder
	if s := r.stepsLine(); s != "" {
		b.WriteString(s + "\n")
	}
	if len(r.errs) == 0 {
		b.WriteString("No errors.")
	} else {
		b.WriteString(strings.Join(r.errs, "\n"))
	}
	return b.String()
}

// report is what the assistant reads.
func (r tryPageResult) report(title string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Loaded %s as your working copy has it, on a copy of the person's records (nothing real changed, nothing installed).\n", title)
	if s := r.stepsLine(); s != "" {
		b.WriteString("\n" + s + "\n")
	}
	if len(r.errs) == 0 {
		b.WriteString("\nNo errors: no exception, console error or failed request.\n")
	} else {
		b.WriteString("\nErrors:\n- " + strings.Join(r.errs, "\n- ") + "\n")
	}
	if r.shows == "" {
		b.WriteString("\nThe page shows no text at all.")
	} else {
		b.WriteString("\nWhat the page shows:\n" + r.shows)
	}
	return b.String()
}

// lookAt asks a model that reads images question about shot.
func lookAt(ctx context.Context, shot []byte, question string) string {
	sess := ToolSessionFromContext(ctx)
	if sess == nil || sess.LLM == nil {
		return "(No model was at hand to look at the screenshot for " + question + ".)"
	}
	resp, err := sess.LLM.Chat(context.WithoutCancel(ctx),
		[]Message{{Role: "user", Content: question, Images: [][]byte{shot}}},
		WithCaller("anvil/try_page"), WithMaxRetries(0), WithThink(false))
	if err != nil || resp == nil || strings.TrimSpace(resp.Content) == "" {
		return "(The screenshot could not be looked at for " + question + ".)"
	}
	return "Asked of the screenshot (" + question + "): " + strings.TrimSpace(resp.Content)
}

// oneLine is s on one line, at most n bytes.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n] + "..."
	}
	return s
}

// publishPage puts a try_page load's card in the conversation, or updates
// it: the check's card, marked as a page, with its screenshot.
func (c *toolCtx) publishPage(id, title, state, output, shot string, start time.Time, took time.Duration) {
	if c.emitBlock == nil {
		return
	}
	c.emitBlock(agenthost.Block{
		Type:  checkBlock,
		ID:    id,
		Title: title,
		Text:  state,
		Data: map[string]string{
			"kind":    "page",
			"state":   state,
			"output":  output,
			"shot":    shot,
			"started": start.UTC().Format(time.RFC3339),
			"took":    fmt.Sprintf("%.0f", took.Seconds()),
		},
	})
}

// --- screenshots for cards ---------------------------------------------------

// Screenshots are kept in memory, the newest maxShots, and served to the
// person they were taken for: a card shows its picture while the server
// runs, and after a restart says it is gone. Nothing is stored.
type keptShot struct {
	user string
	data []byte
	at   time.Time
}

var (
	shotsMu sync.Mutex
	shots   = map[string]keptShot{}
)

// keepShot keeps shot for user and returns the address a card loads it
// from, "" for no screenshot.
func keepShot(user string, shot []byte) string {
	if len(shot) == 0 {
		return ""
	}
	id := UUIDv4()
	shotsMu.Lock()
	defer shotsMu.Unlock()
	shots[id] = keptShot{user: user, data: shot, at: time.Now()}
	for len(shots) > maxShots {
		oldest := ""
		for k, s := range shots {
			if oldest == "" || s.at.Before(shots[oldest].at) {
				oldest = k
			}
		}
		delete(shots, oldest)
	}
	return "api/shot/" + id
}

// handleShot serves a kept screenshot to the person it was taken for.
func (T *Anvil) handleShot(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	shotsMu.Lock()
	s, found := shots[r.PathValue("id")]
	shotsMu.Unlock()
	if !found || s.user != user {
		writeErr(w, http.StatusNotFound, "this screenshot is no longer kept")
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Write(s.data)
}

// isLiveFeed reports a page's live-update long poll: a request to a path
// ending in /changes, which the page holds open until something changes.
func isLiveFeed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/changes")
}
