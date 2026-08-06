// Package imagefetch provides tools for fetching, finding, and generating images
// and collecting them for delivery (e.g. as iMessage attachments).
package imagefetch

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/browser"
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
	_ DetachableTool = (*ImageTool)(nil)
	_ EstimatingTool = (*ImageTool)(nil)
	_ PreflightTool  = (*ImageTool)(nil)
)

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

// imageActions is which of the grouped tool's actions can actually run right
// now. Split out as plain data so the availability RULES are testable without a
// configured search provider or image backend.
type imageActions struct {
	find     bool // needs the serper search provider + key
	fetch    bool // needs nothing — a plain HTTP download
	generate bool // needs an image-generation provider (built-in or a rest_image connector)
	edit     bool // needs a backend wired for image INPUT (img2img / inpaint / compose)
	// backends are the generation backends this caller may pick between. The
	// `backend` param is advertised only when there's more than one — asking a
	// model to choose from a set of one is pure schema weight.
	backends []ImageBackendChoice
	// inboundMedia is how many photos arrived with THIS request. Drives the
	// note above; zero on the static schema, which has no session.
	inboundMedia int
	// kept is the reference library, named in the schema so the model knows it
	// EXISTS. Safe to put here because kept names are stable: they change on
	// keep/forget, not per turn and not per image operation, so unlike the
	// recent ring they do not churn the prefix cache.
	kept []keptRef
	// editors are the subset that take source photos. Disjoint from backends:
	// an img2img graph requires its input, a txt2img graph has nowhere to put
	// one, so a backend belongs to exactly one action.
	editors []ImageBackendChoice
}

// liveImageActions reads what's configured for this caller. The backend list is
// memoized on the session (ReachableImageBackends), so the DynamicChatTool
// cheapness contract holds across repeated catalog builds.
func liveImageActions(sess *ToolSession) imageActions {
	cfg := LoadWebSearchConfig()
	var generators, editors []ImageBackendChoice
	for _, b := range ReachableImageBackends(sess) {
		if b.Edits {
			editors = append(editors, b)
		} else {
			generators = append(generators, b)
		}
	}
	return imageActions{
		kept:         keptRefsFor(sess),
		inboundMedia: sess.InboundMediaCount(),
		find:         cfg.Provider == "serper" && cfg.APIKey != "",
		fetch:        true,
		// Not ImageGenerationAvailable(): that only asks whether a provider is
		// SET, and the provider can be an editing backend. reachableImageBackends
		// already folds a configured built-in into this list, so counting
		// generators is both simpler and the honest question.
		generate: len(generators) > 0,
		edit:     len(editors) > 0,
		backends: generators,
		editors:  editors,
	}
}

// maxEditImages is the largest source-photo count any reachable editing backend
// accepts — what the `images` param can promise.
func (a imageActions) maxEditImages() int {
	max := 0
	for _, e := range a.editors {
		if e.MaxImages > max {
			max = e.MaxImages
		}
	}
	return max
}

// imagesParamDesc names every reference form a source image can take. The
// framework's own image space (image#N) leads, because it's the one that makes
// "edit the picture you just made" work without the model tracking filenames.
//
// The CONTENTS of the space are deliberately absent: they change on every image
// operation, and a tool schema that changes every turn re-pays cold prefill.
// The ids come back in tool results and from action="help".
func (a imageActions) imagesParamDesc() string {
	// The three forms are split by WHO produced the picture, not by when.
	//
	// Both other splits have already failed in production. Describing image#N
	// as "a recent image" made it the obvious pick for "blend the two photos I
	// just sent", and it resolved to the last two pictures the assistant had
	// generated — a confidently wrong answer rather than an error. Splitting on
	// THIS TURN vs EARLIER then sent an agent that had just downloaded two
	// photos to media#1 and media#2, which named nothing at all, because what
	// it had done was this turn and media#N was the this-turn form.
	//
	// Origin is the axis that actually separates them: the user attached it, or
	// a tool made it. And when a tool made it, the filename that tool returned
	// is the most direct handle there is — no numbering to keep straight.
	d := "(edit) Source image(s) to change, as references. " +
		"For a picture a TOOL gave you — found, downloaded or generated — use the workspace filename it returned. That names one picture and goes on naming it. " +
		"Use \"media#1\", \"media#2\" ONLY for a photo the USER ATTACHED to their message, numbered in the order they arrived, so media#1 is the first one they sent. Nothing you produced yourself is ever a media#N. " +
		"For a picture from earlier in the conversation whose filename is gone, \"image#1\" is the most recent one either of you produced or received (call action=\"help\" to list them; these numbers SHIFT as new pictures are saved). "
	switch n := a.maxEditImages(); {
	case n > 1:
		d += "Up to " + strconv.Itoa(n) + ", and this backend expects EXACTLY that many when composing: pass all " + strconv.Itoa(n) + " for a blend. ORDER MATTERS: the first is the base/subject, later ones composite onto it. "
	case n == 1:
		// Stated, because the alternative is discovering it by failing: asked to
		// combine three pictures the model picked one, blended nothing, and
		// called it done.
		d += "This backend changes ONE picture at a time and cannot composite several — if the user asks to combine pictures, tell them that rather than choosing one of them. "
	}
	d += "A picture you kept under a name is \"image#<that name>\" and stays valid indefinitely. "
	return d + "A web URL is NOT accepted — fetch it first, then pass the saved filename."
}

// anyEditorTakesMask reports whether inpainting is possible on some backend.
func (a imageActions) anyEditorTakesMask() bool {
	for _, e := range a.editors {
		if e.AcceptsMask {
			return true
		}
	}
	return false
}

