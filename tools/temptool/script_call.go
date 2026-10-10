package temptool

// A custom app's script calling one of its owner's tools.
//
// An app's data sources and actions could reach the network and the owner's
// credentials but not the owner's tools, so a weather app re-implemented
// get_weather by reading its definition and copying its code into the
// script: two copies of the same logic, free to drift. A script that declares
// "tool:<name>" can now run that tool through the hook (oddjob.call_tool).
//
// What may run is decided here, not by the script:
//   - the owner's own ACTIVE tools, and tools they added from the catalog.
//     A tool still waiting for approval is not one of them.
//   - only a tool that never stops to ask before running (NeedsConfirm, the
//     same rule an unattended fire is held to). A page load has nobody to
//     answer a confirmation, so a tool that would ask is refused, and the
//     refusal says why rather than hanging the page.

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/oddjob/core"
)

// ScriptCallableTool finds the tool named name that user's scripts may call,
// or says why there is none. Exported so authoring can check a declared
// "tool:<name>" when the app is saved, against the same rule the call meets.
func ScriptCallableTool(db Database, user, name string) (TempTool, error) {
	name = strings.TrimSpace(name)
	var tt TempTool
	found := false
	for _, p := range LoadPersistentTempTools(db, user) {
		if p.Tool.Name == name {
			tt, found = p.Tool, true
			break
		}
	}
	if !found {
		for _, p := range AdoptedToolsFor(db, user) {
			if p.Tool.Name == name {
				tt, found = p.Tool, true
				break
			}
		}
	}
	if !found {
		if _, builtin := LookupChatTool(name); builtin {
			return TempTool{}, fmt.Errorf("%q is a built-in tool, and a script calls only tools defined with tool_def (the owner's own, or added from the catalog); for what it does, a script has fetch_url, fetch_via and browse_page", name)
		}
		return TempTool{}, fmt.Errorf("no tool named %q among the owner's active tools or the ones added from the catalog (a tool still waiting for approval does not run)", name)
	}
	if tempToolNeedsConfirm(&tt, user) {
		return TempTool{}, fmt.Errorf("%q asks before it runs (its own ask-first mark, a credential that confirms, raw network or a key handed to its script), and a script has nobody to ask, so it cannot be called from an app", name)
	}
	return tt, nil
}

// CallToolForScript runs name for an app script on sess, if the owner may.
func CallToolForScript(sess *ToolSession, name string, args map[string]any) (string, error) {
	if sess == nil || strings.TrimSpace(sess.Username) == "" {
		return "", fmt.Errorf("no owner to run the tool as")
	}
	tt, err := ScriptCallableTool(sess.DB, sess.Username, name)
	if err != nil {
		return "", err
	}
	Log("[temptool/script] %s calls %q", sess.Username, name)
	return DispatchTempToolDirect(sess, &tt, args)
}
