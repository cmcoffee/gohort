// A minted tool is a frozen command plus typed holes. The tests that matter are
// the ones where a wrong answer either runs something nobody read, or refuses
// something that was approved and then looks broken.
package servitor

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func mintedTool() ApplianceTool {
	return ApplianceTool{
		Name: "deploy_acme", ApplianceID: "lab-box",
		Template: "/opt/acme/deploy --env {env} --version {version}",
		Params: map[string]ToolParam{
			"env":     {Type: "string", Enum: []string{"staging", "qa"}},
			"version": {Type: "string"},
		},
		Required: []string{"env", "version"},
	}
}

// THE property the whole design rests on: a value cannot become syntax.
func TestValuesCannotBecomeSyntax(t *testing.T) {
	cmd, err := renderApplianceCommand(mintedTool(), map[string]any{
		"env": "staging", "version": "; rm -rf / #",
	})
	if err != nil {
		t.Fatalf("a hostile value should render, not error: %v", err)
	}
	if strings.Contains(cmd, "; rm -rf / #'") && !strings.Contains(cmd, "'; rm -rf / #'") {
		t.Fatal("value escaped its quoting")
	}
	if !strings.Contains(cmd, `'; rm -rf / #'`) {
		t.Errorf("the value should arrive as one quoted argument, got %q", cmd)
	}
	// Structure is untouched.
	if !strings.HasPrefix(cmd, "/opt/acme/deploy --env ") {
		t.Errorf("template structure changed: %q", cmd)
	}
}

// An embedded single quote is where naive quoting breaks.
func TestEmbeddedQuoteIsEscaped(t *testing.T) {
	cmd, err := renderApplianceCommand(mintedTool(), map[string]any{
		"env": "qa", "version": `4.2'; whoami; echo '`,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(cmd, "; whoami;") && !strings.Contains(cmd, `'\''`) {
		t.Errorf("an embedded quote must be escaped, got %q", cmd)
	}
}

// The enum is the only thing that catches a value dangerous in the APP's terms
// rather than the shell's — "production" is perfectly well-quoted.
func TestEnumRejectsAValueOutsideTheSet(t *testing.T) {
	_, err := renderApplianceCommand(mintedTool(), map[string]any{"env": "production", "version": "4.2"})
	if err == nil {
		t.Fatal("a value outside the declared set must be refused")
	}
	if !strings.Contains(err.Error(), "staging") {
		t.Errorf("the refusal should name what IS allowed, got %v", err)
	}
}

func TestRequiredValuesAreEnforced(t *testing.T) {
	if _, err := renderApplianceCommand(mintedTool(), map[string]any{"env": "qa"}); err == nil {
		t.Error("a missing required value must not render a half-formed command")
	}
	if _, err := renderApplianceCommand(mintedTool(), map[string]any{"env": "qa", "version": "  "}); err == nil {
		t.Error("a blank required value is a missing one")
	}
}

// A number emits bare so a template expecting an integer still works — but only
// when it really is one.
func TestNumericEmitsBareOnlyWhenNumeric(t *testing.T) {
	tool := ApplianceTool{
		Name: "tail_log", ApplianceID: "b", Template: "tail -n {lines} /var/log/syslog",
		Params: map[string]ToolParam{"lines": {Type: "integer"}}, Required: []string{"lines"},
	}
	cmd, err := renderApplianceCommand(tool, map[string]any{"lines": 50})
	if err != nil || !strings.Contains(cmd, "tail -n 50 ") {
		t.Errorf("a real number should emit bare, got %q (%v)", cmd, err)
	}
	// Declared integer, but a string carrying syntax: quote it.
	cmd, err = renderApplianceCommand(tool, map[string]any{"lines": "50; whoami"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(cmd, `'50; whoami'`) {
		t.Errorf("a non-numeric value declared integer must still be quoted, got %q", cmd)
	}
}

// Both directions of template/param disagreement are real mistakes.
func TestTemplateAndParamsMustAgree(t *testing.T) {
	err := validateTemplateParams("systemctl restart {service}", nil)
	if err == nil || !strings.Contains(err.Error(), "no matching parameter") {
		t.Errorf("a placeholder with no parameter must be refused, got %v", err)
	}
	err = validateTemplateParams("systemctl restart nginx", map[string]ToolParam{"service": {Type: "string"}})
	if err == nil || !strings.Contains(err.Error(), "collected and discarded") {
		t.Errorf("a parameter that goes nowhere must be refused, got %v", err)
	}
	if err := validateTemplateParams("systemctl restart {service}", map[string]ToolParam{"service": {Type: "string"}}); err != nil {
		t.Errorf("a consistent pair should validate, got %v", err)
	}
}

// Minting is a proposal. Asking for a capability must not be the same act as
// being granted it.
func TestSavedToolIsNeverApprovedOnWrite(t *testing.T) {
	udb := grantStore(t)
	in := mintedTool()
	in.Approved = true // a caller trying to approve its own request
	saved, err := SaveApplianceTool(udb, in)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.Approved {
		t.Error("a write must not be able to approve")
	}
	got, ok := LoadApplianceTool(udb, "lab-box", "deploy_acme")
	if !ok || got.Approved {
		t.Errorf("stored record must be unapproved, got ok=%v approved=%v", ok, got.Approved)
	}
	if !ApproveApplianceTool(udb, "lab-box", "deploy_acme", true) {
		t.Fatal("the owner's approval path should find the tool")
	}
	if got, _ := LoadApplianceTool(udb, "lab-box", "deploy_acme"); !got.Approved {
		t.Error("approval did not persist")
	}
}

func TestUnusableNamesAndUnboundToolsAreRefused(t *testing.T) {
	udb := grantStore(t)
	for _, bad := range []string{"Deploy Acme", "x", "deploy/acme", ""} {
		in := mintedTool()
		in.Name = bad
		if _, err := SaveApplianceTool(udb, in); err == nil {
			t.Errorf("name %q should be refused", bad)
		}
	}
	unbound := mintedTool()
	unbound.ApplianceID = ""
	if _, err := SaveApplianceTool(udb, unbound); err == nil {
		t.Error("a tool must be bound to a system")
	}
}

// Two boxes can hold the same capability without colliding.
func TestSameCapabilityOnTwoSystems(t *testing.T) {
	udb := grantStore(t)
	a, b := mintedTool(), mintedTool()
	b.ApplianceID = "prod-box"
	if _, err := SaveApplianceTool(udb, a); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveApplianceTool(udb, b); err != nil {
		t.Fatal(err)
	}
	if got := ListApplianceTools(udb, ""); len(got) != 2 {
		t.Errorf("both should persist, got %d", len(got))
	}
	if got := ListApplianceTools(udb, "prod-box"); len(got) != 1 || got[0].ApplianceID != "prod-box" {
		t.Errorf("listing should narrow to one system, got %+v", got)
	}
}
