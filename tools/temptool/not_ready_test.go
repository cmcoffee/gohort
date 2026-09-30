package temptool

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A tool whose credential is turned off or has no key stays in the catalog
// but says so first, so the agent reports what is missing instead of calling
// it; once the key is set it reads as normal.
func TestAToolOnAnUnfinishedCredentialSaysSo(t *testing.T) {
	secStore := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return secStore }
	defer func() { AuthDB = prev }()
	if err := Secure().SaveAPIDraft(SecureCredential{Name: "forge", Type: SecureCredBearer, BaseURL: "https://forge.example"}); err != nil {
		t.Fatal(err)
	}
	sess := &ToolSession{Username: "alice", DB: &DBase{Store: kvlite.MemStore()}}
	tt := &TempTool{Name: "forge_list", Mode: TempToolModeAPI, Credential: "forge", Method: "GET",
		CommandTemplate: "https://forge.example/api/projects", Description: "List projects."}
	desc := func() string { return agentToolDefsFromTemp(sess, tt)[0].Tool.Description }

	if d := desc(); !strings.HasPrefix(d, "NOT READY:") || !strings.Contains(d, "turned off") {
		t.Errorf("a draft credential is off: %q", d)
	}
	if err := Secure().SetDisabled("forge", false); err != nil {
		t.Fatal(err)
	}
	if d := desc(); !strings.HasPrefix(d, "NOT READY:") || !strings.Contains(d, "has no key yet") {
		t.Errorf("on, but no key: %q", d)
	}
	c, _ := Secure().Load("forge")
	if err := Secure().Save(c, "tok-value"); err != nil {
		t.Fatal(err)
	}
	if d := desc(); strings.Contains(d, "NOT READY") || !strings.HasPrefix(d, "List projects.") {
		t.Errorf("with its key it reads as normal: %q", d)
	}

	shell := &TempTool{Name: "count_lines", Mode: TempToolModeShell, Description: "Count."}
	if d := agentToolDefsFromTemp(sess, shell)[0].Tool.Description; strings.Contains(d, "NOT READY") {
		t.Errorf("a tool with no credential is untouched: %q", d)
	}
}
