package orchestrate

// The two powers an administrator gets over agent security, and the line
// between them.

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

func deploymentTestDB(t *testing.T) Database {
	t.Helper()
	prior := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prior })
	return RootDB
}

// The chain gained a rung, under the owner and over the framework constant.
func TestADeploymentDefaultSitsUnderTheOwnersOwn(t *testing.T) {
	db := deploymentTestDB(t)
	blank := AgentRecord{ID: "a", Owner: "alice"}

	// Nothing set anywhere: the framework's answer, which is open.
	if got := resolveSetting(db, blank, defaultWorkspaceNetwork); got != settingOn {
		t.Errorf("a deployment that sets nothing changed behaviour: %q", got)
	}

	// The deployment speaks, and a fleet nobody has touched reads it.
	setDeploymentSetting(db, deploymentDefault, defaultWorkspaceNetwork, settingOff)
	if got := resolveSetting(db, blank, defaultWorkspaceNetwork); got != settingOff {
		t.Errorf("the deployment default was ignored: %q", got)
	}
	if src := settingSource(db, blank, defaultWorkspaceNetwork); !strings.Contains(src, "Default: Blocked") {
		t.Errorf("the page does not say what the agent is following: %q", src)
	}

	// The AGENT outranks it, in either direction - a default is a starting
	// point, and one that could not be moved would not be one.
	own := AgentRecord{ID: "a", Owner: "alice", WorkspaceNetwork: settingOn}
	if got := resolveSetting(db, own, defaultWorkspaceNetwork); got != settingOn {
		t.Error("an agent could not widen off a deployment DEFAULT, which makes it a ceiling by accident")
	}
	own.WorkspaceNetwork = settingOff
	if got := resolveSetting(db, own, defaultWorkspaceNetwork); got != settingOff {
		t.Error("the agent's own answer stopped winning")
	}
}

// The maximum is the one that constrains, and it constrains everybody.
func TestTheDeploymentMaximumCannotBeWidenedAway(t *testing.T) {
	db := deploymentTestDB(t)
	// An agent whose owner has opened it.
	open := AgentRecord{ID: "a", Owner: "alice", WorkspaceNetwork: settingOn}
	if got := resolveSetting(db, open, defaultWorkspaceNetwork); got != settingOn {
		t.Fatalf("setup: %q", got)
	}

	setDeploymentSetting(db, deploymentMaximum, defaultWorkspaceNetwork, settingOff)
	if got := resolveSetting(db, open, defaultWorkspaceNetwork); got != settingOff {
		t.Errorf("an agent resolved looser than the deployment maximum: %q", got)
	}
	src := settingSource(db, open, defaultWorkspaceNetwork)
	if !strings.Contains(src, "Held at Blocked by the deployment limit") {
		t.Errorf("the page shows a value without saying the ceiling is holding it: %q", src)
	}

	// Lifting the ceiling gives the agent back what it had. Its own setting
	// was never rewritten, so nobody has to remember what it used to be.
	setDeploymentSetting(db, deploymentMaximum, defaultWorkspaceNetwork, "")
	if got := resolveSetting(db, open, defaultWorkspaceNetwork); got != settingOn {
		t.Error("lifting the ceiling left the agent clamped, so the clamp rewrote the record")
	}
}

// A ceiling never LOOSENS. It is a maximum, so an agent already stricter than
// it stays where it is.
func TestTheMaximumOnlyEverTightens(t *testing.T) {
	db := deploymentTestDB(t)
	shut := AgentRecord{ID: "a", Owner: "alice", WorkspaceNetwork: settingOff}
	setDeploymentSetting(db, deploymentMaximum, defaultWorkspaceNetwork, settingOn)
	if got := resolveSetting(db, shut, defaultWorkspaceNetwork); got != settingOff {
		t.Errorf("a maximum of 'allowed' opened an agent its owner had closed: %q", got)
	}
}

// Stricter does not run the same way for every setting, which is why the order
// is declared per setting instead of assumed.
func TestStrictnessIsDeclaredNotGuessed(t *testing.T) {
	db := deploymentTestDB(t)

	// Inbound runs any, only, none - three values, and the loosest is spelled
	// "", which here is a VALUE rather than an absence.
	open := AgentRecord{ID: "a", Owner: "alice"} // accepts anyone
	setDeploymentSetting(db, deploymentMaximum, defaultInboundMode, inboundOnly)
	if got := resolveSetting(db, open, defaultInboundMode); got != inboundOnly {
		t.Errorf("an agent accepting anyone was not clamped to its caller list: %q", got)
	}
	// Already stricter, so untouched.
	shut := AgentRecord{ID: "a", Owner: "alice", InboundMode: inboundNone}
	if got := resolveSetting(db, shut, defaultInboundMode); got != inboundNone {
		t.Errorf("a maximum loosened an agent that accepts nobody: %q", got)
	}

	// Every setting that can carry a ceiling declares one, or the ceiling
	// silently does nothing for it.
	for key, spec := range triSettings {
		if len(spec.strictness) == 0 {
			t.Errorf("%s has no strictness order, so a deployment maximum on it does nothing", key)
			continue
		}
		for _, v := range spec.values {
			if !slices.Contains(spec.strictness, v) {
				t.Errorf("%s takes %q but does not rank it, so a ceiling naming it is ignored", key, v)
			}
		}
	}
}

