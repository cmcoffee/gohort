package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	rules "github.com/cmcoffee/gohort/core/prompts"
	"github.com/cmcoffee/snugforge/kvlite"
)

// What the Rules section gets back, through the real routes and the gate in
// front of them: the Always rules with how they are checked, read and saved
// (a save that sends only the rules leaving how they are checked alone), and
// the Style rules, where a shipped rule is a line to keep or delete and the
// other lines are the deployment's own. Each as an administrator, as a user
// who is not one, by the wrong method and with a broken or missing body.
// Compared with testdata/rules_contract.golden.
//
// Run with UPDATE_GOLDEN=1 to rewrite the golden after a change that is meant.
func TestRulesRoutesContract(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prevAuth, prevRoot := AuthDB, RootDB
	AuthDB = func() Database { return adb }
	RootDB = &DBase{Store: kvlite.MemStore()}
	SetPromptOverrideDB(&DBase{Store: kvlite.MemStore()})
	t.Cleanup(func() {
		SetPromptOverrideDB(nil)
		AuthDB, RootDB = prevAuth, prevRoot
	})

	// The shipped style rules are the code's, and change with it: the style
	// list is recorded without them, and the test moves them by their text.
	builtin := map[string]bool{}
	var shipped []string
	for _, b := range rules.BuiltinStyleRules() {
		builtin[strings.TrimSpace(b.Text)] = true
		shipped = append(shipped, strings.TrimSpace(b.Text))
	}
	if len(shipped) < 2 {
		t.Fatal("the test needs two shipped style rules to move")
	}
	ownLines := func(out string) string {
		var m map[string]any
		if json.Unmarshal([]byte(out), &m) != nil {
			return out
		}
		text, ok := m["rules"].(string)
		if !ok {
			return out
		}
		var own []string
		n := 0
		for _, l := range strings.Split(text, "\n") {
			if builtin[strings.TrimSpace(l)] {
				n++
				continue
			}
			own = append(own, l)
		}
		m["rules"] = strings.Join(own, "\n")
		switch len(shipped) - n {
		case 0:
			m["shipped_lines_left_out"] = "all"
		default:
			m["shipped_lines_left_out"] = fmt.Sprintf("all but %d", len(shipped)-n)
		}
		b, _ := json.Marshal(m)
		return string(b)
	}

	// And in what is posted: the two shipped rules the test moves by name,
	// the rest as one, so a rule added to the code does not move the golden.
	esc := func(text string) string {
		b, _ := json.Marshal(text)
		return strings.Trim(string(b), `"`)
	}
	rest := strings.Join(shipped[2:], "\n")
	masks := []string{}
	if rest != "" {
		masks = append(masks, esc(rest), "<the other shipped rules>")
	}
	masks = append(masks, esc(shipped[0]), "<shipped 1>", esc(shipped[1]), "<shipped 2>")
	ct := &contract{rewrite: strings.NewReplacer(masks...)}
	app := &AdminApp{}
	mux := http.NewServeMux()
	app.RegisterRoutes(mux, "")
	do := func(user, method, path, body string) {
		ct.shape = nil
		if strings.HasPrefix(path, "/api/style-rules") {
			ct.shape = ownLines
		}
		ct.run(mux, adb, user, method, path, body)
	}
	lines := func(ls ...string) string {
		b, _ := json.Marshal(map[string]string{"rules": strings.Join(ls, "\n")})
		return string(b)
	}

	for _, p := range []string{"/api/global-rules", "/api/style-rules"} {
		do("", "GET", p, "")
		do("alice", "GET", p, "")
		do("root", "GET", p, "")
		do("root", "PUT", p, "{}")
		do("alice", "POST", p, `{"rules":"be kind"}`)
		do("root", "POST", p, `{"rules":`)
		do("root", "POST", p, ``)
	}

	do("root", "POST", "/api/global-rules", `{"rules":"Never share a password.\n\n  Ask before deleting anything.  ","depth":"thorough","if_unchecked":"allow","when_checked":"stream"}`)
	do("root", "GET", "/api/global-rules", "")
	do("root", "POST", "/api/global-rules", `{"rules":"Never share a password."}`)
	do("root", "GET", "/api/global-rules", "")
	do("root", "POST", "/api/global-rules", `{"rules":"","depth":"quick","if_unchecked":"block","when_checked":"whole"}`)
	do("root", "GET", "/api/global-rules", "")

	// Every shipped rule but the first, and two of the deployment's own.
	do("root", "POST", "/api/style-rules", lines(append(append([]string{}, shipped[1:]...), "Write dates as YYYY-MM-DD.", "  Sign off with the team name.  ")...))
	do("root", "GET", "/api/style-rules", "")
	// The first one back, and the second's wording changed: a line no
	// shipped rule says, so it is the deployment's own rule and the second
	// shipped rule goes off.
	do("root", "POST", "/api/style-rules", lines(append([]string{shipped[0], shipped[1] + " (edited)"}, shipped[2:]...)...))
	do("root", "GET", "/api/style-rules", "")
	do("root", "POST", "/api/style-rules", lines(shipped...))
	do("root", "GET", "/api/style-rules", "")
	// A shipped rule already reworded (its override in force), saved with
	// that wording: it stays the shipped rule, in the deployment's words.
	rules.SetPromptOverride(rules.BuiltinStyleRules()[1].Key, "Our own wording of the second rule.")
	do("root", "POST", "/api/style-rules", lines(append([]string{shipped[0], "Our own wording of the second rule."}, shipped[2:]...)...))
	do("root", "GET", "/api/style-rules", "")

	ct.check(t, "testdata/rules_contract.golden")
}
