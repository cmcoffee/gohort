package textutil

import "testing"

func TestFirstJSONObject(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"bare":            {`{"a":1}`, `{"a":1}`},
		"prose around":    {"Sure!\n{\"a\":1}\nHope that helps.", `{"a":1}`},
		"fenced":          {"```json\n{\"a\":1}\n```", `{"a":1}`},
		"nested":          {`x {"a":{"b":2}} y`, `{"a":{"b":2}}`},
		"none":            {"no object here", ""},
		"unbalanced":      {`{"a":1`, ""},
		"first of two":    {`{"a":1} {"b":2}`, `{"a":1}`},
		"brace in string": {`{"a":"} not the end"}`, `{"a":"} not the end"}`},
		"escaped quote":   {`{"a":"say \"}\" ok"}`, `{"a":"say \"}\" ok"}`},
	} {
		if got := FirstJSONObject(tc.in); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}
