package imagefetch

import (
	"fmt"
	_ "image/gif"
	_ "image/png"
	"strconv"
	"strings"
	"time"

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
		return p, fmt.Errorf("the edit action is unavailable: no image backend here is wired for image input (img2img / inpaint). Tell the user editing isn't set up; do NOT retry")
	}
	prompt := strings.TrimSpace(StringArg(args, "prompt"))
	refs := imageSources(args)
	if len(refs) == 0 || (strings.EqualFold(StringArg(args, "action"), "edit") && !editsPicture(args)) {
		hint := "its id"
		if m := RecentImageManifest(sess); m != "" {
			hint = m
		}
		return p, fmt.Errorf("edit changes one picture: name it as picture, and put pictures to take people or things from in references. "+
			"To make a NEW picture from reference photos, use generate with them as references. The pictures you can name: %s", hint)
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
		return p, fmt.Errorf("image backend %q creates from text and cannot work from a source picture, use one of: %s, or generate from the prompt alone", backend, strings.Join(editorNames(avail), ", "))
	}
	// Count mismatch, named in terms of what this deployment can do. Checked
	// here rather than left to the backend so the answer arrives before an
	// upload and names the alternatives: "wrong number of images" sends the
	// model guessing, "this can combine 2, you passed 3" does not. Too FEW is
	// no longer refused: the unused inputs are taken out of the graph (core
	// BuildComfyBody), so two pictures can go to a three-input backend.
	if n := backendImageCount(avail, backend); n > 0 && len(refs) > n {
		counts := editorImageCounts(avail)
		strs := make([]string, 0, len(counts))
		for _, c := range counts {
			strs = append(strs, strconv.Itoa(c))
		}
		fix := "Choose the %d that matter and pass those, or do it in more than one call."
		return p, fmt.Errorf("you passed %s, and this deployment composes %s at a time: %q takes exactly %d. "+fix,
			pluralPictures(len(refs)), joinCounts(editorImageCounts(avail)), backend, n, n)
	}
	// Some editing workflows have no text node at all — a blend or an upscale is
	// pure pixel work. Demanding a prompt there makes the model invent one that
	// goes nowhere.
	if prompt == "" && backendNeedsPrompt(avail, backend) {
		return p, fmt.Errorf("prompt is required for this backend: describe what should CHANGE (e.g. \"make it snowy\", \"put the subject on a beach\")")
	}
	// ENFORCEMENT, same as generate: the enum is a hint, this is the boundary.
	if !ImageBackendReachable(sess, backend) {
		return p, fmt.Errorf("image backend %q is not available to you, use one of: %s", backend, strings.Join(editorNames(avail), ", "))
	}
	// What each picture is for, said to a model that reads plain language
	// (framePictures). Not to a graph, whose prompt is a diffusion prompt.
	if !backendIsGraph(avail, backend) {
		prompt = framePictures(prompt, editsPicture(args), len(refs))
	}
	return editPlan{
		backend: backend, prompt: prompt, refs: refs,
		mask: StringArg(args, "mask"),
		// Gated on the BACKEND, not just the argument: the pass needs a second
		// input slot for the identity reference, and asking for it where there
		// is none would spend a whole extra render sharpening a stranger.
		refineFaces: backendRefinesFaces(avail, backend) && boolArgDefault(args, "preserve_faces", true),
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
	// How much this edit is checked, and how many pictures its sources may
	// give before it gives up (image_attempts.go).
	level := editChecking(args)
	limit := ImageCheckAttempts(level)
	tries := attemptKey(sess, p.refs)
	if made := attemptsMade(tries, time.Now()); made >= limit {
		return "", attemptsRefusal(made, limit, level)
	}
	// And the chain this pass is part of, counted as one (image_chain.go).
	if err := chainBegin(sess, p.refs, time.Now()); err != nil {
		return "", err
	}
	// Quick has no face pass, unless the call asks for one by name.
	if level == ImageCheckQuick {
		if _, asked := args["preserve_faces"]; !asked {
			p.refineFaces = false
		}
	}
	if p.refineFaces {
		editProgress(sess, "rendering, with a pass over the faces")
	} else {
		editProgress(sess, "rendering")
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
	made := recordAttempt(tries, time.Now())
	last := tries != "" && made >= limit
	// A one-photo edit: the source BEFORE the result, so the two pictures the
	// model is about to compare arrive in the order the instruction claims.
	// A step from several photos is shown as one contact sheet instead, made
	// below once the result is in hand.
	shown := queueSourceForComparison(sess, p.refs)
	sources := checkSources(sess, p.refs)
	queued := 0
	if sess != nil {
		queued = len(sess.PendingViewImages)
	}
	out, saved, serr := saveImageResultRef(sess, result, "edit", "edited "+strings.Join(p.refs, "+")+": "+truncate(p.prompt, 60), ImageFromEdited, holdsResult(args))
	if serr != nil {
		return out, serr
	}
	// The result as rendered, read before a contact sheet takes its place.
	var rendered []byte
	if sess != nil && len(sess.PendingViewImages) == queued+1 {
		rendered = sess.PendingViewImages[queued].Data
	}
	note := ""
	switch {
	case len(shown) == 1:
		note = fidelityNote(shown[0])
	case len(p.refs) > 1 && len(sources) > 0:
		if refs, ok := sheetInPlaceOfResult(sess, queued, sources); ok {
			note = blendNote(refs, holdsResult(args))
		}
	}
	// And the check that does not rest on the agent's own look, for a
	// one-photo edit as for a blend: the agent's look at a source beside its
	// result is the look that reported a lost ship as present. Not on Quick:
	// that is the render and the agent's look, and no more.
	checked, failed := false, false
	if level != ImageCheckQuick && len(sources) > 0 {
		editProgress(sess, "checking the result against the sources")
		verdicts := checkSourcesInResult(sess, sources, rendered, p.prompt, level == ImageCheckThorough)
		checked = len(verdicts) > 0
		for _, v := range verdicts {
			failed = failed || !v.found
		}
		note += sourceCheckNote(verdicts, len(sources), holdsResult(args), last)
	}
	// And whether it changed anything at all, measured, for an edit of a
	// picture (a new one from references has no "before").
	if editsPicture(args) && len(sources) > 0 && sources[0].ref == p.refs[0] && len(rendered) > 0 {
		if most, ok := largestChange(sources[0].data, rendered); ok && most < unchangedBelow {
			Log("[imagefetch] edit of %s changed nothing visible (largest change %.1f%%)", sources[0].ref, most*100)
			note += unchangedNote(most)
			checked, failed = true, true
		}
	}
	if base := chainRecord(sess, checked, failed, saved, time.Now()); failed {
		note += chainRetryNote(base)
	}
	if last {
		note += lastAttemptNote(made, limit)
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
// A one-photo edit shows its source beside the result: the first reference is
// the subject, and the question is whether that subject survived the render.
//
// A step that takes SEVERAL photos (a blend, a composite, a face from one onto
// another) is shown differently: every source and the result on one contact
// sheet (checkSources, sheetInPlaceOfResult). Each source carries something
// the result has to contain, and the failure a blend has is one of them going
// missing: swapped, merged into another, or dropped. As separate pictures they
// ran into the model's per-request picture limit, and the older ones arrived as
// a paragraph somebody else wrote, which no likeness can be checked against.
//
// Only when there is a model to look with. A detached render counts: its
// result is shown on the round that reports it (saveImageResult), so its
// source is too, or that round compares an AFTER with no BEFORE.
//
// queueSourceForComparison puts a one-photo edit's SOURCE in front of the
// model, before the result is queued, and returns the reference it showed.
//
// Order is the whole point, and it used to be wrong. The result was queued
// first (inside saveImageResult) and the source second, while the text told
// the model the source was "above the result" — so a comparison meant to ask
// "did the likeness survive" was made with the two pictures swapped, and any
// decision about which one to deliver was inverted with them.
func queueSourceForComparison(sess *ToolSession, refs []string) []string {
	if sess == nil || sess.LLM == nil || len(refs) != 1 {
		return nil
	}
	src, ok := sourceData(sess, refs[0])
	if !ok {
		// No source to compare against means no question about it.
		return nil
	}
	// Named, not merely ordered. Ordering alone carried this before, and
	// ordering alone is what got it backwards; a label survives another
	// producer landing between the source and the result.
	sess.AppendViewImageAs(src, "the SOURCE photo, BEFORE the edit ("+refs[0]+")")
	return refs[:1]
}

// sourceImage is one source picture of a step, by the reference it was passed as.
type sourceImage struct {
	ref  string
	data []byte
}

// checkSources resolves every source of an edit, in the order passed, for the
// source check and (for several) the contact sheet. Nil with no model to look
// with. Read as the edit read them (sourceData): a media id or a workspace
// file used to be left off here, and the picture being edited is usually one,
// so the check judged a face swap that dropped a person and cut the frame
// with nothing to compare the scene to, and passed it.
func checkSources(sess *ToolSession, refs []string) []sourceImage {
	if sess == nil || sess.LLM == nil || len(refs) == 0 {
		return nil
	}
	var out []sourceImage
	for _, ref := range refs {
		if data, ok := sourceData(sess, ref); ok {
			out = append(out, sourceImage{ref: ref, data: data})
		}
	}
	return out
}

// sourceData is the picture ref names, read exactly as the edit reads its
// sources (core.ResolveSourceImage): a picture from this conversation
// (image#...), one someone sent (media#N), or a workspace path.
func sourceData(sess *ToolSession, ref string) ([]byte, bool) {
	if data, ok := ResolveRecentImage(sess, ref); ok && len(data) > 0 {
		return data, true
	}
	data, _, err := ResolveSourceImage(sess, ref)
	return data, err == nil && len(data) > 0
}

// sheetInPlaceOfResult swaps the result picture saveImageResult just queued for
// a contact sheet of the sources and that result, and returns the sources it
// shows. queued is how many view pictures were pending before the result: the
// swap is made only when the result is the one picture added since, so it
// never replaces something another producer queued. Not built, the result
// stays as it was queued and the step reads as an ordinary render.
func sheetInPlaceOfResult(sess *ToolSession, queued int, sources []sourceImage) ([]string, bool) {
	if sess == nil || len(sources) == 0 || len(sess.PendingViewImages) != queued+1 {
		return nil, false
	}
	pictures := make([][]byte, 0, len(sources)+1)
	labels := make([]string, 0, len(sources)+1)
	refs := make([]string, 0, len(sources))
	for i, src := range sources {
		pictures = append(pictures, src.data)
		labels = append(labels, fmt.Sprint(i+1))
		refs = append(refs, src.ref)
	}
	pictures = append(pictures, sess.PendingViewImages[queued].Data)
	labels = append(labels, "RESULT")
	sheet, err := contactSheet(pictures, labels)
	if err != nil {
		Debug("[imagefetch] contact sheet not built, showing the result alone: %v", err)
		return nil, false
	}
	sess.PendingViewImages[queued] = ViewImage{
		Data:  sheet,
		Label: fmt.Sprintf("a contact sheet: sources 1 to %d, then the RESULT", len(sources)),
	}
	return refs, true
}

// blendNote is the instruction for a step made from several photos, shown as
// one contact sheet. held says this step is a pass toward a later one, where a
// wrong step is worse than a wrong picture: the next pass inherits it.
func blendNote(refs []string, held bool) string {
	next := "deliver it"
	stop := ""
	if held {
		next = "go on to the next pass"
		stop = " Do NOT build the next pass on a step that lost a subject: the next pass cannot put it back."
	}
	order := make([]string, len(refs))
	for i, r := range refs {
		order[i] = fmt.Sprintf("%d = %s", i+1, r)
	}
	return fmt.Sprintf(" COMPARE: the picture included with this result is ONE contact sheet, left to right and row by row, each tile labelled at its top: "+
		"the sources %s, then the tile labelled RESULT (its label band is blue). "+
		"Check each numbered source against RESULT: is that source's subject in it, and the SAME ONE (not swapped with another, merged into one, duplicated, or left out)? "+
		"This is a question looking CAN settle: not who anyone is, but whether each subject in the sources survived into the result. "+
		"If one is missing or came out as somebody or something else, say which number, plainly, and offer to try again: do NOT try again on your own, the person can see it.%s "+
		"If every one survived, %s and do not raise this again.", strings.Join(order, ", "), stop, next)
}

// fidelityNote is the instruction that goes with the two queued pictures. It
// names the order explicitly because the model is being handed two images of
// the same subject and everything it is asked to judge depends on telling them
// apart.
func fidelityNote(ref string) string {
	return fmt.Sprintf(" COMPARE: two pictures are included with this result, FIRST the source you passed (%s), SECOND the edited result. "+
		"This is the one identity question looking CAN settle: not who the person is, but whether the person in the second picture is the SAME ONE as in the first. "+
		"If a face, animal or product came out visibly different, say so plainly and offer to try again, do NOT try again on your own: the person can see it; "+
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
	return " Produce ONE finished image. The supplied pictures are references for likeness and content ONLY" +
		" they must not appear as objects inside the result. No inset, thumbnail, corner overlay, watermark," +
		" picture-in-picture, side-by-side panel, collage, or duplicate of a face anywhere in the frame."
}

// hasImageSources reports whether the caller supplied anything to work FROM.
// This is the whole routing decision, and it is a fact about the arguments
// rather than a judgement about the request.
func hasImageSources(args map[string]any) bool {
	return len(imageSources(args)) > 0
}

// rendersFromPictures reports a render that goes through the edit path: one
// given pictures, or an edit, which changes a picture even when the call
// forgot to name it (and is told to).
func rendersFromPictures(args map[string]any) bool {
	return hasImageSources(args) || strings.EqualFold(strings.TrimSpace(StringArg(args, "action")), "edit")
}

// imageSources are the pictures an image call names, in the order the backend
// gets them: the picture to change first (picture), then the pictures to take
// people or things from (references). images is the older single list, still
// accepted from calls that send it; its first picture was the one to change.
func imageSources(args map[string]any) []string {
	var out []string
	if p := strings.TrimSpace(StringArg(args, "picture")); p != "" {
		out = append(out, p)
	}
	out = append(out, stringsArg(args, "references")...)
	if len(out) == 0 {
		out = stringsArg(args, "images")
	}
	return out
}

// editsPicture reports whether the call changes a picture that exists, the
// first of its sources, rather than making a new one from references.
func editsPicture(args map[string]any) bool {
	if strings.TrimSpace(StringArg(args, "picture")) != "" {
		return true
	}
	return len(stringsArg(args, "references")) == 0 && len(stringsArg(args, "images")) > 0
}

// framePictures says what each picture is for, ahead of what the call asked:
// the picture being changed and what must stay in it, or the references a new
// picture is made from. Two asks a model given a list of pictures otherwise
// has to tell apart itself: given a scene and a portrait for a face swap, it
// came back in the portrait's shape with a person gone.
func framePictures(prompt string, edits bool, n int) string {
	// The compositing guard says every picture is only a reference, which is
	// wrong for the photo being edited: that one IS the result. Said here by
	// role instead (framedGuard).
	guarded := strings.HasSuffix(prompt, editCompositingGuard())
	prompt = strings.TrimSuffix(prompt, editCompositingGuard())
	var b strings.Builder
	switch {
	case edits && n > 1:
		b.WriteString("Picture 1 is the photo to edit. Change only what is asked below, and keep everything else in it as it is: its framing, its shape, and everyone and everything the change does not name. ")
		// One reference read "Pictures 2 to 2 are references".
		if n == 2 {
			b.WriteString("Picture 2 is a reference: take from it only what the change asks for, such as a face.")
		} else {
			fmt.Fprintf(&b, "Pictures 2 to %d are references: take from each only what the change asks for, such as a face.", n)
		}
	case edits:
		b.WriteString("The picture is the photo to edit. Change only what is asked below, and keep everything else in it as it is: its framing, its shape, and everyone and everything the change does not name.")
	default:
		b.WriteString("Make a new picture from the request below. The pictures are references: take from each what the request asks for.")
	}
	label := "The change: "
	if !edits {
		label = "The request: "
	}
	b.WriteString("\n\n" + label + strings.TrimSpace(prompt))
	if guarded {
		b.WriteString(framedGuard(edits, n))
	}
	return b.String()
}

// framedGuard is the compositing guard said by role: the photo being edited
// is the result, and only the references must stay out of the frame.
func framedGuard(edits bool, n int) string {
	const artifacts = " No inset, thumbnail, corner overlay, watermark, picture-in-picture, side-by-side panel, collage, or duplicate of a face anywhere in the frame."
	switch {
	case edits && n > 1:
		return "\n\nProduce ONE finished image: the photo to edit, changed. The references must not appear as objects inside it." + artifacts
	case edits:
		return "\n\nProduce ONE finished image: the photo, changed." + artifacts
	}
	return "\n\nProduce ONE finished image. The references are for likeness and content only: they must not appear as objects inside the result." + artifacts
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
	if rendersFromPictures(args) {
		return editImage(sess, args, avail)
	}
	return generateImage(sess, args, avail)
}
