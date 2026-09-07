package imagefetch

import (
	"fmt"
	_ "image/gif"
	_ "image/png"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	_ "golang.org/x/image/webp" // register the WebP decoder for image.Decode
)

// editPlan is an edit call reduced to what will actually run: which backend,
// with what prompt, over which source references.
type editPlan struct {
	backend string
	prompt  string
	refs    []string
	mask    string
	// refineFaces is the second, face-only render (core/image_face_refine.go).
	// Default ON, which is why it lives on the plan rather than being read at
	// the call site: "absent" and "false" are opposite instructions here, and a
	// plain BoolArg collapses them.
	refineFaces bool
	// note is what the pre-dispatch subject check rewrote, in words for the
	// model. Carried on the plan rather than returned separately so Preflight
	// and the real call run identical checks — a check that only one of them
	// performs is one the detached path skips.
	note string
}

// planEdit does every check an edit can make from its arguments alone — which
// is all of them except the render itself.
//
// Split out from editImage so the SAME checks can run before the call detaches
// (see ImageTool.Preflight). A render long enough to detach is long enough that
// "you named a backend that can't edit" would otherwise come back a minute
// after the agent already promised the user a picture.
func planEdit(sess *ToolSession, args map[string]any, avail imageActions) (editPlan, error) {
	var p editPlan
	if !avail.edit {
		return p, fmt.Errorf("the edit action is unavailable — no image backend here is wired for image input (img2img / inpaint). Tell the user editing isn't set up; do NOT retry")
	}
	prompt := strings.TrimSpace(StringArg(args, "prompt"))
	refs := stringsArg(args, "images")
	if len(refs) == 0 {
		hint := "pass the image to change"
		if m := RecentImageManifest(sess); m != "" {
			hint = m
		}
		return p, fmt.Errorf("edit needs at least one source image — %s", hint)
	}
	// Before anything is dispatched: take attached people's NAMES out of the
	// prompt, and refuse outright if it names somebody whose picture we have
	// and did not pass. Here rather than after the render because a render that
	// invented the wrong face has already cost the time and is already
	// deliverable — a note under it is something a model can read and ship
	// anyway. See prompt_scrub.go.
	prompt, note, err := buildEditPrompt(sess, prompt, refs)
	if err != nil {
		return p, err
	}
	backend := strings.TrimSpace(StringArg(args, "backend"))
	if backend == "" {
		// Routed by how many pictures were passed — see defaultEditBackend.
		backend = defaultEditBackend(avail, len(refs))
	}
	// Argument checks BEFORE the reachability check, so a wrong argument is
	// named as one. Reachability is the broadest failure — run it first and
	// every mistake reports as "backend not available", which sends the model
	// looking for a permissions problem it doesn't have.
	//
	// This doesn't weaken the boundary: avail.editors is itself derived from
	// ReachableImageBackends, so anything passing isEditor was already
	// reachable, and the explicit check below still gates the dispatch.
	if !isEditor(avail, backend) {
		if len(avail.editors) == 0 {
			return p, fmt.Errorf("no image backend here can edit photos")
		}
		return p, fmt.Errorf("image backend %q creates from text and cannot work from a source picture — use one of: %s, or drop images to render from the prompt alone", backend, strings.Join(editorNames(avail), ", "))
	}
	// Count mismatch, named in terms of what this deployment can do. Checked
	// here rather than left to the backend so the answer arrives before an
	// upload and names the alternatives: "wrong number of images" sends the
	// model guessing, "this can combine 2, you passed 3" does not.
	if n := backendImageCount(avail, backend); n > 0 && len(refs) != n {
		counts := editorImageCounts(avail)
		strs := make([]string, 0, len(counts))
		for _, c := range counts {
			strs = append(strs, strconv.Itoa(c))
		}
		// Too MANY and too FEW need opposite advice, and one sentence for both
		// told a caller with a spare picture to go and find another one.
		fix := "Choose the %d that matter and pass those, or do it in more than one call."
		if len(refs) < n {
			fix = "Supply %d, or ask the person for the missing one(s) before trying again."
		}
		return p, fmt.Errorf("you passed %s, and this deployment composes %s at a time — %q takes exactly %d. "+fix,
			pluralPictures(len(refs)), joinCounts(editorImageCounts(avail)), backend, n, n)
	}
	// Some editing workflows have no text node at all — a blend or an upscale is
	// pure pixel work. Demanding a prompt there makes the model invent one that
	// goes nowhere.
	if prompt == "" && backendNeedsPrompt(avail, backend) {
		return p, fmt.Errorf("prompt is required for this backend — describe what should CHANGE (e.g. \"make it snowy\", \"put the subject on a beach\")")
	}
	// ENFORCEMENT, same as generate: the enum is a hint, this is the boundary.
	if !ImageBackendReachable(sess, backend) {
		return p, fmt.Errorf("image backend %q is not available to you — use one of: %s", backend, strings.Join(editorNames(avail), ", "))
	}
	return editPlan{
		backend: backend, prompt: prompt, refs: refs,
		mask: StringArg(args, "mask"),
		// Gated on the BACKEND, not just the argument: the pass needs a second
		// input slot for the identity reference, and asking for it where there
		// is none would spend a whole extra render sharpening a stranger.
		refineFaces: avail.canRefineFaces() && boolArgDefault(args, "preserve_faces", true),
		note:        note,
	}, nil
}

