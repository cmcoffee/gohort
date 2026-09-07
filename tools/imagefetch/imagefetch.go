// Package imagefetch provides tools for fetching, finding, and generating images
// and collecting them for delivery (e.g. as iMessage attachments).
package imagefetch

import (
	"fmt"
	_ "image/gif"
	_ "image/png"
	"slices"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	_ "golang.org/x/image/webp" // register the WebP decoder for image.Decode
)

func init() {
	RegisterChatTool(new(ImageTool))
	RegisterChatTool(new(FetchImageTool))
	RegisterChatTool(new(FindImageTool))
	RegisterChatTool(new(GenerateImageTool))
}

// --- ImageTool (grouped) ---
//
// Single entry point for image work — find | fetch | generate — picked by
// `action`, mirroring the `video` grouped tool. Collapses three
// near-identical schemas into one. The standalone find_image / fetch_image
// / generate_image stay registered (phantom + explicit allowlists use
// them); orchestrate drops them from its default pool in favor of this
// (see supersededWorkerTools). The handler just delegates to the existing
// per-action tools, so behavior is identical.
//
// The schema is DYNAMIC (core.DynamicChatTool): only the actions whose backing
// config exists are advertised. `find` without a serper key and `generate`
// without a provider used to be offered anyway and refused at call time, which
// the model couldn't predict and would retry.

type ImageTool struct{}

// The optional interfaces the framework asks for by assertion, pinned here so
// a signature drift is a build error rather than a silently skipped check —
// losing Preflight would put every bad argument back in the background.
var (
	_ DetachableTool     = (*ImageTool)(nil)
	_ EstimatingTool     = (*ImageTool)(nil)
	_ PreflightTool      = (*ImageTool)(nil)
	_ SeriesTool         = (*ImageTool)(nil)
	_ AlwaysDetachTool   = (*ImageTool)(nil)
	_ DetachIdentityTool = (*ImageTool)(nil)
	_ SupersedingTool    = (*ImageTool)(nil)
)

// SeriesCapable: a render books itself against the set and asks for the next
// one when pieces remain. See noteSeriesPiece.
func (t *ImageTool) SeriesCapable() bool { return true }

// DetachIdentity: `image` and every connector's generate_image_<name> tool are
// the same act under different names, so they share one slot and one set.
func (t *ImageTool) DetachIdentity() string { return RenderDetachIdentity }

// Supersedes: everything this tool absorbed. The standalone trio it replaced,
// and every connector's generate_image_<name> — `image` reaches all of those
// backends through its `backend` param, so a caller holding both sees one tool
// instead of a grouped one plus a twin per backend.
//
// Declared here rather than listed in the app because this is the tool that
// knows what it covers, and a connector materializing a new backend tool must
// not require anybody to remember to update a list somewhere else.
func (t *ImageTool) Supersedes(name string) bool {
	switch name {
	case "find_image", "fetch_image", "generate_image":
		return true
	}
	return strings.HasPrefix(name, RestImageToolPrefix)
}

// AlwaysDetach sends renders to the background whatever they are expected to
// cost. A twenty-second generate is fast and still holds the conversation shut
// for twenty seconds; four of them hold it for eighty, and the reply the user
// gets at the end is one message carrying a batch they waited in silence for.
// Detached, each picture arrives as it is ready and the agent can talk in
// between. find and fetch are ordinary HTTP and keep waiting.
//
// Off by knob, not by code: a deployment that prefers the wait sets
// tune_image_always_detach to 0 and gets the duration rule back.
func (t *ImageTool) AlwaysDetach(args map[string]any, sess *ToolSession) bool {
	switch effectiveImageAction(args) {
	case "generate", "edit":
		return TuneBool("tune_image_always_detach")
	}
	return false
}

func (t *ImageTool) Name() string         { return "image" }
func (t *ImageTool) Caps() []Capability   { return []Capability{CapNetwork, CapRead} }
func (t *ImageTool) IsInternetTool() bool { return true }

// allImageActions is the full shape, used by the STATIC Desc/Params that the
// semantic tool index and the session-less picker surfaces read. The catalog an
// LLM sees comes from SchemaWithSession instead.
var allImageActions = imageActions{find: true, fetch: true, generate: true}

func (t *ImageTool) Desc() string {
	return imageSchemaFor(allImageActions).desc
}
func (t *ImageTool) Params() map[string]ToolParam {
	return imageSchemaFor(allImageActions).params
}

// SchemaWithSession narrows the advertised actions to the ones that will
// actually run. See imageSchemaFor for why.
func (t *ImageTool) SchemaWithSession(sess *ToolSession) (string, map[string]ToolParam) {
	s := imageSchemaFor(liveImageActions(sess))
	return s.desc, s.params
}

