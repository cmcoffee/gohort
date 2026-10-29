package bundle

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// A search of several patterns answers each as if it had been searched alone:
// the same hits with the same context, the same counts and cuts, groups and
// rows over time, in every mode. The patterns overlap (one line matches two),
// one is noisy, one rare, one absent. Searched together on four workers in
// chunks of a slice, against each alone on one worker reading whole files.
// Every case leaves the limit room: a lane that fills it reads on with the
// others, which TestAFullLaneDoesNotStopTheOthers covers.
func TestSeveralPatternsAnswerAsEachAlone(t *testing.T) {
	withMemStore(t)
	files := map[string]string{}
	for f, n := range []int{SliceLines*2 + 40, 300, 90, 700} {
		var b strings.Builder
		for i := 1; i <= n; i++ {
			kind := []string{"INFO ok %d", "ERROR timeout after %dms", "WARN refused %d", "INFO ok %d", "ERROR timeout healthcheck %d"}[(i+f)%5]
			if i%211 == 0 {
				kind = "ERROR oom killer took %d"
			}
			fmt.Fprintf(&b, "2026-03-14T%02d:%02d:%02dZ "+kind+"\n", f+1, (i/60)%60, i%60, i)
		}
		files[fmt.Sprintf("svc-%d.log", f)] = b.String()
	}
	stage := writeStage(t, files)
	if _, err := Open("u1", "b1").Ingest(context.Background(), stage); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	prevW, prevC := SearchWorkers, ChunkSlices
	defer func() { SearchWorkers, ChunkSlices = prevW, prevC }()

	patterns := []string{"error", "timeout", "oom|killer", "no such thing"}
	since := time.Date(2026, 3, 14, 1, 10, 0, 0, time.UTC)
	until := time.Date(2026, 3, 14, 3, 30, 0, 0, time.UTC)
	for name, q := range map[string]Query{
		"context":   {Before: 2, After: 1, PerFile: 30},
		"shares":    {PerFile: 7, After: 2},
		"exclude":   {Exclude: "healthcheck", PerFile: 25},
		"count":     {CountOnly: true},
		"group":     {Group: true},
		"over-time": {OverTime: true, Bucket: 10 * time.Minute},
		"window":    {Since: since, Until: until, Before: 1, PerFile: 30},
	} {
		SearchWorkers, ChunkSlices = 4, 1
		mq := q
		mq.Pattern, mq.Patterns = patterns[0], patterns[1:]
		multi, err := Open("u1", "b1").Search(context.Background(), mq)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(multi.Lanes) != len(patterns) {
			t.Fatalf("%s: %d lanes for %d patterns", name, len(multi.Lanes), len(patterns))
		}
		SearchWorkers, ChunkSlices = 1, 1<<30
		for l, p := range patterns {
			aq := q
			aq.Pattern = p
			alone, err := Open("u1", "b1").Search(context.Background(), aq)
			if err != nil {
				t.Fatal(err)
			}
			got := multi.Lane(l)
			label := fmt.Sprintf("%s, %q", name, p)
			if got.Lanes != nil || multi.Lanes[l].Pattern != p {
				t.Errorf("%s: lane %d is %q", label, l, multi.Lanes[l].Pattern)
			}
			if !reflect.DeepEqual(alone.Hits, got.Hits) {
				t.Errorf("%s: hits differ: %d alone, %d together", label, len(alone.Hits), len(got.Hits))
			}
			if !reflect.DeepEqual(alone.Budget, got.Budget) || alone.Excluded != got.Excluded || alone.Truncated != got.Truncated || alone.Scanned != got.Scanned {
				t.Errorf("%s: counts differ:\nalone    %+v excluded %d\ntogether %+v excluded %d", label, alone.Budget, alone.Excluded, got.Budget, got.Excluded)
			}
			if q.Group && !reflect.DeepEqual(alone.Groups.Sorted(), got.Groups.Sorted()) {
				t.Errorf("%s: groups differ", label)
			}
			if q.OverTime && alone.Histogram.Render("x", q.Bucket) != got.Histogram.Render("x", q.Bucket) {
				t.Errorf("%s: rows over time differ", label)
			}
		}
		// The cases have to have something to compare: the noisy lane and
		// the rare one both matched, and the absent one did not.
		if multi.Lanes[0].Total == 0 || multi.Lanes[2].Total == 0 || multi.Lanes[3].Total != 0 {
			t.Errorf("%s: lane totals %d/%d/%d/%d", name, multi.Lanes[0].Total, multi.Lanes[1].Total, multi.Lanes[2].Total, multi.Lanes[3].Total)
		}
	}
}

