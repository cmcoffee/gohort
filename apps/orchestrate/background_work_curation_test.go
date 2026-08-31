package orchestrate

import (
	"regexp"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// background_work is force-included for every agent that has tools at all, so
// offering it in the curation picker was a switch that did nothing: turn it
// off, save, and the runner adds it back on the next turn.
//
// It is not a capability. It is the brake on one the framework grants
// unilaterally — detaching is the framework's decision, not the agent's — and
// withholding a brake does not make an agent do less, it makes an agent that
// starts work nobody can stop.
func TestBackgroundWorkIsNotOfferedForCuration(t *testing.T) {
	for _, opt := range availableWorkerToolOptions("") {
		if opt.Value == "background_work" {
			t.Fatal("background_work is in the tool picker, but the runner force-includes it — the switch does nothing")
		}
	}
}

// The sweep. Any tool the framework wires in unilaterally must be hidden from
// the picker, or the picker offers a switch that does not switch: uncheck it,
// save, and the next turn adds it back.
//
// Source-scanned rather than listed here, because a list in a test is a list
// that goes stale the first time somebody force-includes a fifth tool without
// reading this file. The regex finds the literal names appended to the
// catalog, which is the shape every force-include in these files takes.
func TestNoForceIncludedToolIsOfferedForCuration(t *testing.T) {
	offered := map[string]bool{}
	for _, opt := range availableWorkerToolOptions("") {
		offered[opt.Value] = true
	}
	literal := regexp.MustCompile(`toolNames = append\(toolNames, "([a-z_]+)"\)`)
	seen := 0
	for _, file := range []string{"runner.go", "agent_dispatch.go", "scheduled_updates.go"} {
		for _, m := range literal.FindAllStringSubmatch(readSourceFile(t, file), -1) {
			seen++
			if offered[m[1]] {
				t.Errorf("%s force-includes %q AND the picker offers it — unchecking it does nothing", file, m[1])
			}
		}
	}
	if seen == 0 {
		t.Fatal("found no force-included tools at all; the scan pattern has drifted from the code")
	}
}

// The runner's force-include is the other half. If this ever stops happening,
// hiding it from the picker turns a dead control into a missing one.
func TestBackgroundWorkIsForceIncluded(t *testing.T) {
	src := readSourceFile(t, "runner.go")
	if !strings.Contains(src, `toolNames = append(toolNames, "background_work")`) {
		t.Error("the runner no longer force-includes background_work; hiding it from the picker now removes it entirely")
	}
	// Still excluded by the no-tools sentinel — an agent with no tools has
	// nothing to start, so it needs nothing to stop.
	if !strings.Contains(src, `!isNoToolsSentinel(t.agent.AllowedTools) && !slices.Contains(toolNames, "background_work")`) {
		t.Error("the no-tools sentinel should still withhold it")
	}
}

// Hidden from the picker, but the agent must actually receive it.
func TestBackgroundWorkStillResolvesByName(t *testing.T) {
	var found bool
	for _, tool := range RegisteredChatTools() {
		if tool.Name() == "background_work" {
			found = true
			if !IsFrameworkTool(tool) {
				t.Error("background_work should declare itself a framework tool")
			}
		}
	}
	if !found {
		t.Error("background_work must stay REGISTERED — the runner resolves it by name")
	}
}
