package orchestrate

// The two powers an administrator gets over agent security, and the line
// between them.

import (
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
	if got := resolveSetting(db, "alice", blank, defaultWorkspaceNetwork); got != settingOn {
		t.Errorf("a deployment that sets nothing changed behaviour: %q", got)
	}

	// The deployment speaks, and a fleet nobody has touched reads it.
	setDeploymentSetting(db, deploymentDefault, defaultWorkspaceNetwork, settingOff)
	if got := resolveSetting(db, "alice", blank, defaultWorkspaceNetwork); got != settingOff {
		t.Errorf("the deployment default was ignored: %q", got)
	}
	if src := settingSource(db, "alice", blank, defaultWorkspaceNetwork); !strings.Contains(src, "deployment default") {
		t.Errorf("the page does not say where the answer came from: %q", src)
	}

	// The OWNER outranks it, in either direction - it is a starting point, and
	// one that could not be moved would not be one.
	setFleetDefault(db, "alice", defaultWorkspaceNetwork, settingOn)
	if got := resolveSetting(db, "alice", blank, defaultWorkspaceNetwork); got != settingOn {
		t.Error("an owner could not widen off a deployment DEFAULT, which makes it a ceiling by accident")
	}

	// And the agent outranks the owner, as before.
	own := AgentRecord{ID: "a", Owner: "alice", WorkspaceNetwork: settingOff}
	if got := resolveSetting(db, "alice", own, defaultWorkspaceNetwork); got != settingOff {
		t.Error("the agent's own answer stopped winning")
	}
}

// The maximum is the one that constrains, and it constrains everybody.
func TestTheDeploymentMaximumCannotBeWidenedAway(t *testing.T) {
	db := deploymentTestDB(t)
	// An owner who has opened everything, on an agent that has too.
	setFleetDefault(db, "alice", defaultWorkspaceNetwork, settingOn)
	open := AgentRecord{ID: "a", Owner: "alice", WorkspaceNetwork: settingOn}
	if got := resolveSetting(db, "alice", open, defaultWorkspaceNetwork); got != settingOn {
		t.Fatalf("setup: %q", got)
	}

	setDeploymentSetting(db, deploymentMaximum, defaultWorkspaceNetwork, settingOff)
	if got := resolveSetting(db, "alice", open, defaultWorkspaceNetwork); got != settingOff {
		t.Errorf("an agent resolved looser than the deployment maximum: %q", got)
	}
	src := settingSource(db, "alice", open, defaultWorkspaceNetwork)
	if !strings.Contains(src, "maximum") {
		t.Errorf("the page shows a value without saying the ceiling is holding it: %q", src)
	}

	// Lifting the ceiling gives the agent back what it had. Its own setting
	// was never rewritten, so nobody has to remember what it used to be.
	setDeploymentSetting(db, deploymentMaximum, defaultWorkspaceNetwork, "")
	if got := resolveSetting(db, "alice", open, defaultWorkspaceNetwork); got != settingOn {
		t.Error("lifting the ceiling left the agent clamped, so the clamp rewrote the record")
	}
}

// A ceiling never LOOSENS. It is a maximum, so an agent already stricter than
// it stays where it is.
func TestTheMaximumOnlyEverTightens(t *testing.T) {
	db := deploymentTestDB(t)
	shut := AgentRecord{ID: "a", Owner: "alice", WorkspaceNetwork: settingOff}
	setDeploymentSetting(db, deploymentMaximum, defaultWorkspaceNetwork, settingOn)
	if got := resolveSetting(db, "alice", shut, defaultWorkspaceNetwork); got != settingOff {
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
	if got := resolveSetting(db, "alice", open, defaultInboundMode); got != inboundOnly {
		t.Errorf("an agent accepting anyone was not clamped to its caller list: %q", got)
	}
	// Already stricter, so untouched.
	shut := AgentRecord{ID: "a", Owner: "alice", InboundMode: inboundNone}
	if got := resolveSetting(db, "alice", shut, defaultInboundMode); got != inboundNone {
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
	if got := resolveSetting(db, "alice", open, defaultWorkspaceNetwork); got != settingOn {
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
	// Each select offers "unset", which is not the same as any of the values:
	// unset, the rung below answers.
	for _, f := range panel.Fields {
		if f.Type != "select" {
			continue
		}
		if len(f.Options) == 0 || f.Options[0].Value != "" {
			t.Errorf("%s cannot be cleared, so a deployment can never stop saying something", f.Field)
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
	// And the per-owner endpoint stays per-owner: it keys by the CALLER, so an
	// admin gate there would be the wrong fix for the wrong problem.
	if !strings.Contains(mustReadFile(t, "fleet_defaults.go"), "setFleetDefault(RootDB, user, setting, v)") {
		t.Error("the fleet default stopped being keyed to the caller")
	}
}
