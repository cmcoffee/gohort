// Starting a machine that RUNS.
//
// Unattended machines shipped without a door. The mode was reachable from
// Go, from the editor's toggle and from the `machine` tool, and there was
// no way to start one — which meant the whole thing was unexercised, and
// an unexercised runtime is a claim rather than a capability.
//
// This is the first door, and deliberately the smallest one that is
// honest: press a button, the run happens, you read what it produced.
//
// It is SYNCHRONOUS, which is the one thing to know about it. A run walks
// its steps with a model call each, so a long machine holds the request
// open; the timeout below is the ceiling. That is fine for the machines
// somebody is building and watching, and it is NOT the shape a
// twenty-two-step research run wants. The next door (a schedule, or a
// dispatch target) is where background execution belongs, and it can lean
// on the same RunUnattended underneath.
//
// The sibling of "Try it", and the difference is worth saying out loud:
// a rehearsal shows the PATH with no tools and stops at the step that
// waits. This runs the machine for real, with the caller's tools, to the
// step that finishes it.

package orchestrate

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// runTimeout bounds one unattended run started from the page.
//
// Generous, because this is where a machine with real work in it gets
// tried for the first time and a stingy ceiling would make an honest run
// look broken. It is still a ceiling: the request is open the whole time,
// and a run that outlives this wants the background door instead.
const runTimeout = 15 * time.Minute

// handleMachineRuns serves the panel protocol for one machine's runs.
//
//	POST   runs/stream        → run it, streaming the transcript
//	GET    runs/sessions      → past runs, newest first
//	GET    runs/sessions/<id> → one run's stored blocks
//	DELETE runs/sessions/<id> → drop a run
//
// The same protocol a pipeline's runs speak, because it was never about
// pipelines: a run produces a transcript of blocks and a result, and core
// serves that for anything that can emit them (RunSurface).
func (T *OrchestrateApp) handleMachineRuns(w http.ResponseWriter, r *http.Request, udb Database, user string, def MachineDef, sub string) {
	// The two refusals that are true whether or not a model is configured,
	// answered before the deployment check for that reason.
	if !def.Unattended && sub == "stream" {
		http.Error(w, "this machine converses rather than runs: it has a step that waits for a person, and nobody is waiting here. "+
			"Use Try it to rehearse it, or attach it to an agent and talk to it.", http.StatusBadRequest)
		return
	}
	if probs := def.Problems(); len(probs) > 0 && sub == "stream" {
		http.Error(w, "this machine will not run yet — "+probs[0]+" ("+strconv.Itoa(len(probs))+" outstanding). Its checklist lists them.",
			http.StatusBadRequest)
		return
	}
	if T.LLM == nil && sub == "stream" {
		http.Error(w, "worker LLM not configured", http.StatusServiceUnavailable)
		return
	}
	T.ServeRuns(w, r, RunSurface{
		DB:      udb,
		User:    user,
		OwnerID: def.ID,
		Timeout: runTimeout,
		Work: func(ctx context.Context, input string, _ map[string]string, sink PipelineSink) (string, error) {
			return T.runMachineStreaming(ctx, def, user, input, sink)
		},
	}, sub)
}

// runMachineStreaming is one unattended run, narrating itself.
//
// A machine has no transcript of its own: the walk produces phase results
// on a blackboard and breadcrumbs about what the framework decided. This
// turns both into the events the panel speaks — one block per step, the
// framework's decisions as status lines — so a long run is watchable
// rather than a spinner that eventually returns everything at once.
func (T *OrchestrateApp) runMachineStreaming(ctx context.Context, def MachineDef, user, input string, sink PipelineSink) (string, error) {
	sess := &ToolSession{Username: user, DB: AuthDB()}
	catalog, err := GetAgentToolsWithSession(sess, availableWorkerToolNames()...)
	if err != nil {
		Log("[orchestrate.machines] run %q: tool catalog partly unresolved for user=%q: %v", def.Name, user, err)
	}
	cache := NewRunToolCache()
	base := T.PhaseWorker(WrapToolsWithRunCache(cache, catalog))

	var seq int
	runner := func(ctx context.Context, ph MachinePhase, prompt string) (string, error) {
		seq++
		id := "phase-" + strconv.Itoa(seq)
		title := ph.Name
		if d := strings.TrimSpace(ph.Desc); d != "" {
			title += " — " + d
		}
		sink(PipelineEvent{Kind: "block", ID: id, Type: "phase", Title: title})
		out, err := base(ctx, ph, prompt)
		if err != nil {
			sink(PipelineEvent{Kind: "chunk", ID: id, Text: "(this step failed: " + err.Error() + ")"})
			sink(PipelineEvent{Kind: "block_done", ID: id})
			return "", err
		}
		sink(PipelineEvent{Kind: "chunk", ID: id, Text: strings.TrimSpace(out)})
		sink(PipelineEvent{Kind: "block_done", ID: id})
		return out, nil
	}

	cur := &MachineCursor{}
	final, out, err := T.RunUnattended(ctx, def, cur, MachineTurn{
		Input: input,
		User:  user,
		Now:   time.Now().In(UserLocation(user)).Format("Mon, January 2, 2006 at 3:04 PM MST"),
	}, runner, func(kind, detail string) {
		// The framework's own decisions, as status rather than as blocks: they
		// are about the run rather than part of it, and a breadcrumb that
		// claimed a step of its own would read as work that happened.
		sink(PipelineEvent{Kind: "status", Text: detail})
	})
	if err != nil {
		return out, err
	}
	if hits := cache.Hits(); hits > 0 {
		sink(PipelineEvent{Kind: "status", Text: strconv.Itoa(hits) + " tool call(s) answered from earlier in this run"})
	}
	Log("[orchestrate.machines] user=%q ran %q → finished at %s after %d step(s), %d cached tool call(s)",
		user, def.Name, final.Name, len(cur.Log)+1, cache.Hits())
	return out, nil
}

