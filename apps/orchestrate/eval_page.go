// The eval surface: a list of suites, and one page per suite that edits it,
// runs it, and shows what every previous run scored.
//
// The run panel is not built here. An eval run is a run, so the page points
// PipelinePanel at /api/evals/<id>/… and gets the transcript, the history, the
// reconnect and the cancel button for free — the same trade the pipelines page
// makes, for the same reason.
//
// See docs/eval-primitive.md.
package orchestrate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// handleEvalsPage lists the owner's suites.
func (T *OrchestrateApp) handleEvalsPage(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	_ = user
	page := ui.Page{
		Title:     "Evals",
		ShowTitle: true,
		BackURL:   "/orchestrate/",
		Sections: []ui.Section{
			{
				Title: "Suites",
				Subtitle: "A suite grades one thing — an agent, a pipeline, a tool, a machine — and keeps every score it has ever given it. " +
					"The number to watch is the one that moves after an edit.",
				Wide: true,
				Body: ui.Table{
					Source:    "api/eval-suites",
					RowKey:    "id",
					EmptyText: "No suites yet. A suite is how you find out whether the last edit helped.",
					Columns: []ui.Col{
						{Field: "name", Label: "Suite", Link: "/orchestrate/eval?id={id}"},
						{Field: "target", Label: "Grades", Mute: true},
						{Field: "cases", Label: "Cases"},
						{Field: "last", Label: "Last score", Type: "badge"},
						{Field: "trend", Label: "Since the edit before", Mute: true},
					},
				},
			},
			{
				Title:    "New suite",
				Subtitle: "Cases can be lifted from an agent that already has them: POST /api/agents/<id>/eval-suite.",
				Body: ui.FormPanel{
					PostURL:     "api/eval-suites",
					SubmitLabel: "Create",
					Invalidate:  []string{"api/eval-suites"},
					Fields: []ui.FormField{
						{Field: "name", Type: "text", Label: "Name", Placeholder: "Debate quality"},
						{Field: "target_kind", Type: "select", Label: "Grades a", Options: []ui.SelectOption{
							{Value: "agent", Label: "Agent"},
							{Value: "pipeline", Label: "Pipeline"},
							{Value: "tool", Label: "Tool"},
							{Value: "machine", Label: "Machine"},
						}},
						{Field: "target_id", Type: "text", Label: "Which one",
							Help: "Its name or id. A suite pointing at something that no longer exists reports that, rather than scoring zero."},
					},
				},
			},
		},
	}
	_ = udb
	page.ServeHTTP(w, r)
}

// handleEvalSuitePage is one suite: what it grades, its cases, a run panel, and
// the history that makes the whole thing worth having.
func (T *OrchestrateApp) handleEvalSuitePage(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	_ = user
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	suite, found := LoadEvalSuite(udb, id)
	if !found {
		http.NotFound(w, r)
		return
	}

	page := ui.Page{
		Title:     suite.Name,
		ShowTitle: true,
		BackURL:   "/orchestrate/evals",
		MaxWidth:  "100%",
		Sections: []ui.Section{
			{
				Title:    "What it grades",
				Subtitle: fmt.Sprintf("%s · %s", suite.TargetKind, suite.TargetID),
				Body: ui.FormPanel{
					Source:      "api/eval-suites/" + url_(suite.ID),
					PostURL:     "api/eval-suites/" + url_(suite.ID),
					SubmitLabel: "Save",
					Fields: []ui.FormField{
						{Field: "name", Type: "text", Label: "Name"},
						{Field: "desc", Type: "text", Label: "Description"},
						{Field: "runs", Type: "number", Label: "Runs per case",
							Help: "A single run of a non-deterministic model is an anecdote. Three is the smallest number that tells a flake from a failure."},
						{Field: "stub", Type: "toggle", Label: "Script tool results instead of running them",
							Help: "ON by default, and leave it on unless you mean it. With it off a run sends the emails, files the tickets and spends the money, every time anybody presses Run."},
						{Field: "cases", Type: "textarea", Label: "Cases (JSON)", Rows: 14,
							Help: "An array of {name, prompt, must_include, must_not_include, must_call_tools, must_not_call_tools, must_fields, judge_prompt}. " +
								"must_fields grades a pipeline or machine on its DECLARED output ({\"winner\": \"for\"}), which is a sharper test than searching its prose for a word."},
					},
				},
			},
			{
				Title:    "Run it",
				Wide:     true,
				NoChrome: true,
				Body: ui.PipelinePanel{
					SessionsListURL:  "api/evals/" + url_(suite.ID) + "/sessions",
					SessionLoadURL:   "api/evals/" + url_(suite.ID) + "/sessions/{id}",
					SessionDeleteURL: "api/evals/" + url_(suite.ID) + "/sessions/{id}",
					SubmitURL:        "api/evals/" + url_(suite.ID) + "/stream",
					CancelURL:        "api/evals/" + url_(suite.ID) + "/cancel",
					ReconnectURL:     "api/evals/" + url_(suite.ID) + "/reconnect/{id}",
					SubmitLabel:      "Run the suite",
					// This page's ?id= is the SUITE. Unnamed, the panel's
					// fallback reads it as a run id and opens one that cannot
					// exist.
					DeepLinkParam: "session",
					Fields: []ui.PipelineField{{
						Name: "topic", Type: "text",
						Label:       "What changed?",
						Placeholder: "Shortened the judge prompt — optional, but it is what makes a score in the list readable a month later",
					}},
					// The three that make a run list a SCORE list. They come
					// from the meta the run emits, so nothing here computes
					// them.
					SessionMetaFields: []ui.SessionMetaField{
						{Field: "Score", Style: "badge"},
						{Field: "Outcome", Style: "pill", Variants: map[string]string{
							"all passed": "#3fb950",
							"mixed":      "#e3b341",
							"all failed": "#f85149",
							"empty":      "#8b949e",
						}},
						{Field: "Version", Style: "text", Truncate: 8},
					},
					Markdown:   true,
					BulkSelect: true,
					EmptyText:  "No runs yet. The first one is the baseline everything after it is compared against.",
				},
			},
		},
	}
	page.ServeHTTP(w, r)
}

