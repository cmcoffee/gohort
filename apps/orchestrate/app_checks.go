package orchestrate

// App checks: what an app is supposed to do, as tests that live with it
// (docs/design/app-checks.md). Builder writes one with each feature; they run
// on app_def test and on every save.
//
// A script check runs one data source or action with records and inputs of its
// own, in a run that writes nothing (appscript.Job.Check): no outbound write,
// no tool, agent or pipeline, no store command, no notification. Its records
// are scratch: a store made for the run and dropped after, so a check never
// reads or changes anyone's data.

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appcheck"
	"github.com/cmcoffee/gohort/tools/appscript"
	"github.com/cmcoffee/snugforge/kvlite"
)

// appChecksBudget is how long all of an app's checks may take on one run.
const appChecksBudget = 60 * time.Second

// appChecksAtOnce is how many script checks run side by side.
const appChecksAtOnce = 4

// appCheckResult is one check's outcome.
type appCheckResult struct {
	Name string
	Page bool
	// Ran is false for a check that did not run (the budget ran out, or a
	// page check with no browser); Pass is meaningful only when it ran.
	Ran  bool
	Pass bool
	// Why is what a failing check saw, or why one did not run.
	Why string
}

// appCheckScripts is the app's data sources and actions by name, for
// appcheck.Validate: each under the name given and its slugified form, the
// two ways a check may name one.
func appCheckScripts(spec AppSpec) map[string]bool {
	out := map[string]bool{}
	for _, d := range spec.DataSources {
		out[d.Name], out[slugify(d.Name)] = true, true
	}
	for _, a := range spec.Actions {
		out[a.Name], out[slugify(a.Name)] = true, true
	}
	return out
}

// appCheckScript finds the data source or action a script check names.
func appCheckScript(spec AppSpec, name string) (kind string, ds *AppDataSource, act *AppAction) {
	name = strings.TrimSpace(name)
	for i := range spec.DataSources {
		if spec.DataSources[i].Name == name || slugify(spec.DataSources[i].Name) == slugify(name) {
			return "data", &spec.DataSources[i], nil
		}
	}
	for i := range spec.Actions {
		if spec.Actions[i].Name == name || slugify(spec.Actions[i].Name) == slugify(name) {
			return "action", nil, &spec.Actions[i]
		}
	}
	return "", nil, nil
}

// validateAppChecks refuses checks that cannot run against spec as it is
// about to be saved: the shape (appcheck.Validate), and every script a check
// names still being one of the app's. Run on every save, so an update that
// drops a script a check runs is refused naming the check.
func validateAppChecks(spec AppSpec) error {
	if len(spec.Checks) == 0 {
		return nil
	}
	if err := appcheck.Validate(spec.Checks, appCheckScripts(spec)); err != nil {
		return fmt.Errorf("NOT SAVED: %v", err)
	}
	return nil
}

// appChecksArg reads the checks an app_def call passes.
func appChecksArg(raw any) ([]appcheck.Check, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var out []appcheck.Check
	if err := json.Unmarshal(b, &out); err != nil {
		var one appcheck.Check
		if json.Unmarshal(b, &one) != nil {
			return nil, fmt.Errorf("checks must be a list of {name, script, records?, inputs?, expect} or {name, steps}: %v", err)
		}
		out = []appcheck.Check{one}
	}
	return out, nil
}

// runAppChecks runs every check spec keeps, script checks side by side, all of
// them inside appChecksBudget.
func (t *chatTurn) runAppChecks(spec AppSpec) []appCheckResult {
	results := make([]appCheckResult, len(spec.Checks))
	deadline := time.Now().Add(appChecksBudget)
	slots := make(chan struct{}, appChecksAtOnce)
	var wg sync.WaitGroup
	for i, c := range spec.Checks {
		results[i] = appCheckResult{Name: c.Name, Page: c.IsPage()}
		if c.IsPage() {
			results[i].Why = "page checks do not run yet"
			continue
		}
		wg.Add(1)
		go func(i int, c appcheck.Check) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			if time.Now().After(deadline) {
				results[i].Why = fmt.Sprintf("not run: the checks' %s ran out", appChecksBudget)
				return
			}
			results[i] = t.runScriptCheck(spec, c)
		}(i, c)
	}
	wg.Wait()
	return results
}

