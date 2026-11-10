package imagefetch

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	_ "github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/snugforge/kvlite"
)

// attemptsWorld is an edit backend that blends two pictures, the default, and
// a session in a conversation with two source pictures recorded. Each
// render returns a different picture, so a result is never mistaken for a
// source.
func attemptsWorld(t *testing.T, llm *FakeLLM) (*ToolSession, imageActions, []string, *int) {
	t.Helper()
	savedDB, savedAuth, savedProvider, savedCaption := RootDB, AuthDB, ImageProviderFunc, CaptionOnRecord
	RootDB = &DBase{Store: kvlite.MemStore()}
	auth := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return auth }
	ImageProviderFunc = func() string { return "blender" }
	CaptionOnRecord = false
	t.Cleanup(func() {
		UnapproveConnector(RootDB, "blender")
		RootDB, AuthDB, ImageProviderFunc, CaptionOnRecord = savedDB, savedAuth, savedProvider, savedCaption
		attempts.Lock()
		attempts.made = map[string]attemptCount{}
		attempts.Unlock()
	})
	// A connector that edits (the Gemini template's), with its renderer
	// swapped for one that answers here.
	raw, err := os.ReadFile("../../core/recipes/builtin/gemini-images.json")
	if err != nil {
		t.Fatal(err)
	}
	var tmpl struct {
		Bundle struct {
			Artifacts []struct {
				Recipe struct {
					Spec json.RawMessage `json:"spec"`
				} `json:"recipe"`
			} `json:"artifacts"`
		} `json:"bundle"`
	}
	if err := json.Unmarshal(raw, &tmpl); err != nil {
		t.Fatal(err)
	}
	if err := SaveConnector(RootDB, Connector{Name: "blender", Kind: "service", Spec: tmpl.Bundle.Artifacts[0].Recipe.Spec}); err != nil {
		t.Fatal(err)
	}
	RootDB.Set(ImageTable, "provider", "blender")
	renders := 0
	RegisterImageEditBackend("blender", func() int { return 2 }, func(sess *ToolSession, prompt string, images [][]byte) (*ImageGenResult, error) {
		renders++
		f := filepath.Join(t.TempDir(), "out.png")
		if err := os.WriteFile(f, shade(uint8(100+renders)), 0o644); err != nil {
			return nil, err
		}
		return &ImageGenResult{URL: f, Prompt: prompt}, nil
	})
	sess := checkSession(t, llm)
	sess.ChatSessionID = "convo-1"
	// Stable refs: a positional one moves as each result is saved.
	_, ship1 := RecordRecentImageStable(sess, shade(10), "a ship", ImageFromUser)
	_, ship2 := RecordRecentImageStable(sess, shade(20), "another ship", ImageFromUser)
	refs := []string{ship1, ship2}
	return sess, editActions(ImageBackendChoice{Name: "blender", Default: true, Edits: true}), refs, &renders
}

// shade is a small picture of one grey.
func shade(v uint8) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := range img.Pix {
		img.Pix[i] = v
	}
	img.Set(0, 0, color.RGBA{v, v, v, 255})
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

// checkFound is the source check's model turns for two sources, both found.
func checkFound() []FakeTurn {
	return describeTurns(3, "Source 1 takes: the ship\nSource 1 in result: the ship\nSource 1 FOUND: yes\n"+
		"Source 2 takes: the ship\nSource 2 in result: the ship\nSource 2 FOUND: yes")
}

