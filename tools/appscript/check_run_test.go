package appscript

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A check run refuses every call that can act beyond it: tools, agents,
// pipelines, store commands and actions, and notifications reach nobody. ask
// stays as it was.
func TestACheckRunRefusesWhatActsBeyondIt(t *testing.T) {
	sess := &ToolSession{Username: "owner-a"}
	asked := false
	sess.Ask = func(string, bool) (string, error) { asked = true; return "ok", nil }
	holdToCheck(sess)
	if !sess.CheckRun {
		t.Fatal("the session is not marked as a check run")
	}
	if _, err := sess.CallTool("send_email", nil); err == nil {
		t.Error("a check run called a tool")
	}
	if _, _, err := sess.RunAgent("helper", "go"); err == nil {
		t.Error("a check run ran an agent")
	}
	if _, err := sess.RunPipeline("p", "in"); err == nil {
		t.Error("a check run ran a pipeline")
	}
	if _, err := sess.StoreCommand("s", "", "rm", ""); err == nil {
		t.Error("a check run ran a store command")
	}
	if _, err := sess.StoreAction("s", "c", "a", nil); err == nil {
		t.Error("a check run ran a store action")
	}
	if _, err := sess.StorePull("s", "i", false); err == nil {
		t.Error("a check run pulled from a store")
	}
	if out, err := sess.Notify("hi", "there"); err != nil || !strings.Contains(out, "not sent") {
		t.Errorf("a check run's notification: %q %v", out, err)
	}
	if _, err := sess.Ask("q", false); err != nil || !asked {
		t.Error("a check run's ask was refused")
	}
}
