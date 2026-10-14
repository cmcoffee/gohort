package filestore

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A mapped action runs the store's registered binary with arguments, and
// nothing else: the mapping is a model's proposal, and an action that chains,
// pipes or runs something else would be a tool doing whatever it is handed.
func TestAMappedActionRunsOnlyItsCommand(t *testing.T) {
	bin := "/opt/bin/unpack"
	for tmpl, ok := range map[string]bool{
		"/opt/bin/unpack {folder}":                  true,
		"/opt/bin/unpack --list {folder}":           true,
		"'/opt/bin/unpack' {folder} {response}":     true,
		"/opt/bin/other {folder}":                   false,
		"cd {folder} && /opt/bin/unpack .":          false,
		"/opt/bin/unpack {folder}; rm -rf {folder}": false,
		"/opt/bin/unpack {folder} | tee out.txt":    false,
		"/opt/bin/unpack {folder} > {folder}/out":   false,
		"/opt/bin/unpack $(cat {folder}/key)":       false,
		"/opt/bin/unpack `id`":                      false,
		"LANG=C /opt/bin/unpack {folder}":           false,
		"sh -c {script}":                            false,
		"/opt/bin/unpack {folder}\n/opt/bin/other":  false,
		"/opt/bin/unpack_evil {folder}":             false,
	} {
		err := actionRunsTheCommand(bin, tmpl)
		if (err == nil) != ok {
			t.Errorf("%q: allowed=%v, want %v (%v)", tmpl, err == nil, ok, err)
		}
	}

	// Saving a mapping with such an action is refused; one stored before the
	// rule is left out of what an agent gets.
	db := &DBase{Store: kvlite.MemStore()}
	if _, err := SaveStore(db, Store{Name: "Intake", Path: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveStoreCommand(db, StoreCommand{Usage: UsageOpen, Slug: "intake", Name: "decrypt", Command: bin}); err != nil {
		t.Fatal(err)
	}
	_, err := SaveCommandTools(db, "intake", "decrypt", "Decrypts a dump.", []TempToolAction{
		{Name: "run", CommandTemplate: "cd {folder} && " + bin + " ."},
	})
	if err == nil || !strings.Contains(err.Error(), "must run the registered command") {
		t.Errorf("a chained action was saved: %v", err)
	}
	cmd, _ := LoadStoreCommand(db, "intake", "decrypt")
	cmd.Tools = []TempToolAction{
		{Name: "good", CommandTemplate: bin + " {folder}"},
		{Name: "old_bad", CommandTemplate: bin + " {folder}; rm -rf /"},
	}
	tt := cmd.asTempTool()
	if len(tt.Actions) != 1 || tt.Actions[0].Name != "good" || !tt.AdminMapped {
		t.Errorf("what an agent gets from a stored mapping: %+v", tt)
	}
}