// The checking level decides what an edit from source pictures costs and how
// many pictures the same sources may give. Standard (the default) checks with
// a quick judge and gives up after two; Thorough's judge thinks it over and
// allows three; Quick checks nothing and allows one. Past the limit the edit
// refuses before rendering, and the last picture allowed says so.
func TestAnEditGivesUpAtItsCheckingLevel(t *testing.T) {
	llm := &FakeLLM{Turns: append(checkFound(), checkFound()...)}
	sess, avail, refs, renders := attemptsWorld(t, llm)
	edit := func(extra map[string]any) (string, error) {
		args := map[string]any{"prompt": "the two ships side by side", "images": []any{refs[0], refs[1]}}
		for k, v := range extra {
			args[k] = v
		}
		return editImage(sess, args, avail)
	}

	out, err := edit(nil)
	if err != nil {
		t.Fatalf("first edit: %v", err)
	}
	if strings.Contains(out, "ATTEMPTS") {
		t.Errorf("the first of two says it was the last: %s", out)
	}
	if got := llm.Calls(); got != 4 {
		t.Fatalf("standard checks: 3 looks and a judge, got %d calls", got)
	}
	if think := llm.Config(3).Think; think == nil || *think {
		t.Errorf("standard's judge must not think it over: %v", think)
	}
	out, err = edit(nil)
	if err != nil || !strings.Contains(out, "picture 2 of the 2 allowed") {
		t.Fatalf("the second, and last: %v %s", err, out)
	}
	if _, err = edit(nil); err == nil || !strings.Contains(err.Error(), "no picture was made") || *renders != 2 {
		t.Fatalf("a third is refused before it renders: %v, %d renders", err, *renders)
	}

	// The same pictures passed another way are the same sources.
	if _, err = editImage(sess, map[string]any{"prompt": "again", "images": []any{refs[1], refs[0]}}, avail); err == nil {
		t.Error("the same sources in another order counted afresh")
	}

	// Thorough, asked for on the call, allows a third; its judge thinks.
	llm.Turns = append(llm.Turns, checkFound()...)
	out, err = edit(map[string]any{"checking": "thorough"})
	if err != nil || !strings.Contains(out, "picture 3 of the 3 allowed") {
		t.Fatalf("thorough's third: %v %s", err, out)
	}
	if think := llm.Config(llm.Calls() - 1).Think; think == nil || !*think {
		t.Errorf("thorough's judge thinks it over: %v", think)
	}

	// Quick, in another conversation: no check at all, and one picture.
	sess.ChatSessionID = "convo-2"
	before := llm.Calls()
	out, err = edit(map[string]any{"checking": "quick"})
	if err != nil || llm.Calls() != before || !strings.Contains(out, "picture 1 of the 1 allowed") || strings.Contains(out, "AUTOMATIC CHECK") {
		t.Fatalf("quick: %v, %d model calls, %s", err, llm.Calls()-before, out)
	}
	if _, err = edit(map[string]any{"checking": "quick"}); err == nil {
		t.Error("quick allows one picture from the same sources")
	}
}

// A picture made from a prompt alone, or with no conversation to count in, is
// not an attempt at a picture: nothing counts it, and the limit never bites.
func TestOnlyAnEditInAConversationIsCounted(t *testing.T) {
	sess := &ToolSession{Username: "alice"}
	if attemptKey(sess, []string{"image#1"}) != "" {
		t.Error("no conversation, no count")
	}
	sess.ChatSessionID = "c"
	if attemptKey(sess, nil) != "" {
		t.Error("no sources, no count")
	}
	other := &ToolSession{Username: "bob", ChatSessionID: "c"}
	if attemptKey(sess, []string{"a.png"}) == attemptKey(other, []string{"a.png"}) {
		t.Error("two people's attempts are counted apart")
	}
	if attemptKey(&ToolSession{Username: "alice", ChatSessionID: "scheduled:c", DeliverySessionID: "c"}, []string{"a.png"}) != attemptKey(sess, []string{"a.png"}) {
		t.Error("a wake turn's retry is the same conversation's attempt")
	}
}

// The pictures an image call names reach the backend in one order: the
// picture to change, then the references. The older images list still works
// and still means its first picture is the one changed; references alone make
// a new picture.
func TestThePicturesAnImageCallNames(t *testing.T) {
	for _, tc := range []struct {
		args  map[string]any
		want  string
		edits bool
	}{
		{map[string]any{"picture": "image#p", "references": []any{"image#a", "image#b"}}, "image#p,image#a,image#b", true},
		{map[string]any{"references": []any{"image#a"}}, "image#a", false},
		{map[string]any{"images": []any{"image#x", "image#y"}}, "image#x,image#y", true},
		{map[string]any{"picture": "image#p", "images": []any{"image#x"}}, "image#p", true},
		{map[string]any{"prompt": "a cat"}, "", false},
	} {
		if got := strings.Join(imageSources(tc.args), ","); got != tc.want || editsPicture(tc.args) != tc.edits {
			t.Errorf("%v: %q (edits %v), want %q (edits %v)", tc.args, got, editsPicture(tc.args), tc.want, tc.edits)
		}
	}
	if !rendersFromPictures(map[string]any{"action": "edit"}) {
		t.Error("an edit naming no picture went to the text-only render, not to the refusal")
	}
}
