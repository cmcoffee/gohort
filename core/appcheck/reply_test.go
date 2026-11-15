package appcheck

import "testing"

// A script's reply is found among what else it printed: warnings before it
// (a newer Python deprecating something), progress lines, a warning after
// it, pretty-printed or on one line, an object or an array. Output with no
// JSON at its end or start is not a reply.
func TestTheReplyIsFoundAmongWhatElseAScriptPrinted(t *testing.T) {
	warn := "news.py:193: DeprecationWarning: datetime.datetime.utcnow() is deprecated\n  now = datetime.utcnow()"
	for _, c := range []struct {
		name, out, reply, extra string
		ok                      bool
	}{
		{"whole", `{"a":1}`, `{"a":1}`, "", true},
		{"array", "  [1,2]\n", "[1,2]", "", true},
		{"warnings first", warn + "\n" + `{"items":[1]}`, `{"items":[1]}`, warn, true},
		{"pretty", "starting\n{\n  \"a\": [\n    1\n  ]\n}", "{\n  \"a\": [\n    1\n  ]\n}", "starting", true},
		{"array after a line", "fetched 3\n[{\"x\":1}]", `[{"x":1}]`, "fetched 3", true},
		{"warning after", `{"a":1}` + "\nsys:1: ResourceWarning: unclosed file", `{"a":1}`, "sys:1: ResourceWarning: unclosed file", true},
		{"no json", "Traceback (most recent call last):\n  boom", "Traceback (most recent call last):\n  boom", "", false},
		{"broken json", `{"a":`, `{"a":`, "", false},
		{"empty", "  ", "", "", false},
	} {
		reply, extra, ok := ScriptReply(c.out)
		if reply != c.reply || extra != c.extra || ok != c.ok {
			t.Errorf("%s: got (%q, %q, %v), want (%q, %q, %v)", c.name, reply, extra, ok, c.reply, c.extra, c.ok)
		}
	}
}
