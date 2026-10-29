package bundle

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestLevelsAreReadFromTheLineItself(t *testing.T) {
	for line, want := range map[string]string{
		"2026-03-14T01:00:00Z ERROR db gone":                   "ERROR",
		"2026-03-14T01:00:00Z [WARNING] slow":                  "WARN",
		`{"time":"2026-03-14T01:00:00Z","level":"error"}`:      "ERROR",
		`{"level": "warn", "msg": "an ERROR word in the msg"}`: "WARN", // the field, not the word
		"ts=2026-03-14T01:00:00Z level=info msg=ok":            "INFO",
		"severity: CRITICAL disk":                              "CRIT",
		"an error in prose":                                    "",
		"\tat com.x.Y(Y.java:1)":                               "",
	} {
		if got := severityOf(line); got != want {
			t.Errorf("severityOf(%q) = %q, want %q", line, got, want)
		}
	}
	for in, want := range map[string]int{"": 0, "error": 4, "ERR": 4, "warning": 5, "Fatal": 3, "panic": 3, "debug": 8} {
		if got, err := ParseLevel(in); err != nil || got != want {
			t.Errorf("ParseLevel(%q) = %d, %v", in, got, err)
		}
	}
	if _, err := ParseLevel("loud"); err == nil || !strings.Contains(err.Error(), "fatal, error, warn") {
		t.Errorf("an unknown level should say which there are: %v", err)
	}
}

// level keeps the lines at a severity or worse, however the file writes its
// level, in every mode; with no pattern it means every such line; a line
// with no level of its own is left out, and record=true brings the entry
// around a kept line back.
func TestBundleSearchByLevel(t *testing.T) {
	withMemStore(t)
	stage := writeStage(t, map[string]string{
		"app.log": "2026-03-14T01:00:00Z INFO started\n2026-03-14T01:01:00Z WARN slow query\n2026-03-14T01:02:00Z ERROR db gone\n\tat db.Conn(C.java:1)\n2026-03-14T01:03:00Z DEBUG tick\n",
		"svc.json": `{"time":"2026-03-14T01:00:30Z","level":"error","msg":"upstream refused"}` + "\n" +
			`{"time":"2026-03-14T01:01:30Z","level":"info","msg":"an ERROR word in an info line"}` + "\n",
		"kv.log": "ts=2026-03-14T01:02:30Z level=warn msg=retrying\n",
	})
	if _, err := Open("u1", "b1").Ingest(context.Background(), stage); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	search := func(q Query) SearchResult {
		t.Helper()
		res, err := Open("u1", "b1").Search(context.Background(), q)
		if err != nil {
			t.Fatalf("%+v: %v", q, err)
		}
		return res
	}
	lines := func(res SearchResult) string {
		var out []string
		for _, h := range res.Hits {
			out = append(out, fmt.Sprintf("%s:%d", h.Path, h.Line))
		}
		return strings.Join(out, " ")
	}
	if got := lines(search(Query{Level: "warn"})); got != "app.log:2 app.log:3 kv.log:1 svc.json:1" {
		t.Errorf("every line at warn or worse = %s", got)
	}
	if got := lines(search(Query{Pattern: "error|refused", Level: "error"})); got != "app.log:3 svc.json:1" {
		t.Errorf("a pattern at error = %s (the info line with ERROR in its message is not an error)", got)
	}
	if res := search(Query{Level: "error", CountOnly: true}); res.Total != 2 {
		t.Errorf("count at error = %d", res.Total)
	}
	if res := search(Query{Level: "error", OverTime: true, Bucket: time.Hour}); res.Histogram.Lines != 2 {
		t.Errorf("errors over time = %d", res.Histogram.Lines)
	}
	res := search(Query{Pattern: "db gone", Level: "error", Records: true})
	if len(res.Hits) != 1 || len(res.Hits[0].Context) != 2 || !strings.Contains(res.Hits[0].Context[1], "at db.Conn") {
		t.Errorf("record at error should bring the frame back: %+v", res.Hits)
	}
	if lines(search(Query{Pattern: "db.Conn", Level: "debug"})) != "" {
		t.Error("a line with no level of its own is left out by any level")
	}
	if out := RenderSearch(search(Query{Pattern: "db.Conn", Level: "error"}), Query{Pattern: "db.Conn", Level: "error"}, "read"); !strings.Contains(out, "Only lines declaring ERROR or worse") {
		t.Errorf("a miss under a level should say what it left out:\n%s", out)
	}
	if _, err := Open("u1", "b1").Search(context.Background(), Query{Pattern: "x", Level: "loud"}); err == nil {
		t.Error("an unknown level is an error, not a silent no-op")
	}
}