// ExpectedDuration reports how long THIS call is likely to take, so the
// framework can decide whether to detach it. The number is not a guess: an
// image backend already carries its own render deadline, and an edit backend's
// is an order of magnitude larger than a generate's because the model has to
// load first. find and fetch are ordinary HTTP and stay inline.
func (t *ImageTool) ExpectedDuration(args map[string]any, sess *ToolSession) time.Duration {
	switch effectiveImageAction(args) {
	case "generate", "edit":
		return ImageBackendDeadline(sess, strings.TrimSpace(StringArg(args, "backend")))
	}
	return 0
}

// Preflight checks everything about a render EXCEPT the render: that the
// backend can do what was asked, that a prompt is there if the graph needs one,
// and — the one that keeps biting — that every source photo the caller named
// actually resolves to image bytes right now.
//
// The failure this exists for: an agent finds two pictures, calls edit naming
// them by ids it made up, and gets back "started, will report back" because
// nothing had looked at the references yet. It tells the user the blend is
// running. Forty-six seconds later the background job discovers the ids resolve
// to nothing, and the agent has to explain a failure whose cause is no longer
// in front of it — so it guesses, and its guess reaches the user as fact.
//
// Checked here, the same mistake is a plain tool error in the same round, with
// the manifest of real handles attached, and the agent simply calls it again
// correctly. See core.PreflightTool.
func (t *ImageTool) Preflight(args map[string]any, sess *ToolSession) error {
	avail := liveImageActions(sess)
	switch effectiveImageAction(args) {
	case "generate", "edit":
		// One path, chosen by whether sources were passed rather than by which
		// word the model picked. See routeRender.
		if !hasImageSources(args) {
			_, err := planGenerate(sess, args, avail)
			return err
		}
		p, err := planEdit(sess, args, avail)
		if err != nil {
			return err
		}
		return CheckImageInputs(sess, p.backend, p.refs, p.mask)
	}
	return nil
}

// TypicalDuration is what this backend has actually been taking, which is a
// different question from ExpectedDuration and has a different answer. That one
// reports the DEADLINE — how long before the framework gives up — because
// deciding whether a call can hold a turn open has to assume the worst. This
// one is measured, and is the only number the agent is allowed to quote: a
// render that finishes in forty seconds should not be announced as fifteen
// minutes because fifteen minutes is when we would have stopped waiting.
//
// Zero until the backend has been measured, which the notice renders as saying
// nothing about the time rather than guessing.
func (t *ImageTool) TypicalDuration(args map[string]any, sess *ToolSession) time.Duration {
	switch effectiveImageAction(args) {
	case "generate", "edit":
		return ImageBackendTypicalDuration(sess, strings.TrimSpace(StringArg(args, "backend")))
	}
	return 0
}