// One lane filling its limit does not stop the search while another still
// has room: the rare pattern is found in files the noisy one's limit would
// have left unread, and the noisy one keeps the hits it would have kept
// alone, counted over more files.
func TestAFullLaneDoesNotStopTheOthers(t *testing.T) {
	withMemStore(t)
	files := map[string]string{}
	for f := 0; f < 6; f++ {
		var b strings.Builder
		for i := 0; i < 40; i++ {
			fmt.Fprintf(&b, "2026-03-14T01:00:%02dZ ERROR noisy %d\n", i, i)
		}
		if f == 5 {
			b.WriteString("2026-03-14T01:01:00Z FATAL rare one\n")
		}
		files[fmt.Sprintf("f%d.log", f)] = b.String()
	}
	stage := writeStage(t, files)
	if _, err := Open("u1", "b1").Ingest(context.Background(), stage); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	alone, _ := Open("u1", "b1").Search(context.Background(), Query{Pattern: "noisy", MaxHits: 30, PerFile: 10})
	if alone.Unsearched == 0 {
		t.Fatalf("the noisy pattern alone should leave files unread: %+v", alone.Budget)
	}
	res, err := Open("u1", "b1").Search(context.Background(), Query{Patterns: []string{"noisy", "rare"}, MaxHits: 30, PerFile: 10})
	if err != nil {
		t.Fatal(err)
	}
	noisy, rare := res.Lane(0), res.Lane(1)
	if len(rare.Hits) != 1 || rare.Hits[0].Path != "f5.log" {
		t.Errorf("rare lane: %+v", rare.Hits)
	}
	if !reflect.DeepEqual(alone.Hits, noisy.Hits) || noisy.Total != 240 || noisy.Unsearched != 0 || !noisy.Truncated {
		t.Errorf("noisy lane: %d hits (alone %d), total %d, unsearched %d", len(noisy.Hits), len(alone.Hits), noisy.Total, noisy.Unsearched)
	}
}

func TestPatternsList(t *testing.T) {
	got, err := Patterns(" a ", []string{"b", "", "a", "c "})
	if err != nil || !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("Patterns: %q, %v", got, err)
	}
	if _, err := Patterns("", []string{" "}); err == nil {
		t.Error("no patterns should be an error")
	}
	if _, err := Patterns("p", []string{"1", "2", "3", "4", "5", "6", "7", "8"}); err == nil {
		t.Errorf("more than %d patterns should be an error", MaxPatterns)
	}
	if _, err := Open("u", "b").Search(context.Background(), Query{Patterns: []string{"ok", "(bad"}}); err == nil || !strings.Contains(err.Error(), `"(bad"`) {
		t.Errorf("a bad pattern among several should be named: %v", err)
	}
}

func TestPatternsArgTakesWhatModelsSend(t *testing.T) {
	for _, c := range []struct {
		in   any
		want []string
	}{
		{[]any{"a,b", "c"}, []string{"a,b", "c"}},
		{`["x{1,3}", "y"]`, []string{"x{1,3}", "y"}},
		{"timeout, refused", []string{"timeout, refused"}},
		{"", nil},
		{nil, nil},
	} {
		if got := PatternsArg(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("PatternsArg(%#v) = %q, want %q", c.in, got, c.want)
		}
	}
}
