package temptool

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// A toolbox action marked as one that only reads (TempToolAction.Reads), on
// a toolbox built on its author's OWN credential, becomes one of that
// credential's read calls (SecureCredential.ReadCalls). That list is what a
// key lent for reads is held to: the mark itself cannot be, because anybody a
// key is lent to could write a toolbox of their own on it and mark a write as
// a read. So the mark counts at the key only where the key's owner made it.

// noteToolboxReadCalls follows the reads marks this save SET on the named
// toolbox's actions onto its credential's read calls, when the session's
// user owns that credential: an action sent with reads: true adds its call,
// one sent with reads: false takes it off. An action the save did not mark
// leaves the list alone, so a call the owner took off the list stays off
// through every later edit of something else.
func noteToolboxReadCalls(sess *ToolSession, name string, args map[string]any) {
	if sess == nil || strings.TrimSpace(name) == "" {
		return
	}
	tt := sess.LookupTempTool(name)
	if tt == nil || tt.Mode != TempToolModeToolbox || strings.TrimSpace(tt.Credential) == "" {
		return
	}
	if _, owns := Secure().LoadUser(sess.Username, tt.Credential); !owns {
		return
	}
	marked := map[string]bool{} // action -> reads, as this save set it
	if list, ok := args["actions"].([]any); ok {
		for _, raw := range list {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if v, set := m["reads"].(bool); set {
				marked[strings.TrimSpace(StringArg(m, "name"))] = v
			}
		}
	}
	var add, drop []string
	for _, a := range tt.Actions {
		reads, set := marked[a.Name]
		if !set || !isMutatingMethod(a.Method) {
			continue
		}
		p := readCallPath(a.URLTemplate)
		if p == "" {
			continue
		}
		call := strings.ToUpper(strings.TrimSpace(a.Method)) + " " + p
		if reads {
			add = append(add, call)
		} else {
			drop = append(drop, call)
		}
	}
	if len(add) > 0 {
		if err := Secure().AddReadCalls(sess.Username, tt.Credential, add); err != nil {
			Log("[temptool] %s: could not note %q's read calls on %q: %v", sess.Username, name, tt.Credential, err)
		}
	}
	if len(drop) > 0 {
		if err := Secure().RemoveReadCalls(sess.Username, tt.Credential, drop); err != nil {
			Log("[temptool] %s: could not take %q's read calls off %q: %v", sess.Username, name, tt.Credential, err)
		}
	}
}

// readCallPath is the path of a URL template as a read-call pattern: the
// scheme, host and query dropped, and every segment holding a {param} made
// "*". "" for a template with no path.
func readCallPath(tpl string) string {
	tpl = strings.TrimSpace(tpl)
	if i := strings.Index(tpl, "://"); i >= 0 {
		tpl = tpl[i+3:]
		j := strings.IndexByte(tpl, '/')
		if j < 0 {
			return ""
		}
		tpl = tpl[j:]
	}
	if i := strings.IndexAny(tpl, "?#"); i >= 0 {
		tpl = tpl[:i]
	}
	segs := strings.Split(strings.Trim(tpl, "/"), "/")
	for i, s := range segs {
		if strings.Contains(s, "{") {
			segs[i] = "*"
		}
	}
	if len(segs) == 1 && segs[0] == "" {
		return ""
	}
	return "/" + strings.Join(segs, "/")
}