func (t *ImageTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("image requires a session context — use GetAgentToolsWithSession")
}
func (t *ImageTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	// The action set is re-read here, not trusted from the schema: a model can
	// name an action that wasn't in its enum (stale context, a copied call), and
	// an unconfigured action must say WHY rather than fall through to a generic
	// "unknown action".
	avail := liveImageActions(sess)
	action := strings.ToLower(strings.TrimSpace(StringArg(args, "action")))
	// No action, but arguments that only one action takes. That call is a
	// MISFIRE, and answering it with the usage spec is the worst thing this
	// tool can do: help comes back as a SUCCESS, opening with "each saves to
	// your workspace and returns the path" and followed by the manifest of
	// every picture already made — so a model that skims it concludes it just
	// rendered several, and goes looking in the workspace for them.
	//
	// Observed end to end: image(prompt="...armada of generational ships") with
	// no action returned help, the agent announced "here are 4 variations",
	// invented a filename, failed to attach it, listed the workspace, and
	// delivered two unrelated pictures from an earlier session as the set. Not
	// one image was rendered for that request.
	//
	// The arguments already say what was meant — routeRender picks generate vs
	// edit the same way, from whether sources were passed rather than from
	// which word the model chose — so infer it and run the call. See
	// core.GroupedTool, which returns a directive error for the same misfire;
	// it cannot infer because its actions share params, and here they do not.
	if action == "" {
		inferred, why := inferImageAction(args)
		if inferred == "" {
			if why != "" {
				return "", fmt.Errorf("image was called with no \"action\" but with %s — nothing was done, and nothing was rendered. Re-call with action=\"<one>\": %s", why, strings.Join(avail.names(), " | "))
			}
		} else {
			Log("[imagefetch] image called with no action; inferred %q from its arguments", inferred)
			action = inferred
		}
	}
	switch action {
	case "find":
		if !avail.find {
			return "", fmt.Errorf("the find action is unavailable — image search needs the serper provider with an API key configured. Use fetch with a direct image URL, or ask the user to configure search")
		}
		return (&FindImageTool{}).RunWithSession(args, sess)
	case "fetch":
		return (&FetchImageTool{}).RunWithSession(args, sess)
	case "generate", "edit":
		// The ceiling, checked BEFORE the render so a refusal costs no time.
		if err := checkRenderBudget(sess); err != nil {
			return "", err
		}
		out, err := routeRender(sess, args, avail)
		if err != nil {
			// A failed piece ends the set. The wake for a failure already tells
			// the model not to retry silently, and a chain that carries on after
			// one break keeps sending pictures for a request that visibly went
			// wrong.
			if sess != nil && sess.Detached {
				CloseTaskSeries(sess.DeliverySession(), RenderDetachIdentity)
			}
			return out, err
		}
		return t.noteSeriesPiece(sess, args, out), nil
	case "keep":
		name := StringArg(args, "name")
		ref := strings.TrimSpace(StringArg(args, "ref"))
		if ref == "" {
			ref = RecentImageRefPrefix + "1" // "keep what I just made" is the common case
		}
		subject := ResolveKeepSubject(sess, StringArg(args, "of"), BoolArg(args, "is_person"))
		kept, err := KeepImageOf(sess, ref, name, StringArg(args, "note"), subject)
		if err != nil {
			return "", err
		}
		out := fmt.Sprintf("Kept as %s. That name keeps working from now on — pass it anywhere an image id goes, in this conversation or a later one.", kept.Ref)
		if kept.Caption != "" {
			out += "\nWhat it shows: " + kept.Caption
		}
		// Say it is recallable. Otherwise the model has no way to know it can
		// find this again later without having kept a note of the name itself.
		if kept.Subject.Named() {
			if kept.Subject.Person {
				out += "\nFiled as the picture of " + SubjectLabel(kept.Subject) + "."
				if strings.TrimSpace(kept.Subject.Handle) != "" {
					// Say the identification is anchored. The distinction is
					// the whole point of the field: matched to a handle it is
					// an identification, unmatched it is a label.
					out += " Matched to the person you are talking to, so a request naming them resolves to this picture."
				} else {
					out += " Matched by name only — nobody with that name has messaged in, so this is a label rather than a confirmed identification."
				}
				// Say which of the two rules applies. Promising "replaces it"
				// unconditionally was wrong once supersession required an
				// anchored claim, and a model told its keep replaced something
				// it did not would report a library it does not have.
				if strings.TrimSpace(kept.Subject.Handle) != "" {
					out += " This is now THE picture of them: another anchored picture of the same person replaces it."
				} else {
					out += " Because this is a label rather than an identification, it does NOT retire an existing picture of someone by that name — both are kept, and listed as duplicates."
				}
			} else {
				out += "\nFiled as a picture of " + SubjectLabel(kept.Subject) + "."
			}
		}
		out += "\nA detailed description went to your memory alongside it, so a later question can find this picture — and work from what it looks like — without you remembering the name or looking at it again."
		// Say what keeping your own output does and does not buy. Silence here
		// reads as "this is now a reference", which is the belief that had
		// invented subjects standing in for real ones.
		if kept.Origin.AgentMade() {
			out += fmt.Sprintf("\nNote: you MADE this picture (%s), so it is kept but NOT treated as a reference — it is not evidence of what any real thing looks like, and it won't be offered as one. Reference images are the ones you were given or found.", kept.Origin)
		}
		return out + fmt.Sprintf("\nNOT delivered — keeping only files it away. To send it, call workspace(action=\"attach\", path=%q); the kept copy stays where it is. Do NOT re-render it to make it sendable — that produces a different picture.", kept.Ref), nil
	case "label":
		name := StringArg(args, "name")
		of := strings.TrimSpace(StringArg(args, "of"))
		if of == "" {
			return "", fmt.Errorf("label needs \"of\" — who or what the picture shows. To clear a label instead, keep the image again under the same name")
		}
		kept, conflict, err := LabelKeptImage(sess, name, ResolveKeepSubject(sess, of, BoolArg(args, "is_person")))
		if err != nil {
			return "", err
		}
		out := fmt.Sprintf("%s is now filed as a picture of %s.", kept.Ref, SubjectLabel(kept.Subject))
		if kept.Subject.Person {
			if strings.TrimSpace(kept.Subject.Handle) != "" {
				out += " Matched to the person you are talking to, so a request naming them resolves to this picture."
			} else {
				out += " Matched by name only — nobody with that name has messaged in, so this is a label rather than a confirmed identification."
			}
		}
		if conflict != "" {
			// Reported, not resolved. Labelling supplies no replacement, and
			// deleting a real photograph as a side effect of adding a word to
			// a different one is not something to do quietly.
			out += fmt.Sprintf(" NOTE: %s is also filed as that subject. Nothing was deleted — decide which one is right and forget the other, or a request naming them has two answers.", conflict)
		}
		if kept.Origin == ImageOriginUnknown {
			out += " Its origin is still unrecorded — labelling says WHO it shows, not where it came from, so do not start calling it a photograph on the strength of this."
		}
		return out, nil
	case "forget":
		name := StringArg(args, "name")
		gone, err := ForgetImage(sess, name)
		if err != nil {
			return "", err
		}
		if !gone {
			return fmt.Sprintf("Nothing kept under %q — nothing was deleted. Call action=\"help\" to see what you have kept.", name), nil
		}
		return fmt.Sprintf("Forgot %q. Its id no longer resolves.", name), nil
	case "", "help":
		// Say what this is NOT, first. The spec used to open with "each saves
		// to your workspace and returns the path" above a manifest of every
		// picture already made, which is indistinguishable from a result to
		// anything reading quickly — and what followed was an agent attaching
		// old pictures as the ones it had just been asked for.
		help := "NOTHING WAS RENDERED, FOUND OR FETCHED — this is the usage spec for the image tool, not a result. No picture was made by this call.\n\n" +
			"image actions: " + strings.Join(avail.names(), " | ") + ". Each saves to your workspace and returns the path; deliver with workspace(action=\"attach\", path=...)."
		if m := RecentImageManifest(sess); m != "" {
			help += "\n\n" + m
		}
		if m := KeptImageManifest(sess); m != "" {
			help += "\n\n" + m
		}
		return help, nil
	default:
		return "", fmt.Errorf("unknown action %q for image — use %s", StringArg(args, "action"), strings.Join(avail.names(), " | "))
	}
}

