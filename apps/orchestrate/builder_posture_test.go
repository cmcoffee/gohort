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
// with no pipeline section, saved history with nothing writing records, a save
// button on an app with no actions. None were verification failures; verify
// passed on all three. They were summaries written from intent.
//
// The first version of this rule enumerated those false claims and told the
// author to re-read what it had stored. It did not hold: re-reading is a step,
// and a model that feels finished skips it. The rule now defers to a line the
// framework writes into every save and verify (see appInventoryLine), so the
// evidence arrives without being fetched — which is what the assertions here
// pin, rather than any particular list of past mistakes.
func TestBuilderPromptRequiresHonestSummaries(t *testing.T) {
	seed, _ := seedAgentByID("seed-builder")
	p := seed.OrchestratorPrompt
	if !strings.Contains(p, "DESCRIBE ONLY WHAT YOU BUILT") {
		t.Fatal("nothing bounds the summary to what was actually stored")
	}
	if !strings.Contains(p, "STORED —") {
		t.Error("the rule must point at the inventory line, or it is an instruction with no evidence attached")
	}
	// The escape hatch matters as much as the prohibition: an author that
	// cannot claim the missing thing needs somewhere to go other than silence.
	if !strings.Contains(p, "add it before you answer or tell them plainly it is missing") {
		t.Error("say what to do when the user's ask is not in the inventory")
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

// One build produced six apologies — four "My apologies", two "I apologize
// again" — none of which changed a single tool call. A refused validation is
// the normal way authoring works, not a transgression, and contrition spends
// the reader's attention on the author's feelings instead of their build.
func TestBuilderPromptForbidsApologizing(t *testing.T) {
	seed, _ := seedAgentByID("seed-builder")
	p := seed.OrchestratorPrompt
	if !strings.Contains(p, "DON'T APOLOGIZE") {
		t.Fatal("nothing tells Builder to stop apologizing")
	}
	// The rule has to say what to do INSTEAD, or it just suppresses a phrase
	// and leaves the turn shapeless.
	if !strings.Contains(p, "Say what was wrong and what you are changing") {
		t.Error("give the replacement behaviour, not only the prohibition")
	}
	// The specific habit worth naming: retry after retry, each opening with
	// contrition about the last one.
	if !strings.Contains(p, "never stack apologies across retries") {
		t.Error("the observed pattern was per-retry contrition; name it")
	}
}

// store_fact has been called zero times across 20MB of production logs, while
// the same validator refusals recur build after build: "boolean" instead of
// bool, a condition where a bool field name goes, the stage: prefix. The read
// path works and Explicit Memory is on — nothing is ever written to it.
//
// The old instruction asked for "a durable gotcha that VERIFIABLY worked",
// which is a judgement call made at the end of a long turn by a model that has
// just succeeded and wants to answer. The trigger is countable now.
func TestBuilderPromptSavesRepeatedValidatorRefusals(t *testing.T) {
	seed, _ := seedAgentByID("seed-builder")
	p := seed.OrchestratorPrompt
	if !strings.Contains(p, "SAVE ONE WHEN A VALIDATOR REFUSES YOU TWICE FOR THE SAME REASON") {
		t.Fatal("the save trigger is not countable; 'a durable gotcha' is a judgement nobody makes at turn's end")
	}
	// Rule, not incident — a stored "I got it wrong again" helps no future build.
	if !strings.Contains(p, "Write the rule, not the incident") {
		t.Error("say what to store; the shape of the fact is the whole value")
	}
	// Timing: after the turn ends there is no turn left to save in.
	if !strings.Contains(p, "before you answer") {
		t.Error("the save has to happen before the summary, or it does not happen")
	}
}

// A Forge build ended with "Here is what I have built so far: ... Pipeline
// Structure (Draft) — I defined a pipeline named forge_pipeline" after five
// consecutive refusals had stored exactly nothing. The STORED — line cannot
// catch this: it only appends to a SUCCESSFUL save, so a turn that saved
// nothing has nothing contradicting it. The prompt has to carry it.
func TestBuilderPromptDeniesCreditForRefusedCalls(t *testing.T) {
	seed, _ := seedAgentByID("seed-builder")
	p := seed.OrchestratorPrompt
	if !strings.Contains(p, "A REFUSED CALL BUILT NOTHING") {
		t.Fatal("a refused definition is not a draft; the summary must not claim it")
	}
	// The same build then asked whether to keep trying, mid-repair.
	if !strings.Contains(p, "don't stop to ask permission to fix your own error") {
		t.Error("a validator refusal is a step, not a decision point for the user")
	}
}