// runScriptCheck runs one script check against scratch records, in a run that
// writes nothing, and judges what it printed.
func (t *chatTurn) runScriptCheck(spec AppSpec, c appcheck.Check) appCheckResult {
	res := appCheckResult{Name: c.Name, Ran: true}
	kind, ds, act := appCheckScript(spec, c.Script)
	var name, lang, script string
	var caps []string
	switch {
	case ds != nil:
		name, lang, script, caps = ds.Name, ds.Language, ds.Script, ds.Capabilities
	case act != nil:
		name, lang, script, caps = act.Name, act.Language, act.Script, act.Capabilities
	default:
		res.Why = fmt.Sprintf("the app has no data source or action %q", c.Script)
		return res
	}
	recs := c.Records
	if recs == nil {
		recs = []map[string]any{}
	}
	recJSON, _ := json.Marshal(recs)
	params := make(map[string]any, len(c.Inputs))
	for k, v := range c.Inputs {
		params[k] = v
	}
	// Scratch: the run's store is made here and dropped with it, so a shared
	// collection reads empty and nothing the script writes lands anywhere.
	scratch := &DBase{Store: kvlite.MemStore()}
	fixed := map[string]string{"caller": appscript.CallerAlias(spec, t.user)}
	if len(spec.SharedCollections) > 0 {
		fixed["shared"] = appscript.SharedInput(scratch, spec)
	}
	args, _, _ := appScriptArgs(spec, string(recJSON), params, fixed)
	out, err := appscript.Job{Owner: spec.Owner, DB: scratch, Slug: spec.Slug, Kind: kind, Name: name,
		Language: lang, Script: script, Caps: caps, Args: args, Libs: spec.Libraries, Spec: &spec,
		Interactive: t.inPersonsLane(), Check: true}.Run()
	if err != nil {
		res.Why = "it could not run: " + appOneLine(err.Error(), 400)
		return res
	}
	if why := c.Judge(out); why != "" {
		res.Why = why
		return res
	}
	res.Pass = true
	return res
}

// appChecksReport words results for the author: one line each, then a count.
func appChecksReport(results []appCheckResult) string {
	if len(results) == 0 {
		return ""
	}
	var b strings.Builder
	pass, fail, skipped := 0, 0, 0
	b.WriteString("Checks:\n")
	for _, r := range results {
		switch {
		case !r.Ran:
			skipped++
			fmt.Fprintf(&b, "SKIP %q: %s\n", r.Name, r.Why)
		case r.Pass:
			pass++
			fmt.Fprintf(&b, "PASS %q\n", r.Name)
		default:
			fail++
			fmt.Fprintf(&b, "FAIL %q: %s\n", r.Name, r.Why)
		}
	}
	fmt.Fprintf(&b, "%d passed, %d failed", pass, fail)
	if skipped > 0 {
		fmt.Fprintf(&b, ", %d not run", skipped)
	}
	b.WriteString(".")
	return b.String()
}

// appChecksGate runs next's checks before next is saved. A check that fails
// is run again on prior, the version serving now: if it passed there and is
// unchanged by this save, the save broke it, and the save is refused with
// nothing changed. A check that was already failing, or that this save adds
// or changes, is reported and does not block: it describes something not
// built yet, or a feature changed on purpose. report is the outcome for the
// author, "" with no checks.
func (t *chatTurn) appChecksGate(prior *AppSpec, next AppSpec) (report string, refusal error) {
	if len(next.Checks) == 0 {
		return "", nil
	}
	results := t.runAppChecks(next)
	var broke []string
	for i, r := range results {
		if !r.Ran || r.Pass || prior == nil {
			continue
		}
		old, ok := appCheckNamed(prior.Checks, r.Name)
		if !ok || !sameAppCheck(old, next.Checks[i]) {
			continue
		}
		if before := t.runOneAppCheck(*prior, old); before.Ran && before.Pass {
			broke = append(broke, fmt.Sprintf("%q: %s", r.Name, r.Why))
		}
	}
	if len(broke) > 0 {
		what := "a check"
		if len(broke) > 1 {
			what = fmt.Sprintf("%d checks", len(broke))
		}
		return "", fmt.Errorf("NOT SAVED: this change breaks %s that passed on the version serving now:\n- %s\n\nThe app is unchanged and still serving. Fix the change, not the check: change a check only when its feature changed on purpose, and say so.", what, strings.Join(broke, "\n- "))
	}
	return appChecksReport(results), nil
}

// runOneAppCheck runs a single check against spec.
func (t *chatTurn) runOneAppCheck(spec AppSpec, c appcheck.Check) appCheckResult {
	if c.IsPage() {
		return appCheckResult{Name: c.Name, Page: true, Why: "page checks do not run yet"}
	}
	return t.runScriptCheck(spec, c)
}

// appCheckNamed finds the check named name.
func appCheckNamed(checks []appcheck.Check, name string) (appcheck.Check, bool) {
	for _, c := range checks {
		if strings.EqualFold(strings.TrimSpace(c.Name), strings.TrimSpace(name)) {
			return c, true
		}
	}
	return appcheck.Check{}, false
}

// sameAppCheck reports whether two checks test the same thing the same way.
func sameAppCheck(a, b appcheck.Check) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
