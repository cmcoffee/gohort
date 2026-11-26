package temptool

// A custom app's script calling one of its owner's tools.
//
// An app's data sources and actions could reach the network and the owner's
// credentials but not the owner's tools, so a weather app re-implemented
// get_weather by reading its definition and copying its code into the
// script: two copies of the same logic, free to drift. A script that declares
// "tool:<name>" can now run that tool through the hook (gohort.call_tool).
//
// What may run is decided here, not by the script:
//   - the owner's own ACTIVE tools, and tools they added from the catalog.
//     A tool still waiting for approval is not one of them.
//   - the deployment's MCP tools (<server>_<tool>, from an enabled server
//     whose tools are offered to agents), run as the app's owner: what they
//     read goes to the owner's script, so it is the owner's sign-in that
//     answers, never that of the person using the app. A server with a
//     sign-in per person is the owner's to lend, so in a run for someone
//     else it follows the app's choice for it (mcpReachRefusal).
//   - only a tool that never stops to ask before running (NeedsConfirm, the
//     same rule an unattended fire is held to). A page load has nobody to
//     answer a confirmation, so a tool that would ask is refused, and the
//     refusal says why rather than hanging the page.

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appreach"
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
		if ct, _, isMCP := scriptMCPTool(name); isMCP {
			if c, ok := ct.(interface{ NeedsConfirm() bool }); ok && c.NeedsConfirm() {
				return TempTool{}, fmt.Errorf("%q asks before it runs (its name suggests it changes something on the server), and a script has nobody to ask, so it cannot be called from an app", name)
			}
			return TempTool{Name: name}, nil
		}
		if _, builtin := LookupChatTool(name); builtin {
			return TempTool{}, fmt.Errorf("%q is a built-in tool, and a script calls only tools defined with tool_def (the owner's own, or added from the catalog) and MCP tools; for what it does, a script has fetch_url, fetch_via and browse_page", name)
		}
		return TempTool{}, fmt.Errorf("no tool named %q among the owner's active tools or the ones added from the catalog (a tool still waiting for approval does not run)", name)
	}
	if tempToolNeedsConfirm(&tt, user) {
		return TempTool{}, fmt.Errorf("%q asks before it runs (its own ask-first mark, a credential that confirms, raw network or a key handed to its script), and a script has nobody to ask, so it cannot be called from an app", name)
	}
	return tt, nil
}

// CallToolForScript runs name for an app script on sess, if the owner may,
// as the owner, whoever is using the app. An MCP tool ran as the person using
// it, with their own sign-in, and handed what it read to the owner's script:
// their issues, their mail, whatever that server reaches for them.
func CallToolForScript(sess *ToolSession, name string, args map[string]any) (string, error) {
	if sess == nil || strings.TrimSpace(sess.Username) == "" {
		return "", fmt.Errorf("no owner to run the tool as")
	}
	if ct, server, isMCP := scriptMCPTool(name); isMCP {
		if _, err := ScriptCallableTool(sess.DB, sess.Username, name); err != nil {
			return "", err
		}
		run, ok := ct.(interface {
			RunWithSession(map[string]any, *ToolSession) (string, error)
		})
		if !ok {
			return "", fmt.Errorf("MCP tool %q cannot be run for a person", name)
		}
		if err := mcpReachRefusal(sess, server); err != nil {
			return "", err
		}
		Log("[temptool/script] %s's app calls MCP %s (%s)", sess.Username, name, server)
		return run.RunWithSession(args, &ToolSession{Username: sess.Username, DB: sess.DB, Ctx: sess.Context()})
	}
	tt, err := ScriptCallableTool(sess.DB, sess.Username, name)
	if err != nil {
		return "", err
	}
	Log("[temptool/script] %s calls %q", sess.Username, name)
	return DispatchTempToolDirect(sess, &tt, args)
}

// scriptMCPTool is name's registered MCP tool and its server, when name is one
// from an enabled server whose tools are offered to agents. Recognised by the
// tool's category, which every MCP tool carries as its server's
// (MCPToolCategory), so nothing here depends on the proxy type.
func scriptMCPTool(name string) (ChatTool, string, bool) {
	ct, ok := LookupChatTool(name)
	if !ok {
		return nil, "", false
	}
	c, ok := ct.(interface{ Category() string })
	if !ok {
		return nil, "", false
	}
	cat := c.Category()
	for _, cfg := range MCP().List() {
		if cfg.Enabled && cfg.ExposeTools && MCPToolCategory(cfg.Name) == cat {
			return ct, cfg.Name, true
		}
	}
	return nil, "", false
}

// MCPReachKey is how an app's credential rules (core/applocal's
// AppCredentials, a session's CredentialLend and DeniedCredentials) name the
// owner's sign-in to MCP server (appreach.MCPKey).
func MCPReachKey(server string) string { return appreach.MCPKey(server) }

// ScriptMCPSignIn is the server of MCP tool name when that server signs in
// per person: the only kind whose sign-in is the owner's to lend from an app.
// A server on the deployment's own connection is not.
func ScriptMCPSignIn(name string) (string, bool) {
	_, server, ok := scriptMCPTool(strings.TrimSpace(name))
	if !ok || !mcpSignsInPerPerson(server) {
		return "", false
	}
	return server, true
}

// mcpSignsInPerPerson reports whether server signs in per person (oauth).
func mcpSignsInPerPerson(server string) bool {
	cfg, ok := MCP().Load(server)
	return ok && cfg.AuthMode == MCPAuthOAuth
}

// mcpReachRefusal says why an app's script may not reach server on sess, or
// nil, and records a reach. Only a server that signs in per person is held to
// anything: the call runs on the owner's sign-in, which the app may keep out
// altogether or keep from other people's runs (appreach.Rules.MCPRefusal, the
// rule an app's agent and pipeline runs meet in core too).
func mcpReachRefusal(sess *ToolSession, server string) error {
	if !mcpSignsInPerPerson(server) {
		return nil
	}
	person := strings.TrimSpace(sess.CredentialPerson)
	if person == strings.TrimSpace(sess.Username) {
		person = ""
	}
	return appreach.Rules{
		Person: person,
		Lend:   sess.CredentialLend,
		Off:    func(name string) bool { return sess.DeniedCredentials[name] },
		Used:   sess.CredentialUsed,
	}.MCPRefusal(server)
}