// boolArgDefault reads a boolean argument whose default is not false.
//
// BoolArg cannot express one: it answers false for a key that is absent and for
// a key that is present and false, and for a default-ON switch those are
// opposite instructions.
func boolArgDefault(args map[string]any, key string, def bool) bool {
	if v, ok := args[key]; !ok || v == nil {
		return def
	}
	return BoolArg(args, key)
}

// editImage runs the edit action: check the arguments, hand the caller's image
// references to the connector (which verifies and uploads them), and save the
// result the same way generation does.
func editImage(sess *ToolSession, args map[string]any, avail imageActions) (string, error) {
	p, err := planEdit(sess, args, avail)
	if err != nil {
		return "", err
	}
	result, err := EditImageWithBackend(sess, EditImageRequest{
		Backend:     p.backend,
		Prompt:      p.prompt,
		Images:      p.refs,
		Mask:        p.mask,
		RefineFaces: p.refineFaces,
	})
	if err != nil {
		return "", fmt.Errorf("image edit via %q failed: %w", p.backend, err)
	}
	// Source BEFORE result, so the two pictures the model is about to compare
	// arrive in the order the instruction claims.
	compared := queueSourceForComparison(sess, p.refs)
	out, serr := saveImageResult(sess, result, "edit", "edited "+strings.Join(p.refs, "+")+": "+truncate(p.prompt, 60), ImageFromEdited)
	if serr != nil {
		return out, serr
	}
	note := ""
	if compared {
		note = fidelityNote(p.refs[0])
	}
	return out + p.note + note, nil
}

// fidelityCheck shows the SOURCE alongside the result so the model can answer
// the one question about a render that looking can actually settle.
//
// showToModel already puts the output in front of it, and correctly refuses to
// let it judge WHO someone is: handed a found photo of a stranger, an agent
// that does not recognize the face has learned nothing, and treating that as a
// failure threw away correct results. Identity is not visible.
//
// RESEMBLANCE IS. With the source in hand the question stops being "is this
// the right person", which needs knowledge nobody has, and becomes "did the
// person survive the render" — two pictures, side by side, answerable from
// pixels alone. That is the failure an edit model actually has: the reference
// was passed, the backend used it, and the face still came out somebody else's.
//
// Only the FIRST reference, and only when there is a model to look with. The
// first is the base/subject by the ordering this tool already documents, so it
// is the one carrying the identity; showing all three would triple the cost of
// every edit to answer a question about one of them.
// queueSourceForComparison puts the edit's SOURCE image in front of the model,
// before the result is queued, and reports whether it did.
//
// Order is the whole point, and it used to be wrong. The result was queued
// first (inside saveImageResult) and the source second, while the text told
// the model the source was "above the result" — so a comparison meant to ask
// "did the likeness survive" was made with the two pictures swapped, and any
// decision about which one to deliver was inverted with them. Queuing the
// source first also reads the way a before/after comparison should.
func queueSourceForComparison(sess *ToolSession, refs []string) bool {
	if sess == nil || sess.LLM == nil || sess.Detached || len(refs) == 0 {
		return false
	}
	src, ok := ResolveRecentImage(sess, refs[0])
	if !ok || len(src) == 0 {
		// A workspace filename or a media id: resolvable elsewhere, not here.
		// No source to compare against means no check — silence rather than a
		// question the model cannot answer.
		return false
	}
	// Named, not merely first. Ordering alone carried this before, and ordering
	// alone is what got it backwards; a label survives another producer landing
	// between the source and the result.
	sess.AppendViewImageAs(src, "the SOURCE photo, BEFORE the edit ("+refs[0]+")")
	return true
}

