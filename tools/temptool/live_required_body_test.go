package temptool

// What a toolbox action cannot run without, and the two readings of it that
// used to disagree.
//
// The narrowing exists to repair definitions whose required list named every
// declared parameter, because that was once the default and it bounced every
// call that omitted a page cursor. It asked one question — which params does
// the URL PATH need — and for a GET that is the whole story. For a POST it is
// half: the body is where a write carries what it is writing.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func requiredSet(list []string) map[string]bool {
	out := map[string]bool{}
	for _, r := range list {
		out[r] = true
	}
	return out
}

// The live failure. A write whose required list named every param was narrowed
// to the path id alone, so the schema told the model the comment's CONTENT was
// optional — and the dispatcher, reading the author's stored list, refused the
// call for omitting it. An error about an argument the schema says to omit
// cannot be fixed from the model's side, and a standing agent hit it dozens of
// times a day.
func TestABodyParamIsNotOptional(t *testing.T) {
	act := TempToolAction{
		Name:         "reply_to_comment",
		URLTemplate:  "https://x.test/api/v1/posts/{post_id}/comments",
		Method:       "POST",
		BodyTemplate: `{"parent_id": {comment_id}, "content": {content}}`,
		Params: map[string]ToolParam{
			"post_id":    {Type: "string"},
			"comment_id": {Type: "string"},
			"content":    {Type: "string"},
		},
		Required: []string{"post_id", "comment_id", "content"},
	}
	got := requiredSet(liveRequired(act))
	for _, want := range []string{"post_id", "comment_id", "content"} {
		if !got[want] {
			t.Errorf("%q is load-bearing for this write and must stay required; got %v", want, liveRequired(act))
		}
	}
}

// ...while a param the write does not send stays optional. The narrowing is
// still doing its job; it just stopped mistaking the body for decoration.
func TestAParamTheWriteNeverSendsStaysOptional(t *testing.T) {
	act := TempToolAction{
		Name:         "create_post",
		URLTemplate:  "https://x.test/api/v1/posts",
		Method:       "POST",
		BodyTemplate: `{"title": {title}, "body": {body}}`,
		Params: map[string]ToolParam{
			"title":    {Type: "string"},
			"body":     {Type: "string"},
			"idem_key": {Type: "string"}, // declared, never sent
		},
		Required: []string{"title", "body", "idem_key"},
	}
	got := requiredSet(liveRequired(act))
	if !got["title"] || !got["body"] {
		t.Errorf("body fields must be required: %v", liveRequired(act))
	}
	if got["idem_key"] {
		t.Errorf("a param neither the URL nor the body spells cannot be required: %v", liveRequired(act))
	}
}

// The case the narrowing was written for, unchanged: a read bounced for
// omitting a cursor.
func TestAReadStillNarrowsToItsPath(t *testing.T) {
	act := TempToolAction{
		Name:        "get_feed",
		URLTemplate: "https://x.test/api/v1/feeds/{feed_id}?cursor={cursor}&limit={limit}",
		Method:      "GET",
		Params: map[string]ToolParam{
			"feed_id": {Type: "string"},
			"cursor":  {Type: "string"},
			"limit":   {Type: "number"},
		},
		Required: []string{"feed_id", "cursor", "limit"},
	}
	got := liveRequired(act)
	if len(got) != 1 || got[0] != "feed_id" {
		t.Fatalf("a read must narrow to its path placeholders; got %v", got)
	}
}

// A GET with a body template is not promoted. The "_" dummy-placeholder
// pattern that satisfies an API demanding some query arg lives here, and
// turning it into a required field would bounce every legitimate call.
func TestAReadIsNotPromotedByABody(t *testing.T) {
	act := TempToolAction{
		Name:         "search",
		URLTemplate:  "https://x.test/api/v1/search",
		Method:       "GET",
		BodyTemplate: `{"q": {q}}`,
		Params: map[string]ToolParam{
			"q": {Type: "string"},
			"_": {Type: "string"},
		},
		Required: []string{"q", "_"},
	}
	if got := liveRequired(act); len(got) != 0 {
		t.Errorf("a read must not gain required params from a body; got %v", got)
	}
}

