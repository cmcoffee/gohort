package orchestrate

// Every route this app serves without an admin gate must resolve the SESSION
// user and work in that user's own store.
//
// The gate used to be the answer to "who may touch this", wrapped around all
// sixty routes, and it was the wrong answer twice over: it kept users out of
// their own agents, and it hid the fact that each handler already decides for
// itself. Taking it off is only safe because they do — so this pins the
// property the removal rests on, rather than trusting that it holds today and
// will still hold when somebody adds route sixty-one.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// sessionlessByDesign are the routes an outside system calls, which therefore
// cannot have a session. They authenticate some other way, and the exemption
// names that way so it cannot quietly become "no check at all".
//
// handleOperatorEvent is a webhook: a secret token in the path resolves to one
// event monitor, and failures are rate-limited by source so a caller cannot buy
// a scan of every monitor repeatedly.
var sessionlessByDesign = map[string]bool{
	"handleOperatorEvent": true,
}

func TestUserScopedRoutesResolveASessionUser(t *testing.T) {
	var routes strings.Builder
	for _, f := range []string{"orchestrate.go", "console.go"} {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		routes.Write(raw)
	}
	// Every handler named in an UNGATED HandleFunc on this app, including the
	// ones wrapped in the method-only guard: that one is a CSRF protection,
	// not an ACL, so a route wearing it still has to say who is calling.
	ungated := regexp.MustCompile(`T\.HandleFunc\("[^"]*",\s*w?\(?(T\.[A-Za-z]+)\)?\)`)
	var names []string
	for _, m := range ungated.FindAllStringSubmatch(routes.String(), -1) {
		names = append(names, strings.TrimPrefix(m[1], "T."))
	}
	if len(names) < 90 {
		t.Fatalf("only found %d ungated routes; the pattern has stopped matching", len(names))
	}

	bodies := packageSource(t)
	for _, name := range names {
		body, ok := methodBody(bodies, name)
		if !ok {
			t.Errorf("handler %s is routed but not found in this package", name)
			continue
		}
		if sessionlessByDesign[name] {
			// Verify the exemption still holds rather than trusting the
			// list: a route that stops authenticating some other way is
			// exactly the one this must catch.
			if !strings.Contains(body, "FindEventMonitorByToken") {
				t.Errorf("%s is exempt from the session check but no longer authenticates by token", name)
			}
			continue
		}
		if resolvesAUser(bodies, body, 3) {
			continue
		}
		t.Errorf("%s is served without an admin gate and never resolves a session user: "+
			"it would act on whatever the request names, for anybody signed in", name)
	}
}

// resolvesAUser looks for the identity call, following ONE hop into helpers a
// router delegates to — several of these handlers are two lines that dispatch
// on the path, and the check belongs wherever the work is.
func resolvesAUser(src, body string, depth int) bool {
	if strings.Contains(body, "RequireUser(") || strings.Contains(body, "AuthCurrentUser(") ||
		strings.Contains(body, "sessionStateQuery(") {
		return true
	}
	if depth <= 0 {
		return false
	}
	for _, m := range regexp.MustCompile(`T\.([a-zA-Z]+)\(`).FindAllStringSubmatch(body, -1) {
		if inner, ok := methodBody(src, m[1]); ok && resolvesAUser(src, inner, depth-1) {
			return true
		}
	}
	return false
}

func packageSource(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package: %v", err)
	}
	var b strings.Builder
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		b.Write(raw)
		b.WriteString("\n")
	}
	return b.String()
}

// methodBody returns the source of a method on OrchestrateApp, from its
// signature to the closing brace at column zero.
func methodBody(src, name string) (string, bool) {
	i := strings.Index(src, "func (T *OrchestrateApp) "+name+"(")
	if i < 0 {
		return "", false
	}
	rest := src[i:]
	if end := strings.Index(rest, "\n}\n"); end > 0 {
		return rest[:end], true
	}
	return rest, true
}
