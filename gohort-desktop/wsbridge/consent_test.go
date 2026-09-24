package wsbridge

import (
	"testing"
)

type recordingApprover struct {
	name string
	args map[string]any
	ok   bool
}

func (r *recordingApprover) RequestApprovalBlocking(id, name string, args map[string]any) bool {
	r.name, r.args = name, args
	return r.ok
}

// Installing new code on the machine needs a person's yes. With no approver
// it used to auto-allow, and the prompt never showed the environment, which
// is part of what runs.
func TestInstallConsentIsRequiredAndShowsEnv(t *testing.T) {
	c := &wsClient{}
	if c.consentInstall("srv", "npx", []string{"pkg"}, nil) {
		t.Fatal("an install with no approver was allowed")
	}
	if c.consentBridge("imessage") {
		t.Fatal("a relay enable with no approver was allowed")
	}

	ap := &recordingApprover{ok: true}
	c.approver = ap
	if !c.consentInstall("srv", "npx", []string{"pkg"}, envNames(map[string]string{"B_TOKEN": "s", "A_REGION": "r"})) {
		t.Fatal("an approved install was refused")
	}
	if !IsInstallConsent(ap.name) {
		t.Fatalf("the approval name %q does not mark it as an install", ap.name)
	}
	env, _ := ap.args["env"].([]string)
	if len(env) != 2 || env[0] != "A_REGION" || env[1] != "B_TOKEN" {
		t.Fatalf("the prompt should list the environment names, got %v", ap.args["env"])
	}
	if IsInstallConsent("filesystem_read_local_file") {
		t.Error("an ordinary tool call reads as an install")
	}
}
