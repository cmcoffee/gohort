package orchestrate

import (
	"strings"
	"testing"
)

// Builder reached for scripts every time the declarative path was not obvious:
// an action that flipped a status field, a shell tool that shelled out to run a
// pipeline, an html section with localStorage. The cause was not stubbornness —
// its own build guidance listed form, table, display, chart, workbench and
// html, and the section that RUNS a pipeline was not among them. A job with no
// section to map onto leaves a script as the only move.
func TestBuilderPromptOffersThePipelineSection(t *testing.T) {
	seed, ok := seedAgentByID("seed-builder")
	if !ok {
		t.Fatal("Builder seed is gone")
	}
	p := seed.OrchestratorPrompt
	if !strings.Contains(p, "pipeline_id") {
		t.Error("the app guidance never names pipeline_id, so a multi-stage app has nothing to bind")
	}
	if !strings.Contains(p, "ONLY thing that can run a pipeline") {
		t.Error("nothing rules out the three wrong shapes (action script, shell tool, html) that were each tried")
	}
	for _, wrong := range []string{"an action script cannot", "a shell tool cannot", "an html section cannot"} {
		if !strings.Contains(p, wrong) {
			t.Errorf("name the dead end explicitly — %q was actually attempted", wrong)
		}
	}
	// Reach order, so the general case is covered and not just this one.
	if !strings.Contains(p, "REACH IN THIS ORDER") {
		t.Error("no ordering rule: a typed section that fits must beat a script that reimplements it")
	}
}

// Three apps were announced with features they did not have — live streaming
// with no pipeline section, saved history with nothing writing records. That is
// not a verification failure (verify passed on all three); it is a summary
// written from intent instead of from what was stored.
func TestBuilderPromptRequiresHonestSummaries(t *testing.T) {
	seed, _ := seedAgentByID("seed-builder")
	p := seed.OrchestratorPrompt
	if !strings.Contains(p, "DESCRIBE ONLY WHAT YOU BUILT") {
		t.Fatal("nothing tells Builder to summarize from what is stored rather than from what it meant to do")
	}
	for _, cue := range []string{"do not say the stages stream live", "do not say history is saved"} {
		if !strings.Contains(p, cue) {
			t.Errorf("the rule needs the concrete claims that were actually made: %q", cue)
		}
	}
}

// A record-backed table beside a pipeline section stays empty forever. The save
// path reports it, but by then the app is built and the sentence is written —
// the prompt has to stop it being added in the first place.
func TestBuilderPromptWarnsOffTheRunsTable(t *testing.T) {
	seed, _ := seedAgentByID("seed-builder")
	if !strings.Contains(seed.OrchestratorPrompt, "NOT in the app's record store") {
		t.Error("nothing says where a pipeline's runs live, so a 'past runs' table keeps getting added")
	}
}

// A pipeline app's form fields are the run's parameters. Builder has to know
// that when it WRITES the pipeline, because the prompts have to name them —
// discovering it afterwards means re-authoring every stage.
func TestBuilderPromptExplainsPipelineParameters(t *testing.T) {
	seed, _ := seedAgentByID("seed-builder")
	p := seed.OrchestratorPrompt
	if !strings.Contains(p, "{field_name}") {
		t.Error("nothing tells Builder a submit field reaches the prompts, so it will ask for parameters it never uses")
	}
	if !strings.Contains(p, "Write the pipeline's prompts against those names") {
		t.Error("the ordering matters: the pipeline is authored against the form's names, not adapted to them later")
	}
}