// machineRunPanel is the framework's run surface, pointed at this machine.
//
// The same panel a pipeline's page mounts: a transcript that arrives as it
// happens, past runs in a sidebar, cancel, and a record that survives the
// browser going away. None of that is machine-specific, which is why none
// of it is written here.
func machineRunPanel(def MachineDef) ui.Component {
	base := "api/machines/" + url_(def.ID) + "/runs/"
	return ui.PipelinePanel{
		SessionsListURL:  base + "sessions",
		SessionLoadURL:   base + "sessions/{id}",
		SessionDeleteURL: base + "sessions/{id}",
		SubmitURL:        base + "stream",
		SubmitLabel:      "Start the run",
		// This page's ?id= is the MACHINE, so the panel is told which param
		// carries a run or it would open a session that cannot exist.
		DeepLinkParam: "session",
		Fields: []ui.PipelineField{{
			Name: "input", Type: "textarea", Required: true, Rows: 3,
			Label:       "What is this run about?",
			Placeholder: runPlaceholder(def),
		}},
		Markdown:   true,
		BulkSelect: true,
		EmptyText:  "No runs yet. Every step runs for real, with your tools, ending at the step that hands off nowhere.",
	}
}

// runPlaceholder asks for the run's subject in the machine's own terms.
func runPlaceholder(def MachineDef) string {
	if d := strings.TrimSpace(def.Description); d != "" {
		return "What this run is about. " + firstSentence(d)
	}
	return "What this run is about"
}

// unattendedRunSection is the "Run it" section, present only when the
// machine can actually be run this way.
//
// A conversational machine gets an EMPTY section, which the page then
// drops (withoutEmptySections). It is not invisible on its own: the nav
// rail names an untitled section by its position, so an empty one shows up
// as "Section 8", a menu entry that opens nothing. Returning empty and
// filtering keeps the decision here and the page's reading order intact.
//
// The point of skipping it at all: a button that always refuses teaches
// somebody that the page lies to them.
func unattendedRunSection(def MachineDef) ui.Section {
	if !def.Unattended {
		return ui.Section{}
	}
	// A machine marked to run but not yet able to says so HERE, instead of
	// offering a button that refuses on press.
	//
	// The refusal was reaching the reader as a toast: a sentence that
	// appears for three seconds over a panel which then sits there looking
	// inert. That reads as "the button is broken", not as "this machine has
	// a step that waits and a run has nobody to wait for" — and the second
	// is a sentence somebody can act on.
	//
	// Problems() exactly, not the page's fuller checklist: this has to be
	// the same list the stream endpoint refuses on, or the section and the
	// button would disagree about whether the thing can run.
	if probs := def.Problems(); len(probs) > 0 {
		return ui.Section{
			Title: "Run it",
			Wide:  true,
			Subtitle: "Not yet. A machine that RUNS needs no step that waits for a person, and one step that finishes it " +
				"(no \"then go to\"). Fix these and the run panel appears here.",
			Body: ui.Card{HTML: blockedRunHTML(probs)},
		}
	}
	return ui.Section{
		Title:    "Run it",
		Wide:     true,
		NoChrome: true, // the panel manages its own layout
		Body:     machineRunPanel(def),
	}
}

// blockedRunHTML lists what is stopping a run, in the machine's own words.
func blockedRunHTML(probs []string) string {
	var b strings.Builder
	b.WriteString(`<div class="machine-findings-count">` + strconv.Itoa(len(probs)) + ` to fix before it can run</div>`)
	b.WriteString(`<ul class="machine-findings">`)
	for _, p := range probs {
		b.WriteString("<li>" + HTMLEscape(p) + "</li>")
	}
	b.WriteString("</ul>")
	return b.String()
}
