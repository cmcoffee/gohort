package servitor

import (
	"strings"
	"testing"
)

// A tool description is the ONLY thing an agent knows about a capability. It
// can describe the capability perfectly and still be unusable if it never names
// the thing the owner will ask for by name — which is what happened: an agent
// holding ask_system reported it had "no Servitor tools at all", which was true
// of the vocabulary it had been given and wrong about what it could do.

func vocabAppliances() []Appliance {
	return []Appliance{
		{ID: "ws-1", Name: "Lab Estate", Type: "workspace"},
		{ID: "box-1", Name: "lab-box", Type: "ssh"},
	}
}

// TestTheQuestionToolNamesServitor — the owner says "ask Servitor"; the tool has
// to contain that word for the agent to make the connection.
func TestTheQuestionToolNamesServitor(t *testing.T) {
	def := AskSystemToolDef(nil, "craig", "a1", vocabAppliances(), nil)
	desc := def.Tool.Description
	if !strings.Contains(desc, "Servitor") {
		t.Errorf("ask_system never names Servitor, so an agent cannot map the owner's word "+
			"onto the tool it holds:\n%s", desc)
	}
	// The nouns an owner actually uses for the thing they want asked about.
	for _, word := range []string{"machines", "servers", "infrastructure"} {
		if !strings.Contains(strings.ToLower(desc), word) {
			t.Errorf("ask_system does not mention %q, a word an owner will use for the target:\n%s", word, desc)
		}
	}
}

// TestAWorkspaceIsNotDescribedAsAMachine — the reason this came up. A workspace
// reads exactly like a host in a list of "systems", while asking it actually
// fans the question across every member. An agent front-ending a workspace has
// to be told that, or it answers about the estate one machine at a time.
func TestAWorkspaceIsNotDescribedAsAMachine(t *testing.T) {
	list := connectedSystemList(vocabAppliances(), nil)
	if !strings.Contains(list, "Lab Estate") {
		t.Fatalf("the workspace is missing from the list: %s", list)
	}
	if !strings.Contains(list, "GROUP") {
		t.Errorf("a workspace is listed as though it were one machine:\n%s", list)
	}
	// A plain host must NOT be annotated — noise on every entry is how a
	// description stops being read.
	if strings.Contains(list, "lab-box (") {
		t.Errorf("an ordinary ssh host was annotated: %s", list)
	}
}

// TestBothToolsShareOneList — they name the same appliances to the same agent,
// and the two lists drifting is how one tool learns about workspaces and the
// other does not.
func TestBothToolsShareOneList(t *testing.T) {
	ask := AskSystemToolDef(nil, "craig", "a1", vocabAppliances(), nil)
	req := RequestCapabilityToolDef(nil, nil, "a1", vocabAppliances())
	list := connectedSystemList(vocabAppliances(), nil)
	for name, desc := range map[string]string{
		"ask_system":         ask.Tool.Description,
		"request_capability": req.Tool.Description,
	} {
		if !strings.Contains(desc, list) {
			t.Errorf("%s does not render the shared connected-systems list:\n%s", name, desc)
		}
	}
}

// TestEveryApplianceKindReadsAsItself — a bundle and a live host answer very
// differently, and an agent choosing between them has only this to go on.
func TestEveryApplianceKindReadsAsItself(t *testing.T) {
	for _, typ := range []string{"workspace", "repo", "bundle", "toolset", "command"} {
		if applianceKindNote(Appliance{Type: typ}) == "" {
			t.Errorf("appliance type %q has no note, so it is indistinguishable from an SSH host", typ)
		}
	}
	// ssh is the default and needs no annotation.
	if applianceKindNote(Appliance{Type: "ssh"}) != "" {
		t.Error("a plain ssh host is annotated, adding noise to every entry")
	}
	if applianceKindNote(Appliance{}) != "" {
		t.Error("an untyped (legacy) appliance is annotated")
	}
}

// TestAWorkspaceMemberSaysHowItIsReached — a member listed with no explanation
// looks like an independent connection, and the agent loses the one fact that
// tells it when to use the group instead.
func TestAWorkspaceMemberSaysHowItIsReached(t *testing.T) {
	via := map[string]string{"box-1": "Lab Estate"}
	list := connectedSystemList(vocabAppliances(), via)
	if !strings.Contains(list, "member of Lab Estate") {
		t.Errorf("a workspace member does not say how it is reachable:\n%s", list)
	}
	// The workspace keeps its own annotation — both facts have to survive.
	if !strings.Contains(list, "GROUP") {
		t.Errorf("the workspace lost its group annotation:\n%s", list)
	}
}
