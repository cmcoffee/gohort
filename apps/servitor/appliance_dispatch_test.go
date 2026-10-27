// The gates a minted tool passes before anything reaches a machine. Each test
// here is a way the command could run when it should not, or be refused in a
// way the caller cannot act on.
package servitor

import (
	"context"
	"strconv"
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
		// The category the template classifies as, as a real mint stamps it.
		Required: []string{"service"}, Risk: RiskSysControl,
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
	defs := ApplianceToolDefs(nil, udb, "craig", "wren", labBox())
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

// The three agent-facing servitor tools all dispatch work that outlives a
// function call — an investigation, a command on the box, an LLM round to
// draft a capability. Each takes the turn's session so a Stop reaches
// them; this locks the source that makes that possible, since the wiring
// is invisible at the call site and a refactor that drops the parameter
// would compile fine and silently detach the work again.
func TestServitorSourceIsSessionAware(t *testing.T) {
	var _ SessionReferenceToolProvider = servitorSource{}
	var _ ReferenceToolProvider = servitorSource{}
}

// The dispatch has to hand the CALLING agent to the path-scope check.
// Passing only the user is what let an agent nobody had linked a store to
// run a command against it — the tool's own gate (the appliance
// connection) is a different grant from the folder's.

func TestScopedArgsCarryTheCallingAgent(t *testing.T) {
	prevHolds := AgentHoldsReference
	t.Cleanup(func() { AgentHoldsReference = prevHolds })
	AgentHoldsReference = func(user, agentID, kind, itemID string) bool { return agentID == "linked" }

	var sawUser, sawName, sawValue string
	RegisterPathScope("testscope", PathScope{
		Resolve: func(u, n, v string) (string, error) {
			sawUser, sawName, sawValue = u, n, v
			return "/abs/" + v, nil
		},
	})
	// The gate only applies to kinds that are ALSO attachable sources —
	// otherwise there is nothing to attach and requiring it would break a
	// scope with no picker behind it.
	RegisterReferenceSource(scopeTestSource{})

	tool := ApplianceTool{
		Name:   "parse_bundle",
		Params: map[string]ToolParam{"dir": {Type: "string", PathScope: "testscope:bundles"}},
	}
	args := map[string]any{"dir": "scan-1", "other": "left alone"}

	out, err := resolveScopedArgs("alice", "linked", tool, args, false)
	if err != nil {
		t.Fatalf("linked agent refused: %v", err)
	}
	if out["dir"] != "/abs/scan-1" {
		t.Errorf("scoped arg not substituted: %v", out["dir"])
	}
	if out["other"] != "left alone" {
		t.Errorf("unscoped arg was touched: %v", out["other"])
	}
	// The caller's map is what gets logged and echoed back; rewriting it
	// in place would make the record disagree with what the model asked.
	if args["dir"] != "scan-1" {
		t.Errorf("the caller's map was mutated: %v", args["dir"])
	}
	if sawUser != "alice" || sawName != "bundles" || sawValue != "scan-1" {
		t.Errorf("resolver got (%q,%q,%q)", sawUser, sawName, sawValue)
	}

	if _, err := resolveScopedArgs("alice", "unlinked", tool, args, false); err == nil {
		t.Fatal("an unlinked agent resolved a scoped path")
	} else if !strings.Contains(err.Error(), "dir") {
		t.Errorf("the refusal should name the parameter, got %q", err)
	}
}

type scopeTestSource struct{}

func (scopeTestSource) Kind() string                                         { return "testscope" }
func (scopeTestSource) Label() string                                        { return "Test scope" }
func (scopeTestSource) List(string) []ReferenceItem                          { return nil }
func (scopeTestSource) Fetch(context.Context, string, string, string) string { return "" }

// The constraint has to be visible to the model that has to satisfy it.
// Without the names in the description the parameter reads "which bundle
// to parse", the scope is enforced somewhere the model cannot see, and
// the first call is a guess that gets refused.
func TestScopedParamDescriptionCarriesTheFolders(t *testing.T) {
	RegisterPathScope("listscope", PathScope{
		Resolve: func(u, n, v string) (string, error) { return "/abs/" + v, nil },
		Values:  func(u, n string) []string { return []string{"scan-b", "scan-a"} },
	})
	params := map[string]ToolParam{
		"dir":   {Type: "string", Description: "Which bundle to parse.", PathScope: "listscope:bundles"},
		"quiet": {Type: "boolean", Description: "Less output."},
	}
	got := describeScopedParams("u", params, map[string][]string{})

	if got["dir"].Description == params["dir"].Description {
		t.Fatal("the scoped parameter's description was not enriched")
	}
	for _, want := range []string{"Which bundle to parse", "scan-a", "scan-b"} {
		if !strings.Contains(got["dir"].Description, want) {
			t.Errorf("description missing %q: %s", want, got["dir"].Description)
		}
	}
	// Sorted by PathScopeChoices, so the list does not reshuffle between
	// turns and invalidate the prompt cache for no reason.
	if strings.Index(got["dir"].Description, "scan-a") > strings.Index(got["dir"].Description, "scan-b") {
		t.Error("folder list should be sorted")
	}
	// A snapshot that admits it is one. A model handed a list treats it as
	// exhaustive otherwise, and refuses the folder somebody just named.
	if !strings.Contains(got["dir"].Description, "still works") {
		t.Error("description should say a newer folder still resolves")
	}
	if got["quiet"].Description != "Less output." {
		t.Errorf("an unscoped parameter was rewritten: %q", got["quiet"].Description)
	}
	// The stored record must not be touched — it is the frozen tool, and
	// this text is one moment's listing of a directory.
	if params["dir"].Description != "Which bundle to parse." {
		t.Errorf("the stored params were mutated: %q", params["dir"].Description)
	}
	// Second call for the same root reuses the memo rather than re-listing
	// once per tool on the same machine.
	memo := map[string][]string{"listscope:bundles": {"cached-only"}}
	if d := describeScopedParams("u", params, memo)["dir"].Description; !strings.Contains(d, "cached-only") {
		t.Errorf("memo not consulted: %s", d)
	}
}

// An empty root is not "no valid values". A model told that concludes the
// tool is broken and stops; told what is true, it can say so.
func TestEmptyScopeSaysWhatIsActuallyTrue(t *testing.T) {
	hint := scopeHint(nil)
	if strings.Contains(strings.ToLower(hint), "no valid") {
		t.Errorf("hint reads as a broken tool: %s", hint)
	}
	if !strings.Contains(hint, "until a folder appears") {
		t.Errorf("hint should say what would change it: %s", hint)
	}
	// And a long list is capped, with the remainder acknowledged rather
	// than silently dropped.
	many := make([]string, maxListedFolders+7)
	for i := range many {
		many[i] = "f" + strconv.Itoa(i)
	}
	long := scopeHint(many)
	if !strings.Contains(long, "7 more not listed") {
		t.Errorf("truncation should say how many were dropped: %s", long)
	}
}