// A ceiling naming a value the setting does not take is IGNORED rather than
// read as strictest. A typo must not ground a deployment.
func TestAnUnrecognisedCeilingDoesNothing(t *testing.T) {
	db := deploymentTestDB(t)
	open := AgentRecord{ID: "a", Owner: "alice", WorkspaceNetwork: settingOn}
	setDeploymentSetting(db, deploymentMaximum, defaultWorkspaceNetwork, "blocked")
	if got := resolveSetting(db, open, defaultWorkspaceNetwork); got != settingOn {
		t.Errorf("a ceiling nobody can spell grounded the deployment: %q", got)
	}
}

// The admin surface exists and is reachable, which is what makes the layer a
// control rather than a stored value.
func TestTheDeploymentLayerIsOfferedToAnAdmin(t *testing.T) {
	sec := deploymentSettingsSection()
	if sec.Group != "Agents" {
		t.Errorf("the section lands on the %q tab, away from the other agent settings", sec.Group)
	}
	panel, ok := sec.Body.(ui.FormPanel)
	if !ok {
		t.Fatalf("the section body is %T, not a form", sec.Body)
	}
	// Every setting that can carry one is offered, both controls, or the layer
	// is only half reachable.
	offered := map[string]bool{}
	for _, f := range panel.Fields {
		offered[f.Field] = true
	}
	for key := range triSettings {
		if !offered[key] {
			t.Errorf("%s has no deployment default control", key)
		}
		if !offered[key+"_max"] {
			t.Errorf("%s has no deployment maximum control", key)
		}
	}
	// NO select offers "undecided". There is one default for the deployment
	// and it always has a value; an undecided option would be a state the
	// resolution chain has no rung for, and the reader would be choosing
	// between a value and the same value spelled differently.
	//
	// Inbound is the exception that proves it: its loosest value IS the empty
	// string (accepts anyone), so an empty option there is a named choice
	// rather than an absence, and it carries a word.
	for _, f := range panel.Fields {
		if f.Type != "select" {
			continue
		}
		for _, o := range f.Options {
			if o.Value == "" && !strings.HasPrefix(f.Field, defaultInboundMode) {
				t.Errorf("%s offers an undecided option", f.Field)
			}
			if strings.TrimSpace(o.Label) == "" {
				t.Errorf("%s has an option with no words on it", f.Field)
			}
		}
	}
}

// Admin only. An owner sets what their own fleet does; they do not set what
// everybody's does, and they certainly do not set the ceiling over their own.
func TestTheDeploymentLayerIsAdminOnly(t *testing.T) {
	src := mustReadFile(t, "deployment_defaults.go")
	if !strings.Contains(src, "if !RequestIsAdmin(r) {") {
		t.Error("the deployment settings endpoint is not admin-gated, so any owner can set the ceiling over every other owner")
	}
	// And there is no second, owner-scoped copy of the same chain. Two pages
	// answering one question is what this replaced.
	if strings.Contains(mustReadFile(t, "agent_settings.go"), "fleetDefault") {
		t.Error("the per-owner defaults rung is back, so two surfaces answer one question again")
	}
}

// One vocabulary. The admin selects, the per-agent selects and the line under
// them all read the same words, because they all read the SETTING's words -
// they built three of their own before, and "on" was the answer to six
// different questions.
func TestEverySettingSaysWhatItsValuesMean(t *testing.T) {
	for key, spec := range triSettings {
		if len(spec.words) == 0 {
			t.Errorf("%s has no words, so its controls print the stored value", key)
			continue
		}
		for _, v := range spec.strictness {
			w := spec.words[v]
			if w == "" {
				t.Errorf("%s takes %q and has no word for it", key, v)
				continue
			}
			if w == settingOn || w == settingOff {
				t.Errorf("%s calls %q %q, which is the stored value, not a word", key, v, w)
			}
		}
	}
	// A memory layer on a shared agent is Shared or Private. It used to read
	// "They see it" / "Kept to yourself", which overstates both sides: a
	// recipient's view of a shared agent is already narrow, and the off side
	// is not withholding - it is the recipient building a layer of their own
	// that the owner never reads either.
	for _, key := range []string{defaultShareCortex, defaultShareReference, defaultShareNotes} {
		if got := settingWord(key, settingOn); got != "Shared" {
			t.Errorf("%s calls its on side %q", key, got)
		}
		if got := settingWord(key, settingOff); got != "Private" {
			t.Errorf("%s calls its off side %q", key, got)
		}
	}
	// Uploads is a different question wearing the same on/off, so it gets its
	// own words rather than the layer pair: it is about whether a recipient
	// may ADD documents, not about whose layer is read.
	if got := settingWord(defaultShareUploads, settingOn); got != "Allowed" {
		t.Errorf("uploads borrowed the memory-layer words: %q", got)
	}
}

