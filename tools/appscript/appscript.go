// Package appscript runs a custom-app script (a data source or an action) as a
// sandboxed shell TempTool. It is the single execution seam shared by the host
// that serves /custom/<slug>/data|action/<name> (apps/customapps) and the
// authoring tool's test action (apps/orchestrate) — so a script a developer
// "tests" runs through byte-identical machinery to the one a user triggers, and
// a test pass can never disagree with production behavior.
//
// It lives in tools/ (a leaf importing core + temptool) rather than core/
// because temptool imports core, and rather than customapps because customapps
// imports orchestrate — either placement would cycle.
package appscript

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/temptool"
)

// Run executes one custom-app script and returns its stdout. The script is just
// a TempTool the framework dispatches on the app owner's behalf: it runs in the
// owner's workspace under the bwrap sandbox, reaches external data only through
// the gohort hook (fetch/log/…), and receives args as environment variables.
// The script file is named per (kind, slug, name) so concurrent apps/scripts
// don't collide in the owner's workspace.
func Run(user string, db Database, slug, kind, name, language, script string, caps []string, args map[string]any) (string, error) {
	ws, err := EnsureWorkspaceDir(user)
	if err != nil {
		return "", fmt.Errorf("workspace: %w", err)
	}
	interp, ext := "python3", "py"
	if l := strings.ToLower(strings.TrimSpace(language)); l == "bash" || l == "sh" {
		interp, ext = "bash", "sh"
	}
	scriptName := fmt.Sprintf("%s_%s_%s.%s", kind, SanitizeName(slug), SanitizeName(name), ext)
	if caps == nil {
		caps = []string{"fetch", "log"} // sensible default: read external data + log
	}
	// Auto-grant fetch_via for every credential the app OWNER may use. A custom-
	// app script always runs in the owner's context (handleData/handleAction pass
	// the owner as `user`), and the owner can already call fetch_via on these
	// credentials from a hand-authored temptool — so an app data source shouldn't
	// have to redeclare capabilities:["fetch_via:<cred>"] just to reach one. The
	// omission failed SILENTLY: the hook denied fetch_via, the script's try/except
	// swallowed the error, and the panel rendered an empty table that even passed
	// verify (the roster-app trap). fetch_url already reaches a credential-covered
	// host via auto-routing; this brings fetch_via to the same parity. Merged +
	// deduped so an explicit declaration is preserved and never doubled.
	caps = mergeCaps(caps, ownerFetchViaCaps(user))
	params := map[string]ToolParam{}
	for k := range args {
		params[k] = ToolParam{Type: "string"}
	}
	tt := &TempTool{
		Name:             "app_" + kind + ":" + slug + ":" + name,
		Description:      "custom app " + kind,
		Mode:             "shell",
		ScriptBody:       script,
		ScriptName:       scriptName,
		CommandTemplate:  interp + " {workspace_dir}/" + scriptName,
		HookCapabilities: caps,
		Params:           params,
	}
	sess := &ToolSession{
		Username:     user,
		WorkspaceDir: ws,
		DB:           db,
		// The owner is acting in their own app — allow the hook's fetch/browse to
		// reach the network (the sandbox itself stays network-isolated).
		Network: NewNetworkConnector(false),
	}
	return temptool.DispatchTempToolDirect(sess, tt, args)
}

// ownerFetchViaCaps returns a fetch_via:<name> capability for every credential
// the owner may use (their own + globals they're permitted). Empty owner → none.
func ownerFetchViaCaps(owner string) []string {
	if strings.TrimSpace(owner) == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, "fetch_via:"+name)
	}
	for _, c := range Secure().ListUser(owner) {
		add(c.Name)
	}
	for _, c := range Secure().List() {
		// Skip credentials another configuration owns. fetch_via IS a
		// declaring capability, so a secured credential is legitimately
		// usable this way — but the peer key is not a user capability at
		// all. It is an admin resource-sharing arrangement that appears
		// and disappears with its peer, and a script declared against it
		// stops working the day the peer is forgotten.
		if c.ManagedElsewhere() {
			continue
		}
		if Secure().UserMayUse(c, owner) {
			add(c.Name)
		}
	}
	return out
}

// mergeCaps concatenates two capability lists, dropping duplicates and empties so
// an explicitly-declared cap is preserved but never doubled.
func mergeCaps(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range append(append([]string{}, a...), b...) {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

// SanitizeName reduces a slug/name to a safe filename fragment (alnum + _).
func SanitizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}
