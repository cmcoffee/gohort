package servitor

// The silent failure this exists to catch: a tool minted WITHOUT a
// path_scope still works, and nothing about running it looks wrong.
// Rendering quotes the value so it cannot add shell syntax, which reads
// as safety and is not — "../../var/lib/x" is a perfectly well-formed
// single argument. Risk classifies the TEMPLATE, which is frozen and
// fine, and says nothing about arguments. So without a surface for it,
// the miss survives until somebody goes looking.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func pathTool(params map[string]ToolParam) ApplianceTool {
	return ApplianceTool{
		Name: "parse_logs", ApplianceID: "logs",
		Template: "logparse --dir {dir}", Params: params, Required: []string{"dir"},
	}
}

func TestUncheckedPathParamIsReported(t *testing.T) {
	tool := pathTool(map[string]ToolParam{
		"dir": {Type: "string", Description: "which folder"},
	})
	got := tool.UncheckedPathParams()
	if len(got) != 1 || got[0] != "dir" {
		t.Fatalf("expected dir to be flagged, got %v", got)
	}
	// And it reads as a warning on the row where approval happens,
	// wording the consequence rather than the mechanism.
	text := toolChecksText(tool)
	if !strings.Contains(text, "UNCHECKED PATH") {
		t.Errorf("the row should warn: %q", text)
	}
	if !strings.Contains(text, "outside") {
		t.Errorf("the warning should say what it risks: %q", text)
	}
}

func TestAConstrainedParamReadsAsChecked(t *testing.T) {
	tool := pathTool(map[string]ToolParam{
		"dir": {Type: "string", Description: "which folder", PathScope: "files:support_bundles"},
	})
	if got := tool.UncheckedPathParams(); len(got) != 0 {
		t.Errorf("a scoped param must not be flagged, got %v", got)
	}
	text := toolChecksText(tool)
	if !strings.Contains(text, "files:support_bundles") {
		t.Errorf("the row should name the root it is checked against: %q", text)
	}
	if strings.Contains(text, "UNCHECKED") {
		t.Errorf("a checked param must not read as unchecked: %q", text)
	}
}

// An enum is the other legitimate constraint, frozen but real.
func TestAnEnumCountsAsAConstraint(t *testing.T) {
	tool := pathTool(map[string]ToolParam{
		"dir": {Type: "string", Enum: []string{"today", "yesterday"}},
	})
	if got := tool.UncheckedPathParams(); len(got) != 0 {
		t.Errorf("an enum constrains the value, got %v", got)
	}
}

// Over-flagging costs a sentence on a row; under-flagging costs the
// thing this exists to catch. So the name match is deliberately
// generous, and a parameter that plainly is not a path stays quiet.
func TestOnlyPathishNamesAreFlagged(t *testing.T) {
	flagged := pathTool(map[string]ToolParam{
		"log_file": {Type: "string"}, "target_dir": {Type: "string"},
		"folder": {Type: "string"}, "bundle": {Type: "string"},
	}).UncheckedPathParams()
	if len(flagged) != 4 {
		t.Errorf("expected every path-shaped name flagged, got %v", flagged)
	}

	quiet := pathTool(map[string]ToolParam{
		"version": {Type: "string"}, "service": {Type: "string"}, "count": {Type: "number"},
	}).UncheckedPathParams()
	if len(quiet) != 0 {
		t.Errorf("non-path parameters should stay quiet, got %v", quiet)
	}
	// A tool with nothing to say contributes no column text at all,
	// rather than a reassuring blank that looks like a check.
	if text := toolChecksText(pathTool(map[string]ToolParam{"version": {Type: "string"}})); text != "" {
		t.Errorf("expected no text, got %q", text)
	}
}
