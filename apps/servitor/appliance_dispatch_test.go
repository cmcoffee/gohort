// The gates a minted tool passes before anything reaches a machine. Each test
// here is a way the command could run when it should not, or be refused in a
// way the caller cannot act on.
package servitor

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func labBox() Appliance { return Appliance{ID: "lab-box", Name: "Lab Box"} }

func approvedTool(t *testing.T, udb Database) ApplianceTool {
	t.Helper()
	if _, err := SaveApplianceTool(udb, ApplianceTool{
		Name: "restart_web", ApplianceID: "lab-box", Description: "Restart the web service",
		Template: "sudo systemctl restart {service}",
		Params:   map[string]ToolParam{"service": {Type: "string", Enum: []string{"nginx", "apache2"}}},
		Required: []string{"service"}, Risk: AllRiskCategories[0],
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	ApproveApplianceTool(udb, "lab-box", "restart_web", true)
	got, _ := LoadApplianceTool(udb, "lab-box", "restart_web")
	return got
}

// An unapproved tool must not reach a connection, and the refusal must not read
// as "queued" — nobody is coming back to it.
func TestUnapprovedToolIsRefusedBeforeConnecting(t *testing.T) {
	udb := grantStore(t)
	if _, err := SaveApplianceTool(udb, ApplianceTool{
		Name: "wipe_logs", ApplianceID: "lab-box", Template: "rm -f /var/log/app.log",
	}); err != nil {
		t.Fatal(err)
	}
	_, err := DispatchApplianceTool(context.Background(), udb, ApplianceDispatch{
		Appliance: labBox(), ToolName: "wipe_logs", AgentID: "wren",
	})
	if err == nil {
		t.Fatal("an unapproved tool must not run")
	}
	if !strings.Contains(err.Error(), "not approved") {
		t.Errorf("the refusal should name approval as the blocker, got %v", err)
	}
	if !strings.Contains(err.Error(), "nothing is queued") {
		t.Errorf("the refusal must not imply something is pending, got %v", err)
	}
}

// A tool nobody minted is a different failure from one nobody approved, and
// the caller needs to tell them apart.
func TestUnknownToolSaysHowToGetOne(t *testing.T) {
	udb := grantStore(t)
	_, err := DispatchApplianceTool(context.Background(), udb, ApplianceDispatch{
		Appliance: labBox(), ToolName: "nope", AgentID: "wren",
	})
	if err == nil || !strings.Contains(err.Error(), "ask for the capability first") {
		t.Errorf("an unknown tool should point at minting, got %v", err)
	}
}

// A bad argument must read as a bad argument, not as missing access — or the
// model concludes it lacks permission and stops trying.
func TestArgumentErrorIsNotMistakenForPermission(t *testing.T) {
	udb := grantStore(t)
	approvedTool(t, udb)
	_, err := DispatchApplianceTool(context.Background(), udb, ApplianceDispatch{
		Appliance: labBox(), ToolName: "restart_web", AgentID: "wren",
		Args: map[string]any{"service": "postgres"}, // outside the enum
	})
	if err == nil {
		t.Fatal("a value outside the enum must be refused")
	}
	msg := err.Error()
	if !strings.Contains(msg, "nginx") {
		t.Errorf("the error should name what IS allowed, got %v", err)
	}
	if strings.Contains(msg, "standing permission") {
		t.Errorf("an argument problem must not read as a permission problem, got %v", err)
	}
}

// REPLACES two tests that asserted an approved tool ALSO needed a risk-category
// grant. That was double-gating, and it made the recommended configuration —
// approve each command, tick no categories — the one that did not work: the
// only way to use an approved capability was to grant a broad category
// permitting far more than the command that had been read and approved.
//
// Approving a minted tool is the stronger act, so it is the gate.
func TestApprovedToolRunsWithNoCategoryGranted(t *testing.T) {
	udb := grantStore(t)
	approvedTool(t, udb)
	// No SaveCommandGrant at all: nothing is permitted by category.
	_, err := DispatchApplianceTool(context.Background(), udb, ApplianceDispatch{
		Appliance: labBox(), ToolName: "restart_web", AgentID: "wren",
		Args: map[string]any{"service": "nginx"},
	})
	// It gets as far as dialling, which is the proof: the permission gates
	// passed and only the (absent) machine stopped it.
	if err == nil {
		t.Fatal("expected the connection to fail in a test, not the gate to pass silently")
	}
	for _, permissionWording := range []string{"standing permission", "auto-run settings", "Permissions"} {
		if strings.Contains(err.Error(), permissionWording) {
			t.Errorf("an approved tool must not be refused on permissions, got %v", err)
		}
	}
	if !strings.Contains(err.Error(), "could not reach") {
		t.Errorf("expected to reach the connection attempt, got %v", err)
	}
}

// The stored risk and the rendered command must agree, and a disagreement is
// refused before anything is dialled.
//
// NOTE ON WHAT THIS COULD NOT TEST. The first version tried to push a command
// past its approval through an ARGUMENT — passing "rm -rf /var/lib/data" into
// an echo template — and it could not: the value is quoted, so the classifier
// reads `echo` and the render stays exactly as risky as it was approved. That
// is the quoting doing its job, and it means the raise path is nearly
// unreachable from outside.
//
// It is kept anyway, and tested here from the stored side: a record whose Risk
// no longer matches what its template classifies as — an older classifier, an
// edited record — must not run on the strength of the stale number.
func TestStoredRiskMustMatchTheRenderedCommand(t *testing.T) {
	udb := grantStore(t)
	if _, err := SaveApplianceTool(udb, ApplianceTool{
		Name: "clear_logs", ApplianceID: "lab-box",
		Template: "rm -f /var/log/app.log",
		Risk:     RiskNone, // stale: says harmless, the template is not
	}); err != nil {
		t.Fatal(err)
	}
	ApproveApplianceTool(udb, "lab-box", "clear_logs", true)

	_, err := DispatchApplianceTool(context.Background(), udb, ApplianceDispatch{
		Appliance: labBox(), ToolName: "clear_logs", AgentID: "wren",
	})
	if err == nil {
		t.Fatal("a command riskier than its stored approval must not run")
	}
	if !strings.Contains(err.Error(), "approved as") || !strings.Contains(err.Error(), "Nothing ran") {
		t.Errorf("the refusal should name the mismatch and say nothing happened, got %v", err)
	}
	if strings.Contains(err.Error(), "could not reach") {
		t.Error("it must be refused BEFORE any connection is attempted")
	}
}

// Re-classification may only raise. An owner approved a category; a crafted
// value must not be able to talk the command back down below it.
func TestRiskRankOrdersAndNeverTrustsTheUnknown(t *testing.T) {
	if riskRank(RiskNone) != 0 {
		t.Error("no risk should rank lowest")
	}
	if riskRank(AllRiskCategories[0]) <= riskRank(RiskNone) {
		t.Error("a known category outranks none")
	}
	if riskRank(RiskCategory("category_from_a_newer_build")) <= riskRank(AllRiskCategories[len(AllRiskCategories)-1]) {
		t.Error("an unrecognized category must rank above everything known, not below")
	}
}

// The catalog only ever offers what can actually run.
func TestOnlyApprovedToolsBecomeDefs(t *testing.T) {
	udb := grantStore(t)
	approvedTool(t, udb)
	if _, err := SaveApplianceTool(udb, ApplianceTool{
		Name: "pending_one", ApplianceID: "lab-box", Template: "uptime",
	}); err != nil {
		t.Fatal(err)
	}
	defs := ApplianceToolDefs(udb, "craig", "wren", labBox())
	if len(defs) != 1 || defs[0].Tool.Name != "restart_web" {
		t.Fatalf("only the approved tool should be offered, got %d: %+v", len(defs), defs)
	}
	// The description has to say WHERE, or two boxes' tools are indistinguishable.
	if !strings.Contains(defs[0].Tool.Description, "Lab Box") {
		t.Errorf("the description should name the machine, got %q", defs[0].Tool.Description)
	}
	// And it must declare that it writes, or the premise gate and the capability
	// filters treat it as harmless.
	var writes bool
	for _, c := range defs[0].Tool.Caps {
		if c == CapWrite {
			writes = true
		}
	}
	if !writes {
		t.Error("an appliance tool must declare CapWrite")
	}
}
