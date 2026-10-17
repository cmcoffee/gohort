package orchestrate

// "Lead designs, worker builds" for Builder's apps: Builder's own form of
// "Lead plans, worker does the work" (AgentRecord.LeadPlans on Builder).
//
// The generic plan mode does not fit Builder. It takes the lead's work tools
// and hands the work to small steps, each with a fresh context and without
// the authoring checks, while the hard part of Builder's work is writing the
// thing itself. App building splits differently: the lead settles with the
// person what the app is and writes the design down, a worker writes the
// page and the scripts from that design with app_def and its checks, and
// the lead reads what came back (the code, not only the verify result),
// compares it with the design, and sends the worker a list of changes until
// it is right. The lead does the two jobs a stronger model is worth paying
// for, designing and judging, and none of the rounds that cost the most:
// writing code and reading raw tool output.
//
// Only apps move. On the lead, app_def keeps its reading actions (get, list,
// test, verify, ...) and refuses the ones that change an app, which go
// through code_app; agents, tools and everything else Builder makes are
// built as before. A chat turn only: a Builder run another agent sent runs
// as it would without this.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
)

// codeAppTool is the lead's way to have a worker write an app.
const codeAppTool = "code_app"

// maxAppBuilds is how many code_app runs one turn may make: the first build
// and three rounds of changes. A design that is still wrong after that is
// one to take back to the person, not one more round.
const maxAppBuilds = 4

// appWriteActions are app_def's actions that change an app, or put one in a
// folder to change. The lead in this mode refuses them; the coder has them.
var appWriteActions = map[string]bool{
	"create": true, "update": true, "update_section": true, "add_section": true, "remove_section": true,
	"patch_html": true, "patch": true, "replace_function": true, "patch_function": true,
	"revert": true, "undo": true, "restore": true, "checkout": true, "publish": true,
	"unpack": true, "add_asset": true, "remove_asset": true,
}

// builderSplitsApps reports whether this Builder turn designs on the lead and
// has a worker write its apps.
func (t *chatTurn) builderSplitsApps(machineOn bool) bool {
	return isBuilderAgent(t.agent.ID) && t.agent.LeadModel && t.agent.LeadPlans &&
		t.shouldUseLeadModel() && !machineOn
}

// splitForBuilderApps is the chat turn's catalog step for the mode: app_def
// reads only, code_app is added, and the lead is told how the turn works.
func (pr *planRun) splitForBuilderApps() {
	t := pr.t
	if !t.builderSplitsApps(pr.mach.on) {
		return
	}
	for i := range pr.allTools {
		if pr.allTools[i].Tool.Name == "app_def" {
			pr.allTools[i] = leadReadsAppDef(pr.allTools[i])
		}
	}
	// The request is read when a build runs: the catalog is assembled before
	// the turn's newest message is picked out.
	pr.allTools = append(pr.allTools, t.codeAppToolDef(func() string { return pr.triggerMsg }))
	pr.sys += builderSplitBlock
	Log("[orchestrate.routing] agent=%s Builder designs on the lead; a worker writes its apps", t.agent.ID)
}

// builderSplitBlock tells the lead how an app gets made this turn.
const builderSplitBlock = `

## You design and review apps; a worker writes them
In this mode you do not write an app's page or scripts yourself: app_def reads and checks (get, list, stores, revisions, test, verify, run, help) and refuses every change, and code_app hands the writing to a worker. Agents, tools and everything else you build as usual.

For an app, new or changed:
1. Settle with the person what it should do, as you always do.
2. Write the design, complete enough that someone who never saw this conversation could build it: what the page is for and how it is laid out on a phone and on a wide screen; every data source and action with its inputs and the exact JSON it returns; the record fields; the settings; how the page calls its backend; and how to tell it works. For a change, the app's id and exactly what to change and why.
3. Call code_app with it. The worker builds it with app_def, runs test and verify, and reports back; the result also says, from the store, which app changed and whether it passed verify.
4. Review the code, not only the report: read each section with app_def get (id, then section_id for each) and each script with get script=<name>. Check it against the design: the data contracts, what happens on an error or an empty result, the layout at both widths, anything the worker chose on its own. Then call code_app again with a numbered list of specific changes, or tell the person it is ready once verify passes.
`

// leadReadsAppDef is app_def for the designing lead: its reading actions as
// they are, its writing ones refused with where the writing goes instead.
func leadReadsAppDef(td AgentToolDef) AgentToolDef {
	h := td.Handler
	td.Tool.Description = "This turn you read and check apps with this tool; code_app makes every change. " + td.Tool.Description
	if h == nil {
		return td
	}
	td.Handler = func(ctx context.Context, args map[string]any) (string, error) {
		action := strings.ToLower(strings.TrimSpace(stringArg(args, "action")))
		if appWriteActions[action] {
			return "", fmt.Errorf("app_def %s is the worker's to do in this mode: call code_app with the design (or, for a change, the app's id and the changes), and read what it built with app_def get", action)
		}
		return h(ctx, args)
	}
	return td
}