// selectableBackends is every backend a caller may name, generators and editors
// together, in ReachableImageBackends' sorted order. One `backend` param covers
// both actions: the model holds one concept ("which backend"), and picking one
// that can't do the requested action is caught at run with an explanation.
func (a imageActions) selectableBackends() []ImageBackendChoice {
	out := make([]ImageBackendChoice, 0, len(a.backends)+len(a.editors))
	out = append(out, a.backends...)
	out = append(out, a.editors...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// defaultBackend names the backend a caller lands on when it omits `backend`.
func (a imageActions) defaultBackend() string {
	for _, b := range a.selectableBackends() {
		if b.Default {
			return b.Name
		}
	}
	return ""
}

// backendNames lists the selectable backends in sorted order — never re-derived
// from a map, so the enum is stable between turns.
func (a imageActions) backendNames() []string {
	sel := a.selectableBackends()
	out := make([]string, 0, len(sel))
	for _, b := range sel {
		out = append(out, b.Name)
	}
	return out
}

// backendParamDesc folds each backend's role and PromptGuidance into the one
// description the param can carry. JSON Schema has no per-enum-value docs, and
// that guidance used to live on each generate_image_<name> tool's own
// description — without this it would be lost in the collapse.
func (a imageActions) backendParamDesc() string {
	var b strings.Builder
	b.WriteString("Which image backend to render with.")
	if d := a.defaultBackend(); d != "" {
		b.WriteString(" Omit to use the default (" + d + ").")
	} else {
		b.WriteString(" Omit to use the configured default.")
	}
	var notes []string
	for _, c := range a.selectableBackends() {
		note := c.Name + ": "
		if c.Edits {
			note += "edits photos (use with action=edit)"
			if c.MaxImages > 1 {
				// "up to" invited passing fewer, which a compose graph refuses:
				// every mapped input has to be filled or it renders the
				// placeholder its workflow was saved with.
				note += ", composes " + strconv.Itoa(c.MaxImages) + " images and needs all " + strconv.Itoa(c.MaxImages)
			}
		} else {
			note += "generates from text (use with action=generate)"
		}
		if c.Guidance != "" {
			note += " — " + c.Guidance
		}
		notes = append(notes, note)
	}
	if len(notes) > 0 {
		b.WriteString(" Backends — " + strings.Join(notes, " "))
	}
	return b.String()
}

// names lists the enabled actions in a FIXED order. Never derived from a map —
// a reordering enum invalidates the prompt prefix cache every turn.
func (a imageActions) names() []string {
	var out []string
	for _, c := range []struct {
		name string
		on   bool
	}{{"find", a.find}, {"fetch", a.fetch}, {"generate", a.generate}, {"edit", a.edit}} {
		if c.on {
			out = append(out, c.name)
		}
	}
	return out
}

// imageSchema is one action set's advertised schema.
type imageSchema struct {
	desc   string
	params map[string]ToolParam
}

// imageSchemaFor builds the description + parameters for exactly the actions
// that will run. An action whose backing config is missing is not mentioned at
// all: advertising `find` with no serper key produced a guaranteed-failing call
// ("find_image requires the serper search provider…") that the model had no way
// to predict, and it would retry it.
//
// A schema with NO actions returns the zero value (nil params), which marks the
// tool unavailable — an empty enum would invalidate the whole tool payload for
// the turn.
func imageSchemaFor(a imageActions) imageSchema {
	names := a.names()
	if len(names) == 0 {
		return imageSchema{}
	}
	// `help` is advertised in the description below and was NOT in this enum, so
	// the schema forbade the one action the description tells the model to call
	// to find out which pictures it can reference. On a grammar-constrained
	// backend that is not a hint being ignored, it is a value the sampler cannot
	// emit — the documented way out of "which file was the garage image?" was
	// unreachable, and the model went guessing at filenames instead.
	//
	// Appended here rather than to names(), because names() is the CAPABILITY
	// list: its emptiness is what marks the whole tool unavailable, and help
	// needs no backend, so a deployment with nothing wired must not start
	// advertising an image tool that can only describe itself.
	// keep/forget join help outside names(): like help they need no backend
	// (the image space is framework-side), so they must not make an
	// otherwise-unconfigured deployment look like it has an image tool.
	actions := append(append(make([]string, 0, len(names)+3), names...), "help", "keep", "forget")
	desc := "Work with images — single entry point; pick the action matching intent. actions: "
	params := map[string]ToolParam{
		"action": {Type: "string", Enum: actions, Description: strings.Join(actions, " | ") + "."},
	}
	if a.find {
		desc += "find (search the web for a picture/meme/GIF/photo by description and save the best match — use whenever the user wants a picture of something and has no URL), "
		params["query"] = ToolParam{Type: "string", Description: "(find) Description of the image to find (e.g. 'funny cat meme', 'golden gate bridge sunset', 'surprised pikachu')."}
	}
	if a.fetch {
		desc += "fetch (download a specific image URL you already have), "
		params["url"] = ToolParam{Type: "string", Description: "(fetch) Direct URL of the image to download (must resolve to an image file: jpg, png, gif, webp, etc.)."}
	}
	if a.generate {
		desc += "generate (create a NEW image from a text prompt — DALL·E / Stable Diffusion / whatever's wired; generation makes things up, so NOT for real-world reference), "
	}
	// One `prompt`, described for every action that reads it. It used to be
	// added only for generate and labelled "(generate)", so on an edit the
	// model treated it as another action's field and left it out — and a blend
	// with no instructions is the backend's guess at what you wanted, not
	// yours. Say what it means for each.
	if a.generate || a.edit {
		switch {
		case a.generate && a.edit:
			params["prompt"] = ToolParam{Type: "string", Description: "(generate) What to create. (edit) What should CHANGE, or HOW the sources should combine — \"make it snowy\", \"put the subject on a beach\", \"one creature with the bear's body and the pig's snout\". Give one on an edit or blend unless you truly want the backend's default treatment: without it the backend decides how to combine them, and the result is its guess rather than the request."}
		case a.generate:
			params["prompt"] = ToolParam{Type: "string", Description: "(generate) Detailed description of the image to create."}
		default:
			params["prompt"] = ToolParam{Type: "string", Description: "(edit) What should CHANGE about the source photo(s), or HOW they should combine — \"make it snowy\", \"one creature with the bear's body and the pig's snout\". Give one unless you truly want the backend's default treatment: without it the backend decides, and the result is its guess rather than the request."}
		}
	}
	if a.edit {
		desc += "edit (change an EXISTING photo, or combine several — retouch, restyle, replace a background, composite; needs source image(s), not a blank canvas, and normally a `prompt` saying what should change or how they should combine), "
		params["images"] = ToolParam{
			Type:        "array",
			Items:       &ToolParam{Type: "string"},
			Description: a.imagesParamDesc(),
		}
		if a.anyEditorTakesMask() {
			params["mask"] = ToolParam{Type: "string", Description: "(edit) Optional black-and-white mask image (same reference forms as images) marking WHICH PART to change. White = repaint, black = keep. Use for \"change just the sky\"."}
		}
	}
	if len(a.backendNames()) > 1 {
		params["backend"] = ToolParam{Type: "string", Enum: a.backendNames(), Description: a.backendParamDesc()}
	}
	params["name"] = ToolParam{Type: "string", Description: "(keep/forget) Short stable name for a reference image you want to still have later — \"brand_mark\", \"house_style\". Letters, digits, - and _; not a bare number. Reuse the same name to replace what it points at."}
	params["ref"] = ToolParam{Type: "string", Description: "(keep) Which picture to keep, as an image id. Defaults to image#1, the most recent one — so keeping what you just made needs only a name."}
	params["note"] = ToolParam{Type: "string", Description: "(keep) Optional: why you are keeping it, in one line. Stored with the image and recalled with it later, so write what a future you would need to decide whether this is the right picture."}
	// Gloss it. A bare "help." reads as boilerplate every schema carries, and
	// what this one actually does — name the pictures that are still reachable —
	// is the answer to the question a stalled edit is asking.
	desc += "help (list the pictures you can still reference, by id, with what each one is), "
	desc += "keep (save a picture under a NAME so it survives — recent ids shift as new pictures arrive and eventually drop, a kept one answers to image#<name> indefinitely; use it for a reference you expect to want again: a logo, a style sample, a chart to match), "
	desc += "forget (drop a kept image by name). "
	desc += "Each saves into your session workspace and returns the path — it does NOT deliver; follow up with workspace(action=\"attach\", path=...) to ship the file."
	// Omitting an action hides that the capability EXISTS, and that cuts both
	// ways. For find, fetch is a fair substitute and silence costs nothing. For
	// edit it is actively harmful: asked to blend two pictures with no editing
	// backend wired, a model has no idea blending was ever a thing, so it writes
	// a prompt describing the combination and generates a NEW image — handing
	// back something that looks like an answer and isn't. Say the capability is
	// missing so it can say so too.
	if a.generate && !a.edit {
		desc += " NOTE: this deployment can only CREATE images from text — nothing here can modify, blend, or combine existing pictures. If the user asks you to edit, blend, composite, or change a photo, tell them image editing is not configured. Do NOT write a prompt describing the combination and generate a new image instead: that is a different picture, not their photo, and presenting it as the edit is worse than saying it can't be done."
	}
	// A photo arrived with THIS request. Said here, in the schema the model
	// reads before it picks an action, because the media manifest lands further
	// down with the message and by then the action is often already chosen.
	// Costs a schema change only on turns that carry an image — which are
	// already paying cold prefill for the image itself.
	if a.edit && a.inboundMedia > 0 {
		desc += fmt.Sprintf(" NOTE: %d picture(s) arrived with this request, addressable as media#1", a.inboundMedia)
		if a.inboundMedia > 1 {
			desc += fmt.Sprintf("-media#%d", a.inboundMedia)
		}
		desc += ". If the request concerns them, it is an EDIT — pass those ids in images. Do not generate a replacement." +
			" If one shows a subject you could be asked for again — a person, a pet, a product, a place — keep it NOW" +
			" (action=\"keep\", ref=\"media#1\", name=\"…\"): a media id lasts only this turn and ring ids age out as new pictures arrive," +
			" whereas a kept name works indefinitely. Asking the user to re-send a photo they already sent is the failure this avoids."
	}
	// The reference library, named before the action is chosen. Without this the
	// library is reachable only by calling help, which the model has no reason
	// to do — so a request naming a subject it HAS a reference for was answered
	// by inventing a fresh one that looks like somebody else.
	if a.edit && len(a.kept) > 0 {
		desc += " You have reference images kept under stable ids: " + describeKeptRefs(a.kept) +
			". If a request names a subject you hold a reference for, pass that id in images" +
			" — generating instead invents a DIFFERENT-looking subject, which is rarely what was asked for."
	}
	// The decision rule only helps when there's a decision to make.
	if len(names) > 1 {
		desc += " Decision:"
		if a.find {
			desc += " wants a picture of something, no URL → find."
		}
		if a.fetch {
			desc += " Gave an image URL → fetch."
		}
		// Edit is tested FIRST. Read in the other order, "make x sit in y" trips
		// "wants something drawn / created" before anything mentions that a
		// photo is involved — and the model produces a different picture that
		// ignores theirs, which is the single most reported failure of this
		// tool.
		if a.edit {
			desc += " A picture already exists and the ask is about THAT picture (\"this photo\", \"the one you just made\", \"combine these\", \"make x sit in y\", \"put x in it\", \"add x\", \"make it night\") → edit, passing its id in images."
		}
		if a.generate {
			desc += " Wants something drawn / created / imagined FROM NOTHING, with no existing picture involved → generate."
		}
		if a.edit && a.generate {
			desc += " When a photo came with the request, generate is almost never right: it draws a NEW scene and the user's picture takes no part in it."
		}
		// Text-to-image cannot depict a REAL subject, only invent one that fits
		// the words. For a specific person that means the wrong face — and a
		// wrong face is not a worse rendering, it is a picture of somebody
		// else. So the rule is not "prefer a reference", it is "work from the
		// best picture you can obtain, and generate only what is imaginary".
		if a.generate && a.find {
			// Stated as the complement, because a rule that only says when to
			// search reads as "search first, always" — and a search for "a dog
			// on a skateboard" returns somebody's photo to imitate instead of
			// the picture that was asked for.
			desc += " A GENERIC subject — a dog, a mountain, a businessman, a house — has no particular thing to find a picture OF, so generate it directly and do not search first."
		}
		if a.generate {
			desc += " A REAL, specific subject — a named person, someone the user knows, a particular place, product or logo — is depicted FROM A PICTURE OF IT, never from a text prompt: generation invents a likeness, which for a real person is simply somebody else's face."
			switch {
			case a.edit && a.find:
				desc += " Use the best picture you have of them — one kept, or one that arrived with the request — and if you have none, find one first and work from that." +
					// Without this the rule dead-ends. Told to find a reference
					// and finding nothing, the model either stalls or quietly
					// generates anyway — and an invented likeness delivered
					// without comment is a picture of somebody else presented
					// as the person who was asked for.
					" If the search turns up nothing usable, generate it and SAY you could not find a reference, so nobody takes the likeness for the real person."
			case a.edit:
				desc += " Use the best picture you have of them — one kept, or one that arrived with the request — and if you have none, ask for one rather than inventing it."
			case a.find:
				desc += " Find a picture of the real subject rather than describing it to a generator."
			}
			if a.edit {
				// All-or-nothing was the trap: holding a picture of one person
				// and not the other, the whole scene got generated and BOTH
				// faces came out wrong — including the one there was a photo
				// of. Partial beats none, every time.
				// Nobody sends a photo and then wonders whether it will be
				// used. Offering the choice reads as not having understood the
				// request, and asking spends a turn on a question with one
				// answer.
				desc += " A reference you hold is used by DEFAULT — the person who sent it assumes it will be, so do not ask whether to use their picture and do not offer generating from scratch as an alternative. Use it, then say which reference you worked from."
				desc += " A scene with several real subjects uses every reference you have and invents only the rest: pass the pictures you hold, name in the prompt which reference is which person, and describe the ones you have no picture of. Never drop a reference because the set is incomplete — one real face beside one invented is strictly better than two invented, and a group photo counts as a reference for each person in it."
			}
		}
	}
	return imageSchema{desc: desc, params: params}
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
	switch strings.ToLower(strings.TrimSpace(StringArg(args, "action"))) {
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
	switch strings.ToLower(strings.TrimSpace(StringArg(args, "action"))) {
	case "generate":
		_, err := planGenerate(sess, args, avail)
		return err
	case "edit":
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
	switch strings.ToLower(strings.TrimSpace(StringArg(args, "action"))) {
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
	switch action {
	case "find":
		if !avail.find {
			return "", fmt.Errorf("the find action is unavailable — image search needs the serper provider with an API key configured. Use fetch with a direct image URL, or ask the user to configure search")
		}
		return (&FindImageTool{}).RunWithSession(args, sess)
	case "fetch":
		return (&FetchImageTool{}).RunWithSession(args, sess)
	case "generate":
		return generateImage(sess, args, avail)
	case "edit":
		return editImage(sess, args, avail)
	case "keep":
		name := StringArg(args, "name")
		ref := strings.TrimSpace(StringArg(args, "ref"))
		if ref == "" {
			ref = RecentImageRefPrefix + "1" // "keep what I just made" is the common case
		}
		kept, err := KeepImage(sess, ref, name, StringArg(args, "note"))
		if err != nil {
			return "", err
		}
		out := fmt.Sprintf("Kept as %s. That name keeps working from now on — pass it anywhere an image id goes, in this conversation or a later one.", kept.Ref)
		if kept.Caption != "" {
			out += "\nWhat it shows: " + kept.Caption
		}
		// Say it is recallable. Otherwise the model has no way to know it can
		// find this again later without having kept a note of the name itself.
		out += "\nA detailed description went to your memory alongside it, so a later question can find this picture — and work from what it looks like — without you remembering the name or looking at it again."
		// Say what keeping your own output does and does not buy. Silence here
		// reads as "this is now a reference", which is the belief that had
		// invented subjects standing in for real ones.
		if kept.Origin.AgentMade() {
			out += fmt.Sprintf("\nNote: you MADE this picture (%s), so it is kept but NOT treated as a reference — it is not evidence of what any real thing looks like, and it won't be offered as one. Reference images are the ones you were given or found.", kept.Origin)
		}
		return out + "\nNOT delivered — keeping only files it away. To send it, attach it as you would any image.", nil
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
		help := "image actions: " + strings.Join(avail.names(), " | ") + ". Each saves to your workspace and returns the path; deliver with workspace(action=\"attach\", path=...)."
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

// --- FetchImageTool ---

type FetchImageTool struct{}

func (t *FetchImageTool) Name() string       { return "fetch_image" }
func (t *FetchImageTool) Caps() []Capability { return []Capability{CapNetwork, CapRead} } // HTTP GET image
func (t *FetchImageTool) Desc() string {
	return "Download an image from a URL into your session workspace. Returns the saved path. Does NOT deliver — call workspace(action=\"attach\", path=..., cleanup=true) to ship the file. Use this after finding an image URL via web_search, or whenever you already have a specific image URL the user wants."
}
func (t *FetchImageTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"url": {Type: "string", Description: "Direct URL of the image to download (must resolve to an image file: jpg, png, gif, webp, etc.)."},
	}
}
func (t *FetchImageTool) IsInternetTool() bool { return true }

func (t *FetchImageTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("fetch_image requires a session context — use GetAgentToolsWithSession")
}
func (t *FetchImageTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	rawURL := StringArg(args, "url")
	if rawURL == "" {
		return "", fmt.Errorf("url is required")
	}
	return downloadImageTo(rawURL, sess)
}

// --- FindImageTool ---

type FindImageTool struct{}

func (t *FindImageTool) Name() string       { return "find_image" }
func (t *FindImageTool) Caps() []Capability { return []Capability{CapNetwork, CapRead} } // search + download
func (t *FindImageTool) Desc() string {
	return "Search for an image by description and save the SINGLE BEST MATCH into your session workspace. The framework's internal vision-LLM picks the best candidate from multiple search results. Returns the saved path. Does NOT deliver to the user — call workspace(action=\"attach\", path=..., cleanup=true) to ship the file. Use this whenever the user asks for a picture, meme, GIF, or photo."
}
func (t *FindImageTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"query": {Type: "string", Description: "Description of the image to find (e.g. 'funny cat meme', 'golden gate bridge sunset', 'surprised pikachu')."},
	}
}
func (t *FindImageTool) IsInternetTool() bool { return true }

func (t *FindImageTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("find_image requires a session context — use GetAgentToolsWithSession")
}
func (t *FindImageTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	query := StringArg(args, "query")
	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	cfg := LoadWebSearchConfig()
	if cfg.Provider != "serper" || cfg.APIKey == "" {
		return "", fmt.Errorf("find_image requires the serper search provider with an API key configured")
	}
	results, err := SerperImageSearch(query, cfg.APIKey)
	if err != nil {
		return "", fmt.Errorf("image search failed: %w", err)
	}
	if len(results) == 0 {
		return "", fmt.Errorf("no image results found for %q", query)
	}

	// Save the chosen image to the session workspace and return its path with
	// the standard delivery hint. No auto-attach; the LLM ships it via
	// workspace(action="attach", path=..., cleanup=true).
	saveAndReturn := func(data []byte, meta SerperImageResult) (string, error) {
		wsDir, err := EnsureSessionWorkspace(sess)
		if err != nil {
			return "", fmt.Errorf("session workspace unavailable: %w", err)
		}
		name := "find-" + shortID() + extForMime(http.DetectContentType(data))
		target := filepath.Join(wsDir, name)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return "", fmt.Errorf("create parent dir: %w", err)
		}
		if err := os.WriteFile(target, data, 0600); err != nil {
			return "", fmt.Errorf("save image: %w", err)
		}
		Log("[imagefetch/find_image] query=%q delivered %q (title: %q, source: %s)", query, name, meta.Title, meta.Source)
		msg := fmt.Sprintf(
			"NOT sent yet — this only SAVED the image to your workspace as %q (title: %q, source: %s). It is NOT delivered, and your reply text alone will NOT include it. To actually send it you MUST call workspace(action=\"attach\", path=%q, cleanup=true) — do that BEFORE you write a reply claiming you sent it. Skip the attach ONLY if the user just wants info about it (describe / identify / summarize), not the image itself.",
			name, meta.Title, meta.Source, name,
		)
		// A found image belongs in the image space too. Without this, "find a
		// photo of a barn, now make it snowy" only worked while the filename was
		// still in context, and not at all on a later turn.
		if ref := RecordRecentImage(sess, data, "found: "+truncate(query, 60), ImageFromFound); ref != "" {
			msg += editHandleHint(name, ref)
		}
		msg += showToModel(sess, data, "It is a search result, not a verified answer")
		return msg, nil
	}

	// LAZY short-circuit: evaluate results ONE AT A TIME and stop at the first
	// that matches — no need to fetch + vision-score all five every time (the
	// vision call is the expensive part). The first candidate that BOTH
	// text-matches (page/title mentions the subject) AND visually depicts the
	// query is the answer; we only look deeper on a miss. A best-vision-match
	// fallback covers results whose title was too sparse to text-match but
	// whose image is right. Per candidate: one page fetch, one image, one
	// vision call — and usually just the first.
	const maxFindCandidates = 6
	// identityRequired: the query names a specific subject, so PROVENANCE — the
	// page actually being about it — is the only evidence available, and a
	// vision score cannot substitute for it no matter how high. See
	// namedSubjectTokens.
	//
	// This is also the escape hatch for a deployment whose model has no image
	// modality at all. Provenance is a TEXT check: it needs no vision, costs no
	// vision call, and is the one guarantee that survives when the screen is
	// absent or refuses to answer. Everything below treats vision as a bonus
	// on top of it rather than a prerequisite.
	identityRequired := len(namedSubjectTokens(query)) > 0
	var bestData []byte
	var bestMeta SerperImageResult
	bestScore := -1
	bestTextMatch := false
	usable := 0
	// scored counts candidates the vision screen actually RATED. A screen that
	// returns no number is not the same as one that rates everything zero, and
	// conflating them is how "a picture of <a named person>" became "could not
	// download any usable image": asking a vision model to confirm a specific
	// person's identity is the question it most often declines, so it answers
	// with prose and no trailing rating, parseTrailingScore returns -1 for every
	// candidate, and a search that fetched six perfectly good photographs
	// reports a download failure.
	scored := 0
	// fallback is the candidate to use when the screen abstains entirely —
	// preferring one whose page text matched, since that is the only signal
	// left at that point.
	var fallbackData []byte
	var fallbackMeta SerperImageResult
	fallbackTextMatch := false
	for _, r := range results {
		if usable >= maxFindCandidates {
			break
		}
		ogImage, pageMentions := inspectPage(r.Link, query)
		textMatch := pageMentions || pageMentionsSubject(strings.ToLower(r.Title), query)
		// Prefer the page's real image; fall back to the (accessible) result
		// image URL when the source blocks the direct fetch.
		var data []byte
		var ok bool
		if ogImage != "" && ogImage != r.ImageURL {
			data, _, _, ok = fetchValidImage(ogImage, r.Link)
		}
		if !ok {
			data, _, _, ok = fetchValidImage(r.ImageURL, r.Link)
		}
		if !ok {
			Log("[imagefetch/find_image] candidate skipped — no usable image for %q (source blocked?)", r.Link)
			continue
		}
		usable++
		if fallbackData == nil || (textMatch && !fallbackTextMatch) {
			fallbackData, fallbackMeta, fallbackTextMatch = data, r, textMatch
		}
		// No vision configured → can't screen the pixels; take the first
		// text-matching result (or the first usable one at all).
		//
		// Except when the query names someone: then provenance is not a
		// preference, it is the entire basis for believing this is the right
		// subject. Without vision AND without the page mentioning them, there
		// is no evidence at all — keep looking, and refuse below if none of the
		// candidates ever mentions them.
		if sess.LLM == nil {
			if textMatch || (bestScore < 0 && !identityRequired) {
				return saveAndReturn(data, r)
			}
			continue
		}
		score := scoreImageMatch(sess, data, query)
		if score >= 0 {
			scored++
		}
		Log("[imagefetch/find_image] query=%q candidate %d (title %q) text=%v vision=%d/100", query, usable, r.Title, textMatch, score)
		if textMatch && score >= imageMatchThreshold {
			return saveAndReturn(data, r) // confident match — stop here
		}
		if score > bestScore {
			bestData, bestMeta, bestScore, bestTextMatch = data, r, score, textMatch
		}
		// Escalation: the page IS about the subject (text-matched) but the
		// cheap image — a blocked source that fell back to Google's thumbnail,
		// or a low-res cache — didn't pass vision. Render the page in the
		// headless browser to pull its REAL image (bypasses hotlink
		// protection) and re-score before abandoning this candidate. Getting
		// the right image HERE is cheaper than paying a fresh page-fetch +
		// vision call on the next candidate, so it's faster overall when it
		// converts a multi-candidate search into a one-candidate hit.
		if textMatch && score < imageMatchThreshold {
			if raw, rerr := browser.FetchPageImage(r.Link); rerr == nil {
				if rdata, _, _, rok := normalizeToJPEG(raw); rok {
					rscore := scoreImageMatch(sess, rdata, query)
					Log("[imagefetch/find_image] query=%q candidate %d browser-rendered image vision=%d/100 (cheap image was %d)", query, usable, rscore, score)
					if rscore >= imageMatchThreshold {
						return saveAndReturn(rdata, r)
					}
					if rscore > bestScore {
						bestData, bestMeta, bestScore, bestTextMatch = rdata, r, rscore, textMatch
					}
				}
			} else {
				Log("[imagefetch/find_image] query=%q browser render failed for %q: %v", query, r.Link, rerr)
			}
		}
	}
	// Nothing both text- and vision-matched. Use the best vision match if it's
	// a confident depiction; otherwise reject rather than return a wrong image.
	//
	// A NAMED subject never reaches here on pixels alone. This is the exit that
	// shipped four different realtors at 95/100 for one man's name, and the one
	// that has to say what it actually knows: that it found a picture of the
	// right KIND of thing and has no evidence it is the right one.
	if bestScore >= imageMatchThreshold && (!identityRequired || bestTextMatch) {
		Log("[imagefetch/find_image] query=%q no text+vision match; using best vision match %d/100", query, bestScore)
		return saveAndReturn(bestData, bestMeta)
	}
	if identityRequired && bestScore >= imageMatchThreshold && !bestTextMatch {
		Log("[imagefetch/find_image] query=%q REFUSED: best visual match %d/100 but no source page mentions %v", query, bestScore, namedSubjectTokens(query))
		return "", identityUnverifiableError(query, usable)
	}
	switch findOutcome(usable, scored, bestScore) {
	case findNoImages:
		return "", fmt.Errorf("could not download any usable image for %q (sources may be blocking the fetch)", query)
	case findScreenAbstained:
		// Images arrived; the screen just never rated any of them. A screen that
		// will not answer is, for this purpose, the same as not having one — so
		// fall back to the no-vision behaviour (first text-matching candidate)
		// rather than throwing away results it declined to judge.
		//
		// Which makes provenance load-bearing here: with nothing screening the
		// pixels, the page mentioning the subject is the ONLY evidence left,
		// and shipping an unscreened photo of a stranger is worse than saying
		// it wasn't found. Note the screen abstains most often on exactly these
		// queries — asking a model to confirm a specific person is the question
		// it declines — so this path and the identity case coincide constantly.
		if identityRequired && !fallbackTextMatch {
			Log("[imagefetch/find_image] query=%q REFUSED: vision screen abstained on all %d candidate(s) and none mentions %v", query, usable, namedSubjectTokens(query))
			return "", identityUnverifiableError(query, usable)
		}
		Log("[imagefetch/find_image] query=%q vision screen returned no rating for any of %d candidate(s) — delivering the best text match unscreened", query, usable)
		return saveAndReturn(fallbackData, fallbackMeta)
	}
	return "", fmt.Errorf("found image(s) for %q but none clearly depict it (best visual match %d/100) — the search may have surfaced lookalikes or unrelated results; refine the query, or use fetch_image with a specific image URL", query, bestScore)
}

// identityUnverifiableError is what a search for a specific person says when it
// found photographs of the right sort and nothing tying any of them to that
// person.
//
// It has to be explicit that this is not a near-miss to be retried with better
// wording, or the model simply searches again — that is what produced six
// searches in ninety seconds this morning, each delivering a different stranger
// with more confidence than the last. Rewording cannot fix "no page about this
// person carries this photo".
func identityUnverifiableError(query string, usable int) error {
	return fmt.Errorf("found %d photo(s) matching %q in general, but NONE from a page that mentions them by name — so there is no evidence any of these is the right person, only that they look like the sort of picture asked for. Do NOT retry with a reworded query; a face cannot be confirmed from pixels and rewording will just return a different stranger. Tell the user you couldn't find a picture of them, and ask for one (a photo, a link, a profile URL) if you need it", usable, query)
}

// findResolution is how a search that produced no confident match ends.
type findResolution int

const (
	findNoImages        findResolution = iota // nothing downloadable — the sources blocked us
	findScreenAbstained                       // images arrived, the vision screen rated none of them
	findAllRejected                           // the screen rated them and they genuinely do not match
)

// findOutcome separates three failures that used to be two. The middle one is
// the one that mattered: a vision screen asked to confirm a NAMED PERSON often
// declines to answer at all, which left every candidate unscored and reported
// as though nothing had downloaded — sending the caller to debug a network
// problem that never happened.
func findOutcome(usable, scored, bestScore int) findResolution {
	switch {
	case usable == 0:
		return findNoImages
	case scored == 0:
		return findScreenAbstained
	default:
		_ = bestScore
		return findAllRejected
	}
}

// --- GenerateImageTool ---

type GenerateImageTool struct{}

func (t *GenerateImageTool) Name() string       { return "generate_image" }
func (t *GenerateImageTool) Caps() []Capability { return []Capability{CapNetwork, CapRead} } // image-gen API call
func (t *GenerateImageTool) Desc() string {
	return "Generate a NEW image from a text description (DALL·E / Stable Diffusion / whichever image-gen backend is wired up) and save it into your session workspace. Returns the saved path. Does NOT deliver — call workspace(action=\"attach\", path=..., cleanup=true) to ship the file. USE ONLY when the user explicitly asks to CREATE / DRAW / MAKE / GENERATE a fresh image. NOT for finding existing images (use find_image), downloading a known URL (use fetch_image), or page screenshots (use screenshot_page). Generation makes things up — wrong tool for real-world reference."
}
func (t *GenerateImageTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"prompt": {Type: "string", Description: "Detailed description of the image to generate."},
	}
}
func (t *GenerateImageTool) IsInternetTool() bool { return true }

func (t *GenerateImageTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("generate_image requires a session context — use GetAgentToolsWithSession")
}
func (t *GenerateImageTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	// The standalone tool has no backend selector — it always renders through
	// the configured default. Backend choice arrives via the grouped `image`
	// tool, which owns the reachability check.
	return generateImageInto(sess, StringArg(args, "prompt"), "")
}

// generateImage runs the generate action. Split out of the switch for the same
// reason editImage is: the argument checks are worth testing without a live
// connector behind them.
func generateImage(sess *ToolSession, args map[string]any, avail imageActions) (string, error) {
	backend, err := planGenerate(sess, args, avail)
	if err != nil {
		return "", err
	}
	return generateImageInto(sess, StringArg(args, "prompt"), backend)
}

// planGenerate resolves and checks the backend a generate call will use. Split
// from generateImage for the same reason planEdit is: these errors have to
// reach the model before the call detaches, not a minute after it. See
// ImageTool.Preflight.
func planGenerate(sess *ToolSession, args map[string]any, avail imageActions) (string, error) {
	if !avail.generate {
		return "", fmt.Errorf("the generate action is unavailable — no image-generation provider is configured. Tell the user image generation isn't set up; do NOT retry")
	}
	backend := strings.TrimSpace(StringArg(args, "backend"))
	// Resolve an omitted backend to a GENERATOR rather than letting it fall
	// through to the configured provider. The provider setting can point at
	// an editing backend, and an img2img graph run with no source photo
	// doesn't fail — it renders the placeholder image baked into the
	// workflow and returns it as if it were the answer.
	if backend == "" {
		backend = defaultGenerateBackend(avail)
	}
	// Same argument-before-reachability ordering as edit, and the same
	// reason: reachability is the broadest failure, so running it first
	// reports every mistake as a permissions problem.
	if backend != "" && !isGenerator(avail, backend) {
		return "", fmt.Errorf("image backend %q edits existing photos and can't generate from text alone — use one of: %s, or switch to action=\"edit\" and pass images", backend, strings.Join(generatorNames(avail), ", "))
	}
	// ENFORCEMENT. The filtered enum is a hint to the model; nothing stops
	// it naming a backend that isn't in it, so reachability is re-checked
	// here against the same list before anything dispatches.
	if !ImageBackendReachable(sess, backend) {
		names := avail.backendNames()
		if len(names) == 0 {
			return "", fmt.Errorf("image backend %q is not available to you; omit backend to use the configured default", backend)
		}
		return "", fmt.Errorf("image backend %q is not available to you — use one of: %s (or omit backend for the default)", backend, strings.Join(names, ", "))
	}
	return backend, nil
}

// editPlan is an edit call reduced to what will actually run: which backend,
// with what prompt, over which source references.
type editPlan struct {
	backend string
	prompt  string
	refs    []string
	mask    string
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
	backend := strings.TrimSpace(StringArg(args, "backend"))
	if backend == "" {
		backend = defaultEditBackend(avail)
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
		return p, fmt.Errorf("image backend %q generates from text and can't edit a photo — use one of: %s, or switch to action=\"generate\"", backend, strings.Join(editorNames(avail), ", "))
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
	return editPlan{backend: backend, prompt: prompt, refs: refs, mask: StringArg(args, "mask")}, nil
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
		Backend: p.backend,
		Prompt:  p.prompt,
		Images:  p.refs,
		Mask:    p.mask,
	})
	if err != nil {
		return "", fmt.Errorf("image edit via %q failed: %w", p.backend, err)
	}
	return saveImageResult(sess, result, "edit", "edited "+strings.Join(p.refs, "+")+": "+truncate(p.prompt, 60), ImageFromEdited)
}

// defaultEditBackend picks the editing backend when the caller names none: the
// configured default if it can edit, else the first editor. With one editor
// wired — the common case — the `backend` param isn't even advertised.
func defaultEditBackend(a imageActions) string {
	for _, e := range a.editors {
		if e.Default {
			return e.Name
		}
	}
	if len(a.editors) > 0 {
		return a.editors[0].Name
	}
	return ""
}

// defaultGenerateBackend picks the backend a generate lands on when the caller
// names none: the configured default if it can generate, else the first
// generator. Mirrors defaultEditBackend.
func defaultGenerateBackend(a imageActions) string {
	for _, b := range a.backends {
		if b.Default {
			return b.Name
		}
	}
	if len(a.backends) > 0 {
		return a.backends[0].Name
	}
	return ""
}

func generatorNames(a imageActions) []string {
	out := make([]string, 0, len(a.backends))
	for _, b := range a.backends {
		out = append(out, b.Name)
	}
	return out
}

func isGenerator(a imageActions, name string) bool {
	for _, b := range a.backends {
		if b.Name == name {
			return true
		}
	}
	return false
}

func editorNames(a imageActions) []string {
	out := make([]string, 0, len(a.editors))
	for _, e := range a.editors {
		out = append(out, e.Name)
	}
	return out
}

// backendNeedsPrompt reports whether the named editing backend has a text node
// to write a prompt into.
func backendNeedsPrompt(a imageActions, name string) bool {
	for _, e := range a.editors {
		if e.Name == name {
			return e.NeedsPrompt
		}
	}
	return true
}

func isEditor(a imageActions, name string) bool {
	for _, e := range a.editors {
		if e.Name == name {
			return true
		}
	}
	return false
}

// stringsArg reads a string-array tool argument, tolerating a single string —
// models routinely send images="photo.png" instead of ["photo.png"], and
// refusing that costs a round to teach nothing.
func stringsArg(args map[string]any, key string) []string {
	var out []string
	switch v := args[key].(type) {
	case []string:
		out = v
	case []any:
		for _, item := range v {
			if s := strings.TrimSpace(fmt.Sprint(item)); s != "" {
				out = append(out, s)
			}
		}
	case string:
		for _, part := range strings.Split(v, ",") {
			if s := strings.TrimSpace(part); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// generateImageInto renders prompt through the named backend (empty = the
// configured default), saves the result into the session workspace, and returns
// the delivery instruction. Shared by the standalone generate_image tool and the
// grouped `image` tool's generate action so the two can't drift.
//
// Callers are responsible for authorizing backend first (ImageBackendReachable).
func generateImageInto(sess *ToolSession, prompt, backend string) (string, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}
	// The turn's context, not Background: a render is the longest thing a turn
	// does, so it's the call most likely to be Stopped mid-flight.
	result, err := GenerateImageWithBackend(sess.Context(), backend, prompt, true)
	if err != nil {
		if backend != "" {
			return "", fmt.Errorf("image generation via %q failed: %w", backend, err)
		}
		return "", fmt.Errorf("image generation failed: %w", err)
	}
	via := backend
	if via == "" {
		via = "default"
	}
	Log("[imagefetch/generate_image] backend=%s generating for prompt: %s", via, truncate(prompt, 80))
	return saveImageResult(sess, result, "gen", "generated: "+truncate(prompt, 60), ImageFromGenerated)
}

// saveImageResult lands a finished image in both places it needs to be: the
// session workspace (so workspace(attach) can deliver it) and the image space
// (so a LATER turn can edit it by id).
//
// The image space is what replaced telling the model to clean up after itself.
// It keeps a bounded ring and prunes on write, so the reply no longer has to
// push cleanup=true — the file is retained on purpose and named something the
// model can actually refer back to.
func saveImageResult(sess *ToolSession, result *ImageGenResult, prefix, note string, origin ImageOrigin) (string, error) {
	var data []byte
	var err error
	if strings.HasPrefix(result.URL, "http://") || strings.HasPrefix(result.URL, "https://") {
		data, err = downloadImageBytes(result.URL)
	} else {
		data, err = os.ReadFile(result.URL)
		os.Remove(result.URL)
	}
	if err != nil {
		return "", fmt.Errorf("failed to retrieve the finished image: %w", err)
	}
	wsDir, err := EnsureSessionWorkspace(sess)
	if err != nil {
		return "", fmt.Errorf("session workspace unavailable: %w", err)
	}
	name := prefix + "-" + shortID() + ".png"
	target := filepath.Join(wsDir, name)
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return "", fmt.Errorf("create parent dir: %w", err)
	}
	if err := os.WriteFile(target, data, 0600); err != nil {
		return "", fmt.Errorf("save image: %w", err)
	}
	Log("[imagefetch] %s → %s (%d bytes)", note, name, len(data))

	// A detached render has no model round after it: the turn that asked for the
	// picture ended while it was still rendering, so nobody is left to be told
	// "now call workspace(attach)". Attach it here, or the file is produced,
	// stored, announced as done — and never actually sent. Delivery of a
	// detached call's attachments is the framework's job from here on.
	if sess != nil && sess.Detached {
		sess.AppendImage(base64.StdEncoding.EncodeToString(data))
		msg := fmt.Sprintf("The finished picture (%d bytes) IS ATTACHED to this result and will be delivered with the message you send about it. Do NOT call workspace(action=\"attach\") for it — that would send it twice. Just say what it is.", len(data))
		if ref := RecordRecentImage(sess, data, note, origin); ref != "" {
			msg += fmt.Sprintf(" %s is its lasting handle if you need to edit it or send it again later.", ref)
		}
		return msg, nil
	}

	msg := fmt.Sprintf("Stored at %q (%d bytes). This is normally meant for delivery — call workspace(action=\"attach\", path=%q, cleanup=true) and then write a short line describing it. (Skip the attach only if the user explicitly asked you NOT to send it — rare.)", name, len(data), name)
	// Spell out that the workspace copy is one-shot. Saying "do not delete it"
	// next to a path the model is told to clean up reads as a contradiction, and
	// what it did instead was attach with cleanup and then try to attach the
	// same path AGAIN — which errors, because the file is gone. From there it
	// regenerated the picture and delivered the wrong one.
	if ref := RecordRecentImage(sess, data, note, origin); ref != "" {
		msg += fmt.Sprintf(" The workspace copy is consumed by that attach and the path stops working; %s is the lasting handle for this picture. Use %s to edit it later, or to send it again. Never re-attach the workspace path after a cleanup — it is already delivered.", ref, ref)
	}
	// A render is a guess at the prompt, not a rendering of it: the wrong
	// number of people, the text unreadable, the edit applied to nothing. The
	// agent used to find that out when the user did.
	msg += showToModel(sess, data, "This is what the backend produced, which is not always what was asked for")
	return msg, nil
}

// --- Serper image search ---

type SerperImageResult struct {
	ImageURL string
	Title    string
	Source   string
	Link     string
}

type serperImageResponse struct {
	Images []struct {
		ImageURL string `json:"imageUrl"`
		Title    string `json:"title"`
		Source   string `json:"source"`
		Link     string `json:"link"`
	} `json:"images"`
}

func SerperImageSearch(query, apiKey string) ([]SerperImageResult, error) {
	payload, _ := json.Marshal(map[string]any{"q": query, "num": 10})
	req, err := http.NewRequest("POST", "https://google.serper.dev/images", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-KEY", apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("serper images API error (%d): %s", resp.StatusCode, string(body))
	}

	var result serperImageResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parsing serper images response: %w", err)
	}

	var out []SerperImageResult
	for _, img := range result.Images {
		if img.ImageURL != "" {
			out = append(out, SerperImageResult{
				ImageURL: img.ImageURL,
				Title:    img.Title,
				Source:   img.Source,
				Link:     img.Link,
			})
		}
	}
	// Count every provider call — Serper bills per request regardless
	// of how many images come back. Symmetric with web_search's
	// "count on call, not on result-content" semantics.
	ProcessUsage().AddSearchCall()
	return out, nil
}

// --- HTTP helpers ---

const browserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

func FetchImageBytes(rawURL, referer string, timeoutSecs int) ([]byte, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/*,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	client := &http.Client{Timeout: time.Duration(timeoutSecs) * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}
	const maxBytes = 10 * 1024 * 1024
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return nil, fmt.Errorf("read failed: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("empty response")
	}
	return data, nil
}

// pageImageRes matches a page's representative image in its <head> meta
// tags — og:image (either attribute order) and twitter:image.
var pageImageRes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)<meta[^>]+property=["']og:image(?::url)?["'][^>]+content=["']([^"']+)["']`),
	regexp.MustCompile(`(?i)<meta[^>]+content=["']([^"']+)["'][^>]+property=["']og:image(?::url)?["']`),
	regexp.MustCompile(`(?i)<meta[^>]+name=["']twitter:image(?::src)?["'][^>]+content=["']([^"']+)["']`),
}

// inspectPage fetches a source page ONCE and reports (a) its representative
// image URL (og:image / twitter:image, resolved absolute) and (b) whether
// the page actually MENTIONS the search subject. find_image uses both: grab
// the page's real image instead of the cached/thumbnail result image, and
// trust it only when the page is genuinely about what we searched for — the
// drill-in-and-verify step that discards mis-indexed / wrong results.
// Returns ("", false) if the page can't be fetched.
func inspectPage(pageURL, query string) (ogImage string, mentions bool) {
	if pageURL == "" {
		return "", false
	}
	req, err := http.NewRequest("GET", pageURL, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	const maxHTML = 1 << 20 // 1 MB reaches the <head> metas + visible text on any sane page
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTML))
	if err != nil {
		return "", false
	}
	page := string(body)
	mentions = pageMentionsSubject(strings.ToLower(page), query)
	for _, re := range pageImageRes {
		if m := re.FindStringSubmatch(page); len(m) > 1 && strings.TrimSpace(m[1]) != "" {
			if abs := resolvePageURL(pageURL, html.UnescapeString(strings.TrimSpace(m[1]))); abs != "" {
				ogImage = abs
				break
			}
		}
	}
	return ogImage, mentions
}

// imageQueryFiller is generic query noise that shouldn't be required to
// appear on a source page (a page about a red Ferrari needn't say "photo").
var imageQueryFiller = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "from": true,
	"photo": true, "photos": true, "image": true, "images": true,
	"picture": true, "pictures": true, "pic": true, "pics": true,
	"png": true, "jpg": true, "jpeg": true, "gif": true,
}

// significantQueryWords reduces a query to the tokens worth matching on a
// page: alphanumeric, length >= 3, minus generic filler.
func significantQueryWords(query string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len(w) >= 3 && !imageQueryFiller[w] {
			out = append(out, w)
		}
	}
	return out
}

// namedSubjectTokens returns the PROPER-NAME words a query is about — the ones
// no amount of looking at pixels can confirm.
//
// "Shazz Barbaric real estate" is two questions, not one. A vision model can
// answer the second (is this a real-estate person? yes) and has no way to touch
// the first, so it scores the half it can and returns a confident number for
// the whole thing. Production, one search, five candidates: 95, 95, 95, 85, 75
// — five different people, none of them him, every score a pass. Another run
// returned a 19th-century cigarette card of the Sultan of Zanzibar, matched
// through "Savage and Semi-Barbarous Chiefs", at a passing grade.
//
// Capitalization in the query is the signal, and it degrades the right way. A
// real subject's page says its own name — "golden gate bridge" appears on any
// page about the bridge — so requiring the name costs a genuine match nothing.
// A person who is not on the page cannot be rescued by the page being about
// real estate in general, which is exactly the substitution that was happening.
func namedSubjectTokens(query string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(query, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		r := []rune(w)
		if len(r) < 3 || !unicode.IsUpper(r[0]) {
			continue
		}
		lower := strings.ToLower(w)
		if imageQueryFiller[lower] {
			continue
		}
		out = append(out, lower)
	}
	return out
}

// pageMentionsSubject reports whether a page (already lowercased) references
// the search subject.
//
// Two tiers, because they answer different questions. Proper names are
// REQUIRED: they are what makes the request about one thing rather than a
// category, and counting them as one token among many is how a page that never
// says "Shazz" text-matched on "real estate ranch owner Texas" — five of eight
// tokens, a comfortable 60%, name absent. Everything else keeps the majority
// rule, which is right for descriptive queries where any given adjective may
// simply not appear in the prose.
//
// An uncheckable query (no significant words) passes so it never blocks a
// result.
func pageMentionsSubject(pageLower, query string) bool {
	for _, name := range namedSubjectTokens(query) {
		if !strings.Contains(pageLower, name) {
			return false
		}
	}
	toks := significantQueryWords(query)
	if len(toks) == 0 {
		return true
	}
	need := len(toks)
	if need > 3 {
		need = (len(toks)*3 + 4) / 5 // ~60%, rounded up
	}
	hit := 0
	for _, t := range toks {
		if strings.Contains(pageLower, t) {
			hit++
		}
	}
	return hit >= need
}

// resolvePageURL resolves a possibly-relative image ref against the page it
// came from, keeping only http(s) results.
func resolvePageURL(base, ref string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ""
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	abs := b.ResolveReference(r)
	if abs.Scheme != "http" && abs.Scheme != "https" {
		return ""
	}
	return abs.String()
}

// imageMatchThreshold is the minimum 0-100 vision score for find_image to
// accept a candidate. Below it, the tool reports no confident match rather
// than returning a wrong image. Tunable.
const imageMatchThreshold = 50

// scoreImageMatch asks the vision LLM to actually LOOK at ONE image and rate
// 0-100 how well it depicts the query. Forcing a one-line description first
// makes the model examine the pixels instead of guessing from metadata or
// from a confusing multi-image prompt. Returns -1 if no usable score came back.
func scoreImageMatch(sess *ToolSession, img []byte, query string) int {
	prompt := fmt.Sprintf(
		"Look closely at this image. In one sentence, describe what it ACTUALLY shows. "+
			"Then rate from 0 to 100 how well it depicts: %q "+
			"(0 = unrelated or the wrong subject, 100 = exactly that subject). "+
			"Put the rating as a plain number on its own FINAL line.", query)
	resp, err := sess.LLM.Chat(sess.Context(),
		[]Message{{Role: "user", Content: prompt, Images: [][]byte{img}}},
		WithCaller("imagefetch/find_image"),
		WithMaxRetries(0),
		WithThink(true),
	)
	if err != nil || resp == nil {
		return -1
	}
	return parseTrailingScore(resp.Content)
}

// parseTrailingScore extracts the last integer in [0,100] from the content
// (the model is asked to end with the rating on its own line).
func parseTrailingScore(s string) int {
	fields := strings.Fields(s)
	for i := len(fields) - 1; i >= 0; i-- {
		tok := strings.Trim(fields[i], ".,;:%\"'()[]")
		if n, err := strconv.Atoi(tok); err == nil && n >= 0 && n <= 100 {
			return n
		}
	}
	return -1
}

// fetchValidImage downloads a URL and returns the image RE-ENCODED AS JPEG
// plus its pixel dimensions, only if it's a genuinely usable image. ok=false
// for a download error (403 etc.), a non-image body (an HTML "access denied"
// block page from a hotlink-protected source), or an undecodable image — the
// caller then falls back to a more accessible URL.
//
// The JPEG re-encode is the fix for "the LLM isn't seeing what's attached":
// llama.cpp's vision (stb_image) can't read WebP/AVIF — the dominant formats
// on the web — so without normalizing it'd be handed bytes it can't decode
// and would hallucinate a description for a blank/wrong image while the saved
// file (the original webp) renders fine everywhere else. Decoding (webp via
// golang.org/x/image/webp) and re-encoding JPEG guarantees the model scores
// exactly what we save and attach.
func fetchValidImage(rawURL, referer string) (data []byte, w, h int, ok bool) {
	if rawURL == "" {
		return nil, 0, 0, false
	}
	d, err := FetchImageBytes(rawURL, referer, 20)
	if err != nil {
		return nil, 0, 0, false
	}
	return normalizeToJPEG(d)
}

// normalizeToJPEG decodes image bytes (jpeg/png/gif/webp) and re-encodes them
// as JPEG — the format the vision model can actually read — returning the
// JPEG plus pixel dimensions. ok=false for a non-image body or an undecodable
// image. Shared by the plain download path and the go-rod render escalation.
func normalizeToJPEG(d []byte) (data []byte, w, h int, ok bool) {
	if !strings.HasPrefix(http.DetectContentType(d), "image/") {
		return nil, 0, 0, false
	}
	img, _, derr := image.Decode(bytes.NewReader(d))
	if derr != nil {
		return nil, 0, 0, false
	}
	b := img.Bounds()
	if b.Dx() == 0 || b.Dy() == 0 {
		return nil, 0, 0, false
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 88}); err != nil {
		return nil, 0, 0, false
	}
	return buf.Bytes(), b.Dx(), b.Dy(), true
}

func downloadImageBytes(rawURL string) ([]byte, error) {
	return FetchImageBytes(rawURL, "", 30)
}

func downloadImageTo(rawURL string, sess *ToolSession) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("invalid image URL: %s", rawURL)
	}
	data, err := FetchImageBytes(rawURL, "", 20)
	if err != nil {
		return "", err
	}
	ct := http.DetectContentType(data)
	if !strings.HasPrefix(ct, "image/") {
		return "", fmt.Errorf("URL does not appear to be an image (detected: %s)", ct)
	}
	// Save to the session workspace and return the path. Does NOT
	// auto-attach — the LLM uses workspace(action="attach", path=...)
	// to deliver when it's ready.
	wsDir, err := EnsureSessionWorkspace(sess)
	if err != nil {
		return "", fmt.Errorf("session workspace unavailable: %w", err)
	}
	ext := extForMime(ct)
	name := "fetch-" + shortID() + ext
	target := filepath.Join(wsDir, name)
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return "", fmt.Errorf("create parent dir: %w", err)
	}
	if err := os.WriteFile(target, data, 0600); err != nil {
		return "", fmt.Errorf("save image: %w", err)
	}
	Log("[imagefetch/fetch_image] fetched %d bytes from %s → %s", len(data), rawURL, name)
	msg := fmt.Sprintf("NOT sent yet — this only SAVED the image to your workspace as %q (%s, %d bytes). It is NOT delivered, and your reply text alone will NOT include it. To actually send it, call workspace(action=\"attach\", path=%q, cleanup=true) — do that BEFORE you write a reply claiming you sent it. Skip the attach ONLY if the user just wants info about what's in it.",
		name, ct, len(data), name)
	// Downloaded images join the space as well — this is also the path a model
	// is told to use when it tries to pass a URL straight to edit.
	if ref := RecordRecentImage(sess, data, "downloaded: "+truncate(rawURL, 60), ImageFromFound); ref != "" {
		msg += editHandleHint(name, ref)
	}
	msg += showToModel(sess, data, "Nothing has checked what is in it")
	return msg, nil
}

// showToModel puts the picture itself in front of the agent on its next round,
// and tells it that it can now look.
//
// The gap this closes: nothing ever showed the agent the image. find handed
// back a filename, a title and a source; generate and edit handed back a path.
// The agent wrote "here's the photo of him" having seen a string that said
// "Farm & Ranch" and nothing else — it could not have caught the wrong picture,
// because it was never shown one. The only way it looked at anything was
// calling workspace(view_image) unprompted, which nothing asked it to do.
//
// The bytes go through the view-image channel: injected as a synthetic user
// message for ONE round and drained, never persisted into history, and never
// delivered to the user. Costs one image in context on the round where it is
// most useful and nothing after that.
//
// Two escape hatches, because a model that cannot see must not be asked to
// pretend:
//
//   - No LLM on the session — nowhere to send it.
//   - A DETACHED call — the turn that asked ended, and there is no next round
//     to inject into. What it produced travels to the wake as an attachment
//     instead, and telling a model "look at this" when nothing will arrive is
//     an invitation to describe an image it never got.
//
// A model wired without an image modality is the remaining case, and it is why
// this only ever ADDS a chance to catch a mistake. Nothing downstream is gated
// on the agent's verdict, so a model that cannot see simply proceeds as it does
// today. Verification that must hold with no vision at all lives in the
// provenance check, which is text.
func showToModel(sess *ToolSession, data []byte, caveat string) string {
	if sess == nil || sess.LLM == nil || sess.Detached || len(data) == 0 {
		return ""
	}
	sess.AppendViewImage(data)
	// What the agent is asked to judge has to be scoped, or showing it the
	// picture makes things worse rather than better.
	//
	// The first version said "check it really shows what was asked for". Asked
	// for a specific person, the search returned his photo from his own site —
	// name in the page title, first candidate, a clean hit. The agent looked at
	// it, did not recognize a face it has never seen, took that as a failed
	// check, and sent nothing. A correct result, refused, because the question
	// it was handed was the one nobody can answer by looking.
	//
	// Which is the same trap the vision screen was in: identity is not visible.
	// So the instruction names both halves — what looking settles, and what it
	// cannot — and makes DELIVERING the default, so an unfamiliar face is never
	// mistaken for evidence of a wrong picture.
	return fmt.Sprintf(" LOOK AT IT: the picture is included with this result so you can see it on this round. %s."+
		" Looking settles whether it is the right KIND of thing, whether it is blank, broken or garbled, and whether an edit did what was asked."+
		" Looking does NOT settle WHO someone is: you do not know this person's face, so a face you don't recognize is not evidence of a wrong picture — where it came from is the evidence you have, and you should not overrule it from the pixels."+
		" Deliver it unless you can point at something concretely wrong; if you can, say what that is rather than presenting it as a match.", caveat)
}

// editHandleHint names the handle to EDIT this picture by, and says plainly
// that the image#N one moves.
//
// Both find and fetch used to end with "it is also kept as image#1", which is
// true for exactly as long as it is the newest picture. Search for two people
// to blend and both results say image#1 — the first quietly became image#2 the
// moment the second arrived, and nothing said so. What the model does with two
// identical handles for two different pictures is invent a third naming scheme
// and pass ids that resolve to nothing.
//
// So the filename leads: it means this picture and no other, for as long as the
// file is there. image#N follows, with what it actually means.
func editHandleHint(name, ref string) string {
	return fmt.Sprintf(" To EDIT or blend it rather than send it, pass %q as the image to image(action=\"edit\") — that filename keeps meaning THIS picture. It also entered your recent images as %s, but those are positional: whatever is saved next becomes image#1 and this one moves down, so don't hold onto that number across another image call.", name, ref)
}

// extForMime returns a file extension matching a mime type. Used to
// give saved files plausible suffixes so workspace tools downstream
// can mime-detect them by name when convenient. Covers the formats
// http.DetectContentType emits for common image / video / audio
// types. Unknown mimes fall back to ".bin" rather than empty — the
// filename should always have an extension so the user-facing
// attachment delivery (especially iMessage / SMS) has a meaningful
// suffix.
func extForMime(mime string) string {
	switch {
	case strings.HasPrefix(mime, "image/png"):
		return ".png"
	case strings.HasPrefix(mime, "image/jpeg"):
		return ".jpg"
	case strings.HasPrefix(mime, "image/gif"):
		return ".gif"
	case strings.HasPrefix(mime, "image/webp"):
		return ".webp"
	case strings.HasPrefix(mime, "image/avif"):
		return ".avif"
	case strings.HasPrefix(mime, "image/svg"):
		return ".svg"
	case strings.HasPrefix(mime, "image/bmp"):
		return ".bmp"
	case strings.HasPrefix(mime, "image/heic"), strings.HasPrefix(mime, "image/heif"):
		return ".heic"
	case strings.HasPrefix(mime, "image/tiff"):
		return ".tiff"
	case strings.HasPrefix(mime, "image/"):
		// Unknown image subtype — generic .img fallback. Better
		// than no extension; bridge / browser will still mime-detect
		// from content. Log for diagnosis so we can extend this
		// switch when a new format surfaces in the wild.
		Log("[imagefetch] unknown image mime %q — falling back to .img", mime)
		return ".img"
	case strings.HasPrefix(mime, "video/mp4"):
		return ".mp4"
	case strings.HasPrefix(mime, "video/webm"):
		return ".webm"
	case strings.HasPrefix(mime, "video/"):
		Log("[imagefetch] unknown video mime %q — falling back to .mp4", mime)
		return ".mp4"
	case strings.HasPrefix(mime, "audio/mpeg"):
		return ".mp3"
	case strings.HasPrefix(mime, "audio/wav"), strings.HasPrefix(mime, "audio/x-wav"):
		return ".wav"
	case strings.HasPrefix(mime, "audio/"):
		Log("[imagefetch] unknown audio mime %q — falling back to .m4a", mime)
		return ".m4a"
	default:
		// Non-media content (HTML error page, plaintext error, etc.) —
		// shouldn't happen on a successful image fetch but log so the
		// path is observable when it does.
		Log("[imagefetch] non-media mime %q saved as .bin (likely a fetch error masquerading as success)", mime)
		return ".bin"
	}
}

// shortID returns a brief unique-ish identifier for filenames.
func shortID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