// --- the JSON the page reads --------------------------------------------------

// handleEvalSuitesAPI lists and creates suites.
func (T *OrchestrateApp) handleEvalSuitesAPI(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeEvalJSON(w, evalSuiteRows(udb))
	case http.MethodPost:
		var in EvalSuite
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		in.Owner = user
		saved, err := SaveEvalSuite(udb, in)
		if err != nil {
			// The validator's message is the useful part — it says which case
			// asserts nothing, or which field is missing.
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeEvalJSON(w, saved)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// evalSuiteRows renders the list, including the two columns that are the whole
// point: what it scored last, and whether that moved.
func evalSuiteRows(udb Database) []map[string]any {
	out := []map[string]any{}
	for _, s := range ListEvalSuites(udb) {
		row := map[string]any{
			"id": s.ID, "name": s.Name,
			"target": fmt.Sprintf("%s: %s", s.TargetKind, s.TargetID),
			"cases":  len(s.Cases),
			"last":   "never run",
			"trend":  "",
		}
		if runs := ListEvalRuns(udb, s.ID); len(runs) > 0 {
			row["last"] = runs[0].Rate()
			row["trend"] = evalTrend(runs)
		}
		out = append(out, row)
	}
	return out
}

// evalTrend compares the latest run against the most recent one that graded a
// DIFFERENT version.
//
// Not against the previous run: two runs of the same unchanged target differ by
// noise, and reporting that as movement is how a flaky case gets read as a
// regression. Comparing across a version boundary is the only comparison that
// answers "did the edit help".
func evalTrend(runs []EvalRun) string {
	latest := runs[0]
	for _, prev := range runs[1:] {
		if prev.TargetHash == latest.TargetHash || prev.Total == 0 {
			continue
		}
		delta := latest.Passed - prev.Passed
		switch {
		case delta > 0:
			return fmt.Sprintf("+%d (was %s)", delta, prev.Rate())
		case delta < 0:
			return fmt.Sprintf("%d (was %s)", delta, prev.Rate())
		default:
			return "no change (was " + prev.Rate() + ")"
		}
	}
	return "first version graded"
}

func writeEvalJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// handleEvalSuiteOne reads and updates ONE suite: /api/eval-suites/<id>.
//
// The form edits cases as JSON text, so the read renders them as text and the
// write parses them back. A textarea is not a great editor for a list of
// objects, and it is a considerably better one than a form that can only
// express the fields somebody thought of first — the case vocabulary already
// has seven assertion kinds and will grow.
func (T *OrchestrateApp) handleEvalSuiteOne(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/eval-suites/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	suite, found := LoadEvalSuite(udb, id)
	if !found {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodGet:
		cases, _ := json.MarshalIndent(suite.Cases, "", "  ")
		writeEvalJSON(w, map[string]any{
			"id": suite.ID, "name": suite.Name, "desc": suite.Desc,
			"runs": suite.RunCount(), "stub": suite.Stubbed(),
			"cases": string(cases),
		})
	case http.MethodPost, http.MethodPut:
		var in struct {
			Name  string `json:"name"`
			Desc  string `json:"desc"`
			Runs  int    `json:"runs"`
			Stub  *bool  `json:"stub"`
			Cases string `json:"cases"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var cases []EvalCase
		if strings.TrimSpace(in.Cases) != "" {
			if err := json.Unmarshal([]byte(in.Cases), &cases); err != nil {
				// Named as a JSON problem rather than a validation one: the
				// author is looking at a textarea, and "invalid suite" would
				// send them hunting through their cases for a missing comma.
				http.Error(w, "the cases are not valid JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		suite.Name, suite.Desc, suite.Runs, suite.Cases = in.Name, in.Desc, in.Runs, cases
		if in.Stub != nil {
			suite.Stub = in.Stub
		}
		suite.Owner = user
		saved, err := SaveEvalSuite(udb, suite)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeEvalJSON(w, map[string]any{"ok": true, "id": saved.ID})
	case http.MethodDelete:
		DeleteEvalSuite(udb, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
