package bridge

import (
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
)

// This package's blank imports decide which native tools the daemon announces,
// so it's the right place to assert the announced surface is actually usable.
//
// It wasn't: the tools were named "filesystem.read_local_file", the server
// prefixed that into the LLM catalog, and every model API rejects a dot in a
// tool name — so the server dropped the whole desktop surface and the model was
// never offered a single local tool. Nothing failed loudly on either side,
// which is why it survived so long. This test is the loud failure.
func TestAnnouncedToolNamesAreUsableByModelAPIs(t *testing.T) {
	tools := core.RegisteredTools()
	if len(tools) == 0 {
		t.Fatal("no tools registered — the blank imports that pull in the native tool packages are gone")
	}
	for _, tl := range tools {
		name := tl.Name()
		if !core.ValidToolName(name) {
			t.Errorf("tool %q has a name the model APIs reject (must match ^[a-zA-Z0-9_-]{1,128}$)", name)
		}
		if strings.Contains(name, ".") {
			t.Errorf("tool %q is namespaced with a dot; use an underscore", name)
		}
	}
}

// The filesystem suite is the surface every platform gets. A rename that loses
// one of these silently narrows what the agent can do on the user's machine.
func TestFilesystemToolsAreRegistered(t *testing.T) {
	for _, want := range []string{
		"filesystem_read_local_file",
		"filesystem_list_directory",
		"filesystem_stat_file",
		"filesystem_head_file",
		"filesystem_tail_file",
		"filesystem_read_file_range",
		"filesystem_grep_file",
		"filesystem_write_file",
	} {
		if _, ok := core.FindTool(want); !ok {
			t.Errorf("tool %q is not registered", want)
		}
	}
}