// An author who said what they meant keeps it. The repair only ever touches
// the one fingerprint it was written for.
func TestAnExplicitListIsNeverRepaired(t *testing.T) {
	act := TempToolAction{
		Name:         "reply_to_post",
		URLTemplate:  "https://x.test/api/v1/posts/{post_id}/comments",
		Method:       "POST",
		BodyTemplate: `{"content": {content}}`,
		Params: map[string]ToolParam{
			"post_id": {Type: "string"},
			"content": {Type: "string"},
			"draft":   {Type: "boolean"},
		},
		Required: []string{"post_id"}, // partial: deliberate
	}
	if got := liveRequired(act); len(got) != 1 || got[0] != "post_id" {
		t.Errorf("a partial list is deliberate and must survive: %v", got)
	}
}

// A shell action has no URL to narrow against, so the author's list stands.
func TestAShellActionKeepsItsList(t *testing.T) {
	act := TempToolAction{
		Name:            "run",
		CommandTemplate: "./bin/report {target}",
		Params: map[string]ToolParam{
			"target":   {Type: "string"},
			"work_dir": {Type: "string"},
		},
		Required: []string{"target", "work_dir"},
	}
	got := requiredSet(liveRequired(act))
	if !got["target"] || !got["work_dir"] {
		t.Errorf("a shell action's list is the author's; got %v", liveRequired(act))
	}
}

// THE structural point. The schema the model reads and the list the dispatcher
// enforces must be one list — this package has rediscovered the two-readings
// loop more than once, in both directions, and each time the model was handed
// an error it could not act on.
//
// The record is built in memory rather than through createGrouped, on purpose.
// Authoring now refuses a write whose required list names a param the call
// never sends (unsentWriteParams), so the shape that needs repairing cannot be
// created any more — it only exists in records stored before that gate, which
// is exactly the population liveRequired was written for.
func TestDispatchEnforcesTheListTheSchemaAdvertises(t *testing.T) {
	sess := &ToolSession{
		Username:      "alice",
		ChatSessionID: "s-live-req",
		DB:            &DBase{Store: kvlite.MemStore()},
	}
	// A legacy read: required names every declared param, which is the default
	// that produced these records and the reason a feed call was bounced for
	// omitting a cursor.
	rec := TempTool{
		Name:       "molt2",
		Mode:       TempToolModeToolbox,
		Credential: "no_auth",
		Actions: []TempToolAction{{
			Name:        "get_feed",
			Description: "read a feed",
			URLTemplate: "https://x.test/api/v1/feeds/{feed_id}?cursor={cursor}",
			Method:      "GET",
			Params: map[string]ToolParam{
				"feed_id": {Type: "string"},
				"cursor":  {Type: "string"},
			},
			Required: []string{"feed_id", "cursor"},
		}},
	}
	act := rec.Actions[0]
	advertised := liveRequired(act)
	if len(advertised) == 0 || len(advertised) == len(act.Required) {
		t.Fatalf("precondition: the fixture must produce a gap; advertised=%v stored=%v", advertised, act.Required)
	}

	// Supply exactly what the schema asks for. Dispatch must get past
	// validation — it will fail later reaching x.test, and that is a different
	// error with a different shape.
	args := map[string]any{"action": "get_feed"}
	for _, r := range advertised {
		args[r] = "x"
	}
	if _, err := dispatchToolboxModeTempTool(sess, &rec, args); err != nil &&
		strings.Contains(err.Error(), "missing required arg") {
		t.Fatalf("dispatch demands an argument the schema did not ask for: %v\nadvertised: %v", err, advertised)
	}

	// And the converse: omitting an advertised-required arg is still refused,
	// or reconciling the two lists would have quietly disarmed the check.
	for _, drop := range advertised {
		partial := map[string]any{"action": "get_feed"}
		for _, r := range advertised {
			if r != drop {
				partial[r] = "x"
			}
		}
		_, err := dispatchToolboxModeTempTool(sess, &rec, partial)
		if err == nil || !strings.Contains(err.Error(), "missing required arg") {
			t.Errorf("omitting the required %q was not refused: %v", drop, err)
		}
	}
}
