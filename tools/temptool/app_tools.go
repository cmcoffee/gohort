package temptool

// An app's own tools through tool_def (docs/design/app-history.md, stage 2,
// piece B). A call naming an app (app="<slug>") makes, changes, shows,
// lists, tests or deletes a tool of that app's own space rather than one of
// the person's: the same create and update, with the app's space as where
// the tool is looked for and where it is saved. Only Builder's session has
// the app's space wired (ToolSession.AppTools); any other agent naming an
// app is refused, as only Builder and Anvil change an app's tools.

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// appParam is the argument naming the app.
var appParam = ToolParam{Type: "string", Description: "(Builder, optional) The app (its id) whose own tool this is: the tool is part of that app, kept in its own tool space, loaded only by its agents. Omit for your own tools."}

// withAppTarget points sess at the app a tool_def call names, for that
// call, and returns what undoes it. No app named changes nothing.
func withAppTarget(args map[string]any, sess *ToolSession) (func(), error) {
	slug := strings.TrimSpace(StringArg(args, "app"))
	if slug == "" || sess == nil {
		return func() {}, nil
	}
	if sess.AppTools == nil || sess.SaveAppTool == nil || sess.DeleteAppTool == nil {
		return nil, fmt.Errorf("only Builder makes or changes an app's own tools (app=%q): ask Builder. Nothing was changed", slug)
	}
	if _, err := sess.AppTools(slug); err != nil {
		return nil, err
	}
	prev := sess.AuthoringApp
	sess.AuthoringApp = slug
	return func() { sess.AuthoringApp = prev }, nil
}

// withApp runs h with sess pointed at the app args names, if any.
func withApp(h func(map[string]any, *ToolSession) (string, error)) func(map[string]any, *ToolSession) (string, error) {
	return func(args map[string]any, sess *ToolSession) (string, error) {
		done, err := withAppTarget(args, sess)
		if err != nil {
			return "", err
		}
		defer done()
		return h(args, sess)
	}
}

// appToolRecord is the tool name of the app sess is pointed at.
func appToolRecord(sess *ToolSession, name string) (TempTool, bool) {
	if sess == nil || sess.AuthoringApp == "" || sess.AppTools == nil {
		return TempTool{}, false
	}
	tools, err := sess.AppTools(sess.AuthoringApp)
	if err != nil {
		return TempTool{}, false
	}
	for _, t := range tools {
		if t.Name == name {
			return t, true
		}
	}
	return TempTool{}, false
}

// saveAppTool saves t into the app sess is pointed at, and says where it
// landed and what it waits for.
func saveAppTool(sess *ToolSession, t TempTool) string {
	if err := sess.SaveAppTool(sess.AuthoringApp, t); err != nil {
		Log("[temptool.scope] saving %q to app %s failed: %v", t.Name, sess.AuthoringApp, err)
		return fmt.Sprintf("Available for this session; saving it to the app's own tools failed: %v", err)
	}
	Log("[temptool.scope] saved %q to the own tools of app %s", t.Name, sess.AuthoringApp)
	msg := fmt.Sprintf("Saved to the app's own tools (%s): only its agents load it, and it is part of the app and its history.", sess.AuthoringApp)
	if creds, raw := toolReachNote(t); creds != "" || raw {
		msg += " It reaches further than gohort's own tools"
		if creds != "" {
			msg += " (" + creds + ")"
		}
		if raw {
			msg += " (raw network)"
		}
		msg += ", so the app's agents load it once an administrator approves it; the request is in their queue."
	}
	return msg
}

// toolReachNote is what t reaches that waits for an administrator in an
// app's space: the credentials it names, and raw network.
func toolReachNote(t TempTool) (string, bool) {
	var creds []string
	seen := map[string]bool{}
	add := func(c string) {
		if c = strings.TrimSpace(c); c != "" && !seen[c] {
			seen[c] = true
			creds = append(creds, c)
		}
	}
	add(t.Credential)
	for _, c := range t.HookCapabilities {
		if i := strings.IndexByte(c, ':'); i > 0 && (c[:i] == "secret" || c[:i] == "fetch_via") {
			add(c[i+1:])
		}
	}
	return strings.Join(creds, ", "), t.RawNetwork
}

// listAppTools is action="list" for the app sess is pointed at.
func listAppTools(sess *ToolSession) (string, error) {
	tools, err := sess.AppTools(sess.AuthoringApp)
	if err != nil {
		return "", err
	}
	if len(tools) == 0 {
		return fmt.Sprintf("The app %s has no tools of its own yet.", sess.AuthoringApp), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "The app %s's own tools (%d), loaded only by its agents:\n", sess.AuthoringApp, len(tools))
	for _, t := range tools {
		fmt.Fprintf(&b, "- %s (%s): %s\n", t.Name, modeLabel(effectiveTempToolMode(t)), t.Description)
	}
	return b.String(), nil
}

// removeAppTool is action="delete" for the app sess is pointed at.
func removeAppTool(sess *ToolSession, name string) (string, error) {
	if _, ok := appToolRecord(sess, name); !ok {
		return "", fmt.Errorf("the app %s has no tool %q of its own", sess.AuthoringApp, name)
	}
	if err := sess.DeleteAppTool(sess.AuthoringApp, name); err != nil {
		return "", err
	}
	sess.RemoveTempTool(name)
	if sess.DB != nil && sess.ChatSessionID != "" {
		RemoveSessionTempTool(sess.DB, sess.ChatSessionID, name)
	}
	return fmt.Sprintf("Removed %q from the app %s's own tools.", name, sess.AuthoringApp), nil
}