// effectiveImageAction is the action this call will actually run: the one it
// named, or the one its arguments imply.
//
// Every path that inspects the action BEFORE the call — the detach decision,
// the duration estimate, preflight — has to read it exactly the way
// RunWithSession does. Read only the literal argument, a call whose action was
// inferred is judged as if it had none: no estimate, so no detach, so a render
// the framework thinks is a no-op holds the turn open and skips its preflight
// on the way.
func effectiveImageAction(args map[string]any) string {
	if a := strings.ToLower(strings.TrimSpace(StringArg(args, "action"))); a != "" {
		return a
	}
	inferred, _ := inferImageAction(args)
	return inferred
}

// imageArgOwner maps an argument to the ONE action that takes it. Only the
// unambiguous ones are listed: name/ref/of and friends are shared by keep,
// label and forget, and guessing between three destructive-ish verbs is worse
// than saying the action is missing.
var imageArgOwner = map[string]string{
	"prompt":     "generate", // generate or edit — routeRender picks, from the sources
	"images":     "generate",
	"variations": "generate",
	"query":      "find",
	"url":        "fetch",
}

// inferImageAction reads the intended action off the arguments. Returns the
// action when exactly one is implied; otherwise an empty action and, when
// operation params WERE passed, a description of them for the error.
//
// Ambiguity is not resolved here. A call carrying both a prompt and a url has
// two readings and picking one silently is how the wrong thing gets delivered
// with no sign anything was guessed.
func inferImageAction(args map[string]any) (action, why string) {
	var seen []string
	var named []string
	for key, owner := range imageArgOwner {
		if !hasImageArg(args, key) {
			continue
		}
		named = append(named, key)
		if !slices.Contains(seen, owner) {
			seen = append(seen, owner)
		}
	}
	sort.Strings(named)
	if len(seen) == 1 {
		return seen[0], ""
	}
	// Keep/label/forget args count toward "something was asked for", so a call
	// with only those still gets the error rather than the manual.
	for _, key := range []string{"name", "ref", "note", "of", "is_person", "mask", "backend", "preserve_faces"} {
		if hasImageArg(args, key) {
			named = append(named, key)
		}
	}
	if len(named) == 0 {
		return "", "" // a bare call is a genuine probe — the manual is the right answer
	}
	sort.Strings(named)
	return "", strings.Join(named, ", ")
}

