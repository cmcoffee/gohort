package orchestrate

import "testing"

// A change to an app makes earlier reads of it stale; help is one read.
func TestAppDefSaysWhatItTouches(t *testing.T) {
	for _, c := range []struct {
		args  map[string]any
		key   string
		write bool
	}{
		{map[string]any{"action": "help"}, "app_def|help", false},
		{map[string]any{"action": "get", "id": "swing-screener"}, "app|swing-screener|get:", false},
		{map[string]any{"action": "get", "id": "swing-screener", "script": "screener"}, "app|swing-screener|get:screener", false},
		{map[string]any{"action": "update", "id": "swing-screener"}, "app|swing-screener", true},
		{map[string]any{"action": "publish", "dir": "swing-screener.app"}, "app|swing-screener", true},
		{map[string]any{"action": "list"}, "", false},
	} {
		if key, w := appDefTouches(c.args); key != c.key || w != c.write {
			t.Errorf("%v: got (%q, %v), want (%q, %v)", c.args, key, w, c.key, c.write)
		}
	}
}