// codeAppToolDef runs the coder: one worker step with Builder's tools and
// app_def, writing an app from the lead's design. request is the person's
// latest message, which the worker sees beside the design.
func (t *chatTurn) codeAppToolDef(request func() string) AgentToolDef {
	var mu sync.Mutex
	builds := 0
	return AgentToolDef{
		Tool: Tool{
			Name:        codeAppTool,
			Description: "Have a worker write an app, or change one, from your design. It builds with app_def, runs test and verify, and reports what it did; the result also says which app changed and whether it passed verify. Then read the code with app_def get and call this again with the changes it needs.",
			Parameters: map[string]ToolParam{
				"title":  {Type: "string", Description: "What this build is, in a few words (\"Build the weather app\", \"Fix the empty-forecast case\")."},
				"app":    {Type: "string", Description: "The app's id when changing one that exists. Leave it out for a new app."},
				"design": {Type: "string", Description: "For a new app, the whole design: purpose, layout on a phone and on a wide screen, every data source and action with its inputs and the exact JSON it returns, record fields, settings, how the page reaches its backend, how to tell it works. For a change, a numbered list of exactly what to change and why. It must stand alone: the worker sees this and the person's latest message, not the conversation."},
			},
			Required: []string{"design"},
			Caps:     []Capability{CapExecute},
		},
		SerialFirePerBatch: true,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			design := strings.TrimSpace(stringArg(args, "design"))
			if design == "" {
				return "", fmt.Errorf("design is required: what the worker should build, or the changes to make")
			}
			mu.Lock()
			if builds >= maxAppBuilds {
				mu.Unlock()
				return "", fmt.Errorf("this turn has used its %d builds: tell the person where the app stands and what is still wrong, and take the rest up with them", maxAppBuilds)
			}
			builds++
			n := builds
			mu.Unlock()
			title := strings.TrimSpace(stringArg(args, "title"))
			if title == "" {
				title = "Write the app"
			}
			app := slugify(strings.TrimSpace(stringArg(args, "app")))
			before := appRevisions(t.user)
			step := PlanStep{ID: n, Title: title, WorkerBrief: coderBrief(app, design), appCoder: true}
			Log("[orchestrate.worker] agent=%s code_app %d (%q) app=%q", t.agent.ID, n, title, app)
			out, err := t.runWorkerStep(nil, step, request(), nil)
			if err != nil {
				return "", fmt.Errorf("the build failed: %w", err)
			}
			return codeAppResult(out, appsChangedSince(t.user, before), n), nil
		},
	}
}

// coderBrief is the worker's instructions for one build.
func coderBrief(app, design string) string {
	var b strings.Builder
	b.WriteString("You are writing a gohort app with app_def from a design someone else made. Build exactly what it says; where it leaves a choice open, make it and say so in your report.\n\n")
	if app != "" {
		fmt.Fprintf(&b, "Change the existing app %q. Read it first (app_def get, section_id for a section in full, script=<name> for a script) and edit in place: update_section, patch_html, replace_function. Do not rewrite what you were not asked to change.\n\n", app)
	} else {
		b.WriteString("This is a new app. Read app_def help first, then build it (checkout, edit the folder's files, publish; or create).\n\n")
	}
	b.WriteString("Run test on every script, then verify, and fix what they find until verify passes. You cannot ask the person anything.\n\n")
	b.WriteString("Then reply with: the app's id; what you built or changed; the verify result; every choice the design left to you and what you chose; anything you could not do and why.\n\n")
	b.WriteString("## The design\n")
	b.WriteString(design)
	return b.String()
}

// appRevisions is each of the user's apps by slug, with the revision it is at
// and a digest of what it holds: a revision stamp has one-second grain, so
// the digest is what tells two saves in one second apart.
func appRevisions(user string) map[string]string {
	out := map[string]string{}
	for _, s := range ListAppSpecs(user) {
		out[s.Slug] = s.Updated + " " + appLiveFingerprint(s)
	}
	return out
}

// appsChangedSince is each app that is new or at a new revision since before,
// by slug, read from the store.
func appsChangedSince(user string, before map[string]string) []AppSpec {
	var out []AppSpec
	for _, s := range ListAppSpecs(user) {
		if rev, ok := before[s.Slug]; !ok || rev != s.Updated+" "+appLiveFingerprint(s) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}

// codeAppResult is what the lead reads back: the worker's own report, then
// what the store says changed, which the lead can rely on where the report
// is mistaken.
func codeAppResult(report string, changed []AppSpec, n int) string {
	var b strings.Builder
	b.WriteString("## The worker's report\n")
	b.WriteString(strings.TrimSpace(report))
	b.WriteString("\n\n## What changed, read from the store\n")
	if len(changed) == 0 {
		b.WriteString("No app changed. Whatever the report says, nothing was saved.\n")
	}
	for _, s := range changed {
		fmt.Fprintf(&b, "- %s (%q), revision %s: %s\n", s.Slug, s.Name, s.Updated, s.VerifyStatus())
	}
	fmt.Fprintf(&b, "\nBuild %d of %d this turn. Review the code now: app_def get with the id, then section_id for each section and script=<name> for each script, against the design. Call code_app with numbered changes, or tell the person it is ready once verify passes.", n, maxAppBuilds)
	return b.String()
}