// hasImageArg reports whether an argument was supplied with a usable value.
// Presence alone is not enough: a model that fills every field of the schema
// sends prompt:"" on a fetch, and an empty string must not decide the action.
func hasImageArg(args map[string]any, key string) bool {
	v, ok := args[key]
	if !ok || v == nil {
		return false
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t) != ""
	case []any:
		return len(t) > 0
	case []string:
		return len(t) > 0
	case bool:
		return t
	case float64:
		return t != 0
	case int:
		return t != 0
	}
	return true
}

// checkRenderBudget stops a turn that has stopped counting.
//
// A detached call is already rationed — one per tool per turn by the detach
// ledger — so this is the INLINE ceiling, and inline is where it was missing:
// eighteen renders in one turn, seven minutes, five of them delivered.
//
// It refuses BEFORE the render, and it refuses with an instruction to deliver
// rather than a bare limit. A model told only "no" looks for another route; a
// model told "you have unattached pictures, send those" does the thing that was
// actually wanted.
func checkRenderBudget(sess *ToolSession) error {
	if sess == nil || sess.Detached {
		return nil
	}
	_, total := sess.NextImageAttempt(false)
	cap := ImageGenHardCap()
	if total <= cap {
		return nil
	}
	return fmt.Errorf("no picture was made — you have already rendered %d in this turn, which is the ceiling. "+
		"Stop rendering and finish the request with what you have: attach every picture you have not delivered yet (workspace attach, one call per file), then write your reply. "+
		"Do NOT call image again this turn, and do NOT tell the user a number you have not actually attached", cap)
}

// inlineSetNote is what a render says when the turn is still here and the model
// said it was making several.
//
// It replaced a single sentence that did enormous damage: "call image again now
// for the next one". That line counted nothing, so it appeared identically
// after every render and never once said stop; and it named the next render as
// the thing to do next, displacing the attach the result had just asked for. An
// agent followed it eighteen times, delivered five pictures, and reported
// eighteen.
//
// So this one counts (the per-turn render counter, which is turn-scoped and
// cannot leak into the next conversation the way a declared count can), always
// puts the attach FIRST, and terminates — explicitly, by name, at the number
// the model itself asked for.
func inlineSetNote(sess *ToolSession, want int) string {
	if want < 2 {
		return ""
	}
	made := sess.ImageRenderCount()
	if made < want {
		return fmt.Sprintf("\n\nThat is picture %d of the %d you said you would make. Attach THIS one now — deliver them as you go rather than saving them all for the end, which is how a set gets announced and never sent — then call image again for the next, varying the idea rather than repeating it.", made, want)
	}
	return fmt.Sprintf("\n\nThat is the LAST of the %d you said you would make — the set is COMPLETE. Attach this one, make sure every picture in the set has actually been attached, and write your reply. Do NOT render another for this request, and do not name a count you have not attached.", want)
}

// noteSeriesPiece books a finished render against a declared set and, when
// pieces remain, leaves the instruction that starts the next one.
//
// It runs AFTER the render rather than before it, because a set only advances
// on work that actually happened: a piece counted at the start and then failed
// would move the count without producing anything, and the last picture would
// silently never be made.
//
// The detached and inline paths differ because the problem does. Detached,
// there is no round left to make the next one in, so the count has to survive to
// the wake. Inline, the turn is still here and calling again costs nothing —
// there is no series to keep, and saying so is what stops the model waiting for
// a wake that is never coming.
func (t *ImageTool) noteSeriesPiece(sess *ToolSession, args map[string]any, out string) string {
	if sess == nil {
		return out
	}
	want := IntArg(args, "variations")
	if !sess.Detached {
		// A set that started detached and is now finishing its pieces inline —
		// a faster backend, a raised threshold — has nothing left to carry, and
		// leaving the count open would renumber whatever renders next.
		if TaskSeriesOpen(sess.DeliverySession(), RenderDetachIdentity) {
			CloseTaskSeries(sess.DeliverySession(), RenderDetachIdentity)
		}
		return out + inlineSetNote(sess, want)
	}
	prompt := strings.TrimSpace(StringArg(args, "prompt"))
	piece, of := BookSeriesPiece(sess, RenderDetachIdentity, want, "another take on: "+truncate(prompt, 60))
	if of <= 1 {
		return out
	}
	// Said in the RESULT as well, where it becomes part of what the thread
	// keeps. The continuation is one-shot and disappears after the turn that
	// acts on it; this is what a later turn reads to know the set was a set —
	// without it, "here is a picture" three times over has nothing tying the
	// three together, and the fourth request lands with no idea one was running.
	return out + fmt.Sprintf("\n\nThis is picture %d of the %d you said you would make.", piece, of)
}
