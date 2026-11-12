package imagefetch

import (
	"fmt"
	_ "image/gif"
	_ "image/png"
	"sort"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	_ "golang.org/x/image/webp" // register the WebP decoder for image.Decode
)

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
	// editors are the subset that take source photos. A ComfyUI or A1111
	// graph belongs to one action (img2img requires its input, txt2img has
	// nowhere to put one); a connector that does both (Gemini) is in both.
	editors []ImageBackendChoice
}

// liveImageActions reads what's configured for this caller. The backend list is
// memoized on the session (ReachableImageBackends), so the DynamicChatTool
// cheapness contract holds across repeated catalog builds.
func liveImageActions(sess *ToolSession) imageActions {
	_, canFind := imageSearchConnector()
	generators, editors := splitBackends(ReachableImageBackends(sess))
	return imageActions{
		kept:         keptRefsFor(sess),
		inboundMedia: sess.InboundMediaCount(),
		find:         canFind,
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

// splitBackends sorts reachable backends into the ones that make a picture
// from text and the ones that take source photos. A connector doing both is
// in both lists.
func splitBackends(all []ImageBackendChoice) (generators, editors []ImageBackendChoice) {
	for _, b := range all {
		if b.Edits {
			editors = append(editors, b)
		}
		if !b.Edits || b.Generates {
			generators = append(generators, b)
		}
	}
	return generators, editors
}

// maxEditImages is the largest source-photo count any reachable editing backend
// accepts — what the `images` param can promise.
// Reports the CASCADE capacity, not the per-call limit: a backend given more
// images than it takes at once now runs them as chained stages, so promising
// only the per-call number would make the model refuse work that succeeds.
func (a imageActions) maxEditImages() int {
	max := 0
	for _, e := range a.editors {
		n := e.CascadeMax
		if n < e.MaxImages {
			n = e.MaxImages
		}
		if n > max {
			max = n
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
	d := "Photos to take people or things FROM: a face, a pet, a product, a place. With edit they go into the picture; with generate a NEW picture is made from them. " +
		"For a picture a TOOL gave you (found, downloaded or generated), use the workspace filename it returned. That names one picture and goes on naming it. " +
		"Use \"media#1\", \"media#2\" ONLY for a photo the USER ATTACHED to their message, numbered in the order they arrived, so media#1 is the first one they sent. Nothing you produced yourself is ever a media#N. " +
		"For a picture from earlier in the conversation whose filename is gone, \"image#1\" is the most recent one either of you produced or received (call action=\"help\" to list them; these numbers SHIFT as new pictures are saved). "
	switch n := a.maxEditImages(); {
	case n > 1:
		// Counts, not a cap. Each compose workflow IS its input count, and the
		// right one is selected from how many you pass — so the model supplies
		// what the request needs and never has to know which connector holds
		// how many inputs.
		d += "This deployment composes " + joinCounts(editorImageCounts(a)) + " pictures at a time, the picture being edited included, and the right backend is chosen automatically from how many you pass: supply exactly what the request needs, all of them in one call. "
	case n == 1:
		// Stated, because the alternative is discovering it by failing: asked to
		// combine three pictures the model picked one, blended nothing, and
		// called it done.
		d += "This backend changes ONE picture at a time and cannot composite several: if the user asks to combine pictures, tell them that rather than choosing one of them. "
	}
	// Said here and not only in the list of recent pictures, which a turn
	// may never ask for: "change it" after two renders went back to the
	// original photo, because every line about renders said not to use one.
	d += "Asked to change a picture you just made, edit that render as picture: going back to the original photo starts over and loses every change since. " +
		"But told it came out WRONG and asked to try again, edit the original picture again, not the wrong render: building on it keeps its mistakes and loses more. "
	d += "A picture you kept under a name is \"image#<that name>\" and stays valid indefinitely. "
	return d + "A web URL is NOT accepted: fetch it first, then pass the saved filename."
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

// canRefineFaces reports whether the face pass could run on any editor here.
//
// It needs two input slots: one for the crop being refined, one for the source
// photo that says who the person is. A one-slot editor can still edit, it just
// cannot be told an identity, so the parameter is withheld rather than offered
// and ignored — an advertised switch that does nothing is the shape a model
// reaches for and then reports as done.
//
// And it runs only where the rendering is gohort's own: a ComfyUI or A1111
// graph (core/image_face_refine.go, on a rest_image connector). A model behind
// a service connector (Gemini, OpenAI) gets one request and returns one
// picture, so no face pass runs there, and offering the switch, or saying
// "rendering, with a pass over the faces", told the agent and the person
// something that never happened.
func (a imageActions) canRefineFaces() bool {
	for _, e := range a.editors {
		if refinesFaces(e) {
			return true
		}
	}
	return false
}

// refinesFaces reports whether the face pass can run on editor e.
func refinesFaces(e ImageBackendChoice) bool { return e.Graph && e.MaxImages >= 2 }

// backendRefinesFaces reports whether the face pass can run on the named
// editor.
func backendRefinesFaces(a imageActions, name string) bool {
	for _, e := range a.editors {
		if strings.EqualFold(e.Name, name) {
			return refinesFaces(e)
		}
	}
	return false
}

// selectableBackends is every backend a caller may name, generators and editors
// together, in ReachableImageBackends' sorted order. One `backend` param covers
// both actions: the model holds one concept ("which backend"), and picking one
// that can't do the requested action is caught at run with an explanation.
// selectableBackends is what a caller may NAME. Generators always: choosing
// between two of them is a real choice about style or model, and nothing else
// can make it.
//
// Editors only when routing cannot decide for itself. A compose backend is
// selected by how many pictures are passed (defaultEditBackend), so listing one
// per count re-offers a choice that was deliberately removed — and invites the
// mistake of naming the three-picture backend for a two-picture job. Where two
// editors share a count, routing genuinely cannot tell them apart and the
// caller has to.
func (a imageActions) selectableBackends() []ImageBackendChoice {
	out := make([]ImageBackendChoice, 0, len(a.backends)+len(a.editors))
	out = append(out, a.backends...)
	perCount := map[int]int{}
	for _, e := range a.editors {
		perCount[e.MaxImages]++
	}
	// A backend that both generates and edits is already listed as a
	// generator: named once.
	listed := map[string]bool{}
	for _, b := range out {
		listed[b.Name] = true
	}
	for _, e := range a.editors {
		if perCount[e.MaxImages] > 1 && !listed[e.Name] {
			out = append(out, e)
		}
	}
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
			if c.Generates {
				// Both, said as both: "works from pictures" alone told the
				// model a backend that also makes pictures from a prompt
				// could not, and it told the person so.
				note += "generates from text alone or from references, and edits a picture you pass"
			} else {
				note += "edits a picture you pass, or makes one from references"
			}
			if c.MaxImages > 1 {
				// Fewer is fine: the unused inputs are taken out of the graph
				// (core BuildComfyBody). It said "needs all", which was true
				// while an unfilled input rendered the workflow's placeholder.
				note += ", composes up to " + strconv.Itoa(c.MaxImages) + " images"
				// More than that is not a refusal any more — it runs as staged
				// calls, each folding the running result into the next batch.
				// Worth saying, or the model caps itself at the per-call number
				// and tells the user the rest cannot be combined.
				if c.CascadeMax > c.MaxImages {
					note += " (pass more and they are combined in stages, up to " + strconv.Itoa(c.CascadeMax) + ")"
				}
			}
		} else {
			note += "generates from text (use with action=generate)"
		}
		if c.Guidance != "" {
			note += " - " + c.Guidance
		}
		notes = append(notes, note)
	}
	if len(notes) > 0 {
		b.WriteString(" Backends: " + strings.Join(notes, " "))
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
		// Two asks, two actions: edit changes a picture that exists (named
		// as picture), generate makes a new one (from text, or from
		// references). They were one action from 2026-09 because a model
		// given both picked generate from text when it should have passed
		// the photo. Generate now takes references, so picking it with a
		// photo in hand still uses the photo; and one action with a list of
		// pictures left a backend to guess which one was being changed: a
		// face swap came back in the reference's shape with a person gone.
	}{{"find", a.find}, {"fetch", a.fetch}, {"generate", a.generate || a.edit}, {"edit", a.edit}} {
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
// ("find_image requires the serper search provider...") that the model had no way
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
	actions := append(append(make([]string, 0, len(names)+4), names...), "help", "keep", "label", "forget")
	desc := "Work with images: single entry point; pick the action matching intent. actions: "
	params := map[string]ToolParam{
		"action": {Type: "string", Enum: actions, Description: strings.Join(actions, " | ") + "."},
	}
	if a.find {
		desc += "find (search the web for a picture/meme/GIF/photo by description and save the best match: use whenever the user wants a picture of something and has no URL), "
		params["query"] = ToolParam{Type: "string", Description: "(find) Description of the image to find (e.g. 'funny cat meme', 'golden gate bridge sunset', 'surprised pikachu')."}
	}
	if a.fetch {
		desc += "fetch (download a specific image URL you already have), "
		params["url"] = ToolParam{Type: "string", Description: "(fetch) Direct URL of the image to download (must resolve to an image file: jpg, png, gif, webp, etc.)."}
	}
	if a.generate || a.edit {
		// The two asks, each said as what it is for. Edit's line carries the
		// rule that decides between them: a request about a picture that
		// exists is an edit of it.
		editLine := "edit (change a picture that EXISTS: name it as `picture`, say in `prompt` what to change in it, and pass in `references` any photos to take a face or a thing from. It keeps the picture as it is apart from the change. A request about a picture that already exists, \"this photo\", \"the one you just made\", is always an edit of it), "
		switch {
		case a.generate && a.edit:
			desc += "generate (make a NEW picture: from a text prompt alone, or from reference photos you pass in `references`, taking from them the people or things the request names), " + editLine
		case a.edit:
			// Editors only. Saying "from a text prompt alone" here would be a
			// lie about this deployment, and the model would discover it by
			// failing.
			desc += "generate (make a NEW picture from reference photos you pass in `references`: this deployment cannot create from a text prompt alone, and a request with nothing to work from cannot be served), " + editLine
		default:
			desc += "generate (create a NEW image from a text prompt), "
		}
	}
	// One `prompt`, described for every action that reads it. It used to be
	// added only for generate and labelled "(generate)", so on an edit the
	// model treated it as another action's field and left it out — and a blend
	// with no instructions is the backend's guess at what you wanted, not
	// yours. Say what it means for each.
	if a.generate || a.edit {
		// One prompt, described once. It used to be labelled "(generate)" and
		// so was skipped on edits, and a blend with no instructions is the
		// backend's guess rather than the request.
		d := "What you want. For a new picture from no photos: a detailed description of the picture to create."
		if a.edit {
			d += " For an edit: what should CHANGE in the picture. For a new picture from references: the scene to make, and which reference is which. Give one whenever you pass pictures, unless you genuinely want the backend's default treatment." +
				" POINT AT THE PICTURES, DO NOT REDESCRIBE THEM. Name the part you mean and which image it comes from: \"the face from the first picture on the body in the second\", \"the background of the second, everything else from the first\", \"make it snowy\"." +
				" That is the instruction, and a combine that does not say which part comes from where is the backend's guess." +
				" What must NOT go in is who the subject IS or what they LOOK like: no names, no \"a man with a short beard\", no borrowing the wording of a caption." +
				"The picture already carries all of that (it is why you passed it), and appearance words compete with it. The renderer draws the words, so \"Rory, a man with a short beard, on a beach\" yields a stranger with a beard next to nothing you supplied," +
				" where \"on a beach at sunset\" keeps the real person. A name is worse than useless: it means nothing to the renderer at best, and at worst pulls in whoever it thinks that name looks like." +
				"So refer to people and things by WHERE THEY ARE (\"the person in the first image\", \"the animal on the left\"), never by name and never by description."
		}
		params["prompt"] = ToolParam{Type: "string", Description: d}
		// Declaring a SET up front. One call makes one picture and only one
		// render runs at a time, so "I'll do you a few variations" needs the
		// count to survive the gap between them — the tool is what carries it,
		// and this is where it is told. Without it the model made one picture,
		// was refused when it tried to start the second in the same turn, and
		// the promise went unkept.
		// A pass that is a step toward a later one. Each background render is
		// sent on its own, so a four-picture blend on a two-picture editor
		// sent the person two in-between pictures before the one they asked
		// for.
		params["deliver"] = ToolParam{
			Type: "boolean",
			Description: "Defaults to TRUE. Set FALSE on a pass that is only a step toward a later one: when you blend more pictures than one render takes, every pass but the last. " +
				"A held pass is kept under its id and NOT sent; you pass that id to the next pass, and only the last pass, left at the default, is sent to the person. Never hold a picture the person asked to see.",
		}
		params["variations"] = ToolParam{
			Type: "integer",
			Description: "How many pictures you are going to make IN TOTAL, when you are making a set: variations on one idea, a few options to pick from. Say it on the FIRST call only; it carries from there. " +
				"They render one at a time: you get this one back, deliver it, and are told to start the next. Leave it out for a single picture. " +
				"Whenever you tell the user you will make several, put the number here: otherwise you get one, and a second call in the same turn is refused.",
		}
	}
	if a.edit {
		params["picture"] = ToolParam{Type: "string",
			Description: "(edit) The ONE picture to change, by id (the same forms as references). The result keeps it as it is apart from the change: its framing, its shape, and everyone in it."}
		params["references"] = ToolParam{
			Type:        "array",
			Items:       &ToolParam{Type: "string"},
			Description: a.imagesParamDesc(),
		}
		if a.anyEditorTakesMask() {
			params["mask"] = ToolParam{Type: "string", Description: "Optional black-and-white mask image (the same forms as picture) marking WHICH PART of the picture to change. White = repaint, black = keep. Use for \"change just the sky\"."}
		}
		if a.canRefineFaces() {
			params["preserve_faces"] = ToolParam{Type: "boolean", Description: "Defaults to TRUE and you rarely need to send it. When a source picture shows a person, the edit runs a second pass over the face so the result still looks like them: an edit that redraws the whole scene renders the face too small to keep a likeness. Set FALSE only when the instruction is deliberately ABOUT the face and must be free to change it: ageing someone, changing their expression, making them somebody else, or turning them into a statue, a cartoon, an animal. Setting it false on an ordinary edit costs you the likeness; leaving it true on a face-changing one fights the change you asked for. With checking quick it is off unless you send it."}
		}
		params["checking"] = ToolParam{Type: "string", Enum: []string{ImageCheckQuick, ImageCheckStandard, ImageCheckThorough},
			Description: "Leave it out unless the person asks for faster or more careful pictures; it defaults to the deployment's setting (" + ImageChecking() + "). " +
				"How much an edit from source pictures is checked, and how many pictures the same sources may give: quick (no face pass, no separate check, 1 picture), " +
				"standard (face pass and a quick check, 2), thorough (face pass and a careful check, about 15 seconds more, 3)."}
	}
	if len(a.backendNames()) > 1 {
		params["backend"] = ToolParam{Type: "string", Enum: a.backendNames(), Description: a.backendParamDesc()}
	}
	params["name"] = ToolParam{Type: "string", Description: "(keep/label/forget) Short stable name for a reference image you want to still have later: \"brand_mark\", \"house_style\". Letters, digits, - and _; not a bare number. Reuse the same name to replace what it points at."}
	params["ref"] = ToolParam{Type: "string", Description: "(keep) Which picture to keep, as an image id. Defaults to image#1, the most recent one, so keeping what you just made needs only a name."}
	params["note"] = ToolParam{Type: "string", Description: "(keep) Optional: why you are keeping it, in one line. Stored with the image and recalled with it later, so write what a future you would need to decide whether this is the right picture."}
	params["of"] = ToolParam{Type: "string", Description: "(keep/label) Who or what the picture is OF: \"Rory\", \"my dog Bess\", \"the office\". Set this whenever the picture shows a specific subject: it is what lets a later request that NAMES that subject find this picture instead of guessing between filenames. For a person, use the name they go by in this conversation, spelled the way you have seen it."}
	params["is_person"] = ToolParam{Type: "boolean", Description: "(keep/label) True when \"of\" is a person. A picture of a person is what you must work from when asked to depict them, so these are listed separately and are the only ones offered as a likeness."}
	// Gloss it. A bare "help." reads as boilerplate every schema carries, and
	// what this one actually does — name the pictures that are still reachable —
	// is the answer to the question a stalled edit is asking.
	desc += "help (list the pictures you can still reference, by id, with what each one is), "
	desc += "keep (save a picture under a NAME so it survives, recent ids shift as new pictures arrive and eventually drop, a kept one answers to image#<name> indefinitely; use it for a reference you expect to want again: a person's face, a logo, a style sample, a chart to match. Say who or what it shows with \"of\", and is_person=true for a person, so a later request that names them finds it), "
	desc += "label (say who or what an ALREADY-kept image shows, without re-keeping it: use this the moment you notice a kept picture whose subject nobody recorded; an unlabelled picture of a person is one you will end up describing in words instead of passing), "
	desc += "forget (drop a kept image by name). "
	desc += "Each saves into your session workspace and returns the path: it does NOT deliver; follow up with workspace(action=\"attach\", path=...) to ship the file."
	// Omitting an action hides that the capability EXISTS, and that cuts both
	// ways. For find, fetch is a fair substitute and silence costs nothing. For
	// edit it is actively harmful: asked to blend two pictures with no editing
	// backend wired, a model has no idea blending was ever a thing, so it writes
	// a prompt describing the combination and generates a NEW image — handing
	// back something that looks like an answer and isn't. Say the capability is
	// missing so it can say so too.
	if a.generate && !a.edit {
		desc += " NOTE: this deployment can only CREATE images from text, nothing here can modify, blend, or combine existing pictures. If the user asks you to edit, blend, composite, or change a photo, tell them image editing is not configured. Do NOT write a prompt describing the combination and generate a new image instead: that is a different picture, not their photo, and presenting it as the edit is worse than saying it can't be done."
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
		desc += ". If the request is to change one, EDIT it, with that id as picture; to put who or what is in them into a new picture, pass them in references. That is what makes the result their picture rather than a new one that ignores it." +
			"If one shows a subject you could be asked for again (a person, a pet, a product, a place), keep it NOW" +
			" (action=\"keep\", ref=\"media#1\", name=\"...\", of=\"who or what it shows\", and is_person=true for a person):" +
			" a media id lasts only this turn and ring ids age out as new pictures arrive, whereas a kept name works indefinitely." +
			" Set \"of\": a kept picture with no subject is one you will later have to identify by guessing at its filename," +
			" and a face you cannot identify is one you will invent instead. Asking the user to re-send a photo they already sent is the other failure this avoids."
	}
	// The reference library, named before the action is chosen. Without this the
	// library is reachable only by calling help, which the model has no reason
	// to do — so a request naming a subject it HAS a reference for was answered
	// by inventing a fresh one that looks like somebody else.
	if a.edit && len(a.kept) > 0 {
		desc += " You have reference images kept under stable ids: " + describeKeptRefs(a.kept) +
			". If a request names a subject you hold a reference for, pass that id in references" +
			", generating instead invents a DIFFERENT-looking subject, which is rarely what was asked for."
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
		// Three clauses used to live here, arguing the model out of picking
		// generate over edit: an ordering rule, a phrasings list, and a
		// "generate is almost never right" warning. All of it was compensating
		// for a choice that no longer exists — there is one render action, and
		// the question is only which picture the request is about.
		//
		// What remains is that question, stated once.
		if a.edit {
			desc += " Wants a picture and one already exists that the request is ABOUT (\"this photo\", \"the one you just made\", \"swap the guy on the left\") → edit it, as picture. A NEW picture of people or things you hold photos of (\"make x sit in y\", \"combine these\", \"the four of us on a stage\") → generate with those photos in references."
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
			desc += "A GENERIC subject (a dog, a mountain, a businessman, a house), has no particular thing to find a picture OF, so generate it directly and do not search first."
		}
		if a.generate {
			desc += "A REAL, specific subject (a named person, someone the user knows, a particular place, product or logo), is depicted FROM A PICTURE OF IT, never from a text prompt: generation invents a likeness, which for a real person is simply somebody else's face."
			switch {
			case a.edit && a.find:
				desc += "Use the best picture you have of them (one kept, or one that arrived with the request), and if you have none, find one first and work from that." +
					// Without this the rule dead-ends. Told to find a reference
					// and finding nothing, the model either stalls or quietly
					// generates anyway — and an invented likeness delivered
					// without comment is a picture of somebody else presented
					// as the person who was asked for.
					" If the search turns up nothing usable, generate it and SAY you could not find a reference, so nobody takes the likeness for the real person."
			case a.edit:
				desc += "Use the best picture you have of them (one kept, or one that arrived with the request), and if you have none, ask for one rather than inventing it."
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
				desc += " A reference you hold is used by DEFAULT: the person who sent it assumes it will be, so do not ask whether to use their picture and do not offer generating from scratch as an alternative. Use it, then say which reference you worked from."
				desc += " A scene with several real subjects uses every reference you have and invents only the rest: pass the pictures you hold, name in the prompt which reference is which person, and describe ONLY the ones you have no picture of. Never drop a reference because the set is incomplete: one real face beside one invented is strictly better than two invented, and a group photo counts as a reference for each person in it."
				// Describing a subject you also passed a picture of gives the
				// backend two sources for one face and invites it to draw from
				// the words. Say what they DO, not what they look like.
				desc += " Do NOT describe the appearance of someone you passed a picture of: say what they are doing, wearing or where they are, and let the picture carry the likeness. Words about a face compete with the reference rather than reinforcing it."
				// Several people go in with ONE call carrying every person's
				// picture. Placeholder passes (a scene of stand-ins, then one
				// swap per pass) were tried and did worse on live use: each
				// pass redraws the whole picture, so a face placed earlier was
				// lost and the picture degraded pass by pass, and a chain whose
				// check kept failing ran on. Observed: the single call with the
				// scene and all four people came back with four; the passes
				// after it alternated between three and four.
				// People only: a blend of two ships that lost one was the edit
				// model failing to draw it from a small reference, not the prompt's
				// words, and nothing showed passes would have saved it.
				desc += " Putting SEVERAL real people into one scene goes in ONE call: with a scene to put them in, edit it as picture with every person's photo in references; with none, generate with every person's photo in references. Name in the prompt which photo is which person and where each one stands. Do NOT build the scene up one person at a time: every pass redraws the whole picture, so a long chain of passes loses more than it fixes. Look at the result. If one person did not come through, make at most one fix pass: edit that result as picture, with EVERY person's photo in references again, saying who to fix and that everyone else stays as they are."
			}
		}
	}
	return imageSchema{desc: desc, params: params}
}
