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

// noteToolboxReadCalls adds the read-marked actions of the named toolbox to
// its credential's read calls, when the session's user owns that credential.
func noteToolboxReadCalls(sess *ToolSession, name string) {
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
	var calls []string
	for _, a := range tt.Actions {
		if !a.Reads || !isMutatingMethod(a.Method) {
			continue
		}
		if p := readCallPath(a.URLTemplate); p != "" {
			calls = append(calls, strings.ToUpper(strings.TrimSpace(a.Method))+" "+p)
		}
	}
	if len(calls) == 0 {
		return
	}
	if err := Secure().AddReadCalls(sess.Username, tt.Credential, calls); err != nil {
		Log("[temptool] %s: could not note %q's read calls on %q: %v", sess.Username, name, tt.Credential, err)
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
