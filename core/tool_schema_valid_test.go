package core

import (
	"strings"
	"testing"
)

// Gemini rejects an enum containing an empty string:
//
//	GenerateContentRequest.tools[0].function_declarations[67]
//	  .parameters.properties[surface].enum[0]: cannot be empty
//
// And it rejects the WHOLE request, so one bad param anywhere in the catalog
// takes every tool down with it — the agent loses all tool use, not just that
// tool. "Optional, pass an empty string for the default" is a natural way to
// write a schema and a fatal one; the default belongs in the description, with
// the caller omitting the param.
//
// walkToolParams checks every param a tool declares, nested objects included.
func assertNoEmptyEnumValue(t *testing.T, toolName string, params map[string]ToolParam) {
	t.Helper()
	for name, p := range params {
		for i, v := range p.Enum {
			if strings.TrimSpace(v) == "" {
				t.Errorf("tool %q param %q enum[%d] is empty — Gemini rejects the entire request, disabling every tool in the catalog. Drop the empty value and document the default in the description instead.",
					toolName, name, i)
			}
		}
		if p.Properties != nil {
			assertNoEmptyEnumValue(t, toolName+"."+name, p.Properties)
		}
		if p.Items != nil {
			for i, v := range p.Items.Enum {
				if strings.TrimSpace(v) == "" {
					t.Errorf("tool %q param %q items.enum[%d] is empty", toolName, name, i)
				}
			}
		}
	}
}

// The rule itself, pinned independently of what happens to be registered —
// the offending tool lived in a package this test binary may not import.
func TestEmptyEnumValueIsRejectedByTheCheck(t *testing.T) {
	fake := map[string]ToolParam{
		"surface": {Type: "string", Enum: []string{"", "session"}},
	}
	sub := &testing.T{}
	assertNoEmptyEnumValue(sub, "fake_tool", fake)
	if !sub.Failed() {
		t.Fatal("the check does not actually catch an empty enum value")
	}

	ok := map[string]ToolParam{
		"surface": {Type: "string", Enum: []string{"session", "cortex"}},
	}
	sub2 := &testing.T{}
	assertNoEmptyEnumValue(sub2, "fake_tool", ok)
	if sub2.Failed() {
		t.Error("the check fires on a valid enum")
	}
}