// fidelityNote is the instruction that goes with the two queued pictures. It
// names the order explicitly because the model is being handed two images of
// the same subject and everything it is asked to judge depends on telling them
// apart.
func fidelityNote(ref string) string {
	return fmt.Sprintf(" COMPARE: two pictures are included with this result — FIRST the source you passed (%s), SECOND the edited result. "+
		"This is the one identity question looking CAN settle — not who the person is, but whether the person in the second picture is the SAME ONE as in the first. "+
		"If a face, animal or product came out visibly different, say so and try once more with the likeness named as the thing to preserve; "+
		"if it survived, deliver it and do not raise this again.", ref)
}

// buildEditPrompt produces the prompt the backend actually receives: the
// caller's text with named people rewritten to positions (prompt_scrub), plus
// the compositing guard. Split out so the assembly is testable without a
// configured backend — the guard reaching the wire is the whole point of it,
// and a test that skips when no backend is wired proves nothing.
func buildEditPrompt(sess *ToolSession, prompt string, refs []string) (string, string, error) {
	scrubbed, note, err := checkPromptSubjects(sess, prompt, refs)
	if err != nil {
		return "", "", err
	}
	// An EMPTY prompt stays empty. Two things depend on it: a backend that
	// requires a prompt validates by checking for one, so padding it with the
	// guard would let a promptless call through to render from the guard text
	// alone; and a blend backend has no text node at all, so there is nowhere
	// for the guard to go. Only a real prompt gets it.
	if strings.TrimSpace(scrubbed) == "" {
		return scrubbed, note, nil
	}
	return scrubbed + editCompositingGuard(), note, nil
}

// editCompositingGuard is appended to every edit prompt.
//
// The source pictures are handed to the backend as a plain list, and nothing in
// that list says which is the canvas and which is only a likeness to apply.
// The tool description tells the MODEL the convention (first is the base, later
// ones composite onto it) and prompt_scrub rewrites names into positional
// references, so the request itself is well formed — but the renderer is a
// different model, and given two pictures it will sometimes place one INSIDE
// the result rather than draw from it.
//
// Observed, repeatedly: a face swap that lands the right face on the character
// and then puts a thumbnail of the original face on its forehead, and results
// carrying the source picture inset in a corner. Both are the same mistake
// about what a reference IS, and both are cheap to argue out of in the prompt.
// Naming the specific artifacts beats a general "don't composite", because the
// failure is a literal reading of the input, not a stylistic choice.
func editCompositingGuard() string {
	return " Produce ONE finished image. The supplied pictures are references for likeness and content ONLY —" +
		" they must not appear as objects inside the result. No inset, thumbnail, corner overlay, watermark," +
		" picture-in-picture, side-by-side panel, collage, or duplicate of a face anywhere in the frame."
}

// hasImageSources reports whether the caller supplied anything to work FROM.
// This is the whole routing decision, and it is a fact about the arguments
// rather than a judgement about the request.
func hasImageSources(args map[string]any) bool {
	return len(stringsArg(args, "images")) > 0
}

// routeRender is the single render entry point.
//
// There used to be two actions, and choosing between them was the most
// persistent failure this tool had: "make x sit in y" reads as creation, so
// generate won the word-match and the photo the person had just sent took no
// part in the result. Three separate layers of prompt wording were added to
// argue the model out of that, which is the shape of a fix that is fighting its
// own API.
//
// So the choice is gone. The model asks for a picture; whether it hands over
// sources is a PARAMETER, and the framework routes on that. A parameter is a
// much easier thing for a model to get right than an action, because it is
// filling in what it has rather than predicting which door to walk through.
//
// "edit" still arrives — from older prompts, stored tools, habit — and lands
// here too. It is not an error, it is the same request.
func routeRender(sess *ToolSession, args map[string]any, avail imageActions) (string, error) {
	if hasImageSources(args) {
		return editImage(sess, args, avail)
	}
	return generateImage(sess, args, avail)
}