// A default looser than the ceiling is a value nothing ever runs under: every
// agent following it is clamped on the way out, so it would exist on the admin
// page and nowhere else.
func TestTheDefaultCannotBeLooserThanTheMaximum(t *testing.T) {
	db := deploymentTestDB(t)

	// Set the loose default first, then tighten the ceiling under it.
	setDeploymentSetting(db, deploymentDefault, defaultWorkspaceNetwork, settingOn)
	setDeploymentSetting(db, deploymentMaximum, defaultWorkspaceNetwork, settingOff)
	if got := effectiveDeploymentDefault(db, defaultWorkspaceNetwork); got != settingOff {
		t.Errorf("the default reads looser than the maximum: %q", got)
	}
	// And an agent following it lands in the same place, so the page and the
	// runtime agree.
	blank := AgentRecord{ID: "a", Owner: "alice"}
	if got := resolveSetting(db, blank, defaultWorkspaceNetwork); got != settingOff {
		t.Errorf("an agent following the default resolved %q", got)
	}

	// The select offers only what the value may be. A control that takes the
	// click, shows the new value and means the old one reads as broken.
	for _, v := range deploymentDefaultChoices(db, defaultWorkspaceNetwork) {
		if v == settingOn {
			t.Error("the default select still offers a value the maximum forbids")
		}
	}
	// Lifting the ceiling gives the choice back.
	setDeploymentSetting(db, deploymentMaximum, defaultWorkspaceNetwork, "")
	if len(deploymentDefaultChoices(db, defaultWorkspaceNetwork)) != 2 {
		t.Error("lifting the maximum did not restore the choice")
	}
}

// Tightening the maximum pulls the stored default down with it, rather than
// refusing the write and asking the reader to go and change the other control
// first - a rule the page would then have to teach.
func TestTighteningTheMaximumMovesTheDefault(t *testing.T) {
	app, _, _ := newTestOrchestrate(t)
	pinRootDB(t)
	AuthDB().Set(AuthTable, "user:alice", AuthUser{Username: "alice", Admin: true})
	patch := func(body string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPatch, "/api/console/deployment-settings", strings.NewReader(body))
		w := httptest.NewRecorder()
		app.handleDeploymentSettings(w, asUser(r, "alice"))
		if w.Code != http.StatusNoContent {
			t.Fatalf("patch %s: %d", body, w.Code)
		}
	}
	patch(`{"workspace_network":"on"}`)
	patch(`{"workspace_network_max":"off"}`)
	if got := deploymentSetting(RootDB, deploymentDefault, defaultWorkspaceNetwork); got != settingOff {
		t.Errorf("the STORED default was left looser than the ceiling: %q", got)
	}
}

// The per-agent control obeys the same rule, and says why its list is short.
// A select missing the option somebody came to pick, with nothing explaining
// it, reads as a broken control - and the reason is a maximum they may not be
// able to see.
func TestAnAgentIsNotOfferedWhatTheMaximumForbids(t *testing.T) {
	db := deploymentTestDB(t)
	setDeploymentSetting(db, deploymentMaximum, defaultWorkspaceNetwork, settingOff)

	for _, o := range settingOptions(db, defaultWorkspaceNetwork) {
		if o.Value == settingOn {
			t.Error("an agent is offered a value that would be clamped the moment it was picked")
		}
	}
	line := settingSource(db, AgentRecord{ID: "a", Owner: "alice"}, defaultWorkspaceNetwork)
	if !strings.Contains(line, "Limit: Blocked") {
		t.Errorf("the short list is unexplained: %q", line)
	}
	// No maximum, no sentence: a line that explains a constraint nobody set is
	// noise on every other deployment.
	setDeploymentSetting(db, deploymentMaximum, defaultWorkspaceNetwork, "")
	if line = settingSource(db, AgentRecord{ID: "a", Owner: "alice"}, defaultWorkspaceNetwork); strings.Contains(line, "Limit") {
		t.Errorf("an unconstrained setting talks about a limit: %q", line)
	}
}
