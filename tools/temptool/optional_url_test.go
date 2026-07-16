package temptool

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// TestSubstituteURLOptionalQueryDrop verifies that an optional query param
// (not in required) drops out of the URL when the caller omits it, while
// provided params and required/path params behave as before.
func TestSubstituteURLOptionalQueryDrop(t *testing.T) {
	params := map[string]ToolParam{
		"sort":  {Type: "string"},
		"limit": {Type: "integer"},
		"id":    {Type: "string"},
	}
	cases := []struct {
		name     string
		tmpl     string
		required []string
		args     map[string]any
		want     string
		wantErr  bool
	}{
		{
			name:     "both optional omitted → clean base url",
			tmpl:     "https://x.test/api/v1/posts?sort={sort}&limit={limit}",
			required: []string{}, // explicit none-required
			args:     map[string]any{},
			want:     "https://x.test/api/v1/posts",
		},
		{
			name:     "one optional provided → only it remains",
			tmpl:     "https://x.test/api/v1/posts?sort={sort}&limit={limit}",
			required: []string{},
			args:     map[string]any{"limit": 5},
			want:     "https://x.test/api/v1/posts?limit=5",
		},
		{
			name:     "both provided → both remain",
			tmpl:     "https://x.test/api/v1/posts?sort={sort}&limit={limit}",
			required: []string{},
			args:     map[string]any{"sort": "new", "limit": 5},
			want:     "https://x.test/api/v1/posts?sort=new&limit=5",
		},
		{
			name:     "path placeholder still substitutes",
			tmpl:     "https://x.test/api/v1/posts/{id}?sort={sort}",
			required: []string{"id"},
			args:     map[string]any{"id": "abc"},
			want:     "https://x.test/api/v1/posts/abc",
		},
		{
			name:     "required query param absent → error (not silently dropped)",
			tmpl:     "https://x.test/api/v1/posts?sort={sort}",
			required: []string{"sort"},
			args:     map[string]any{},
			wantErr:  true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := substituteURL(c.tmpl, params, c.required, c.args)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}
