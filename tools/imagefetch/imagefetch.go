// Package imagefetch provides tools for fetching, finding, and generating images
// and collecting them for delivery (e.g. as iMessage attachments).
package imagefetch

import (
	"bytes"
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
		find:  cfg.Provider == "serper" && cfg.APIKey != "",
		fetch: true,
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
	// media#N comes FIRST and the two are distinguished by WHEN, not by
	// recency. Describing image#N as "a recent image" made it the obvious pick
	// for "blend the two photos I just sent" — and it resolved to the last two
	// pictures the assistant had generated, which is a confidently wrong answer
	// rather than an error.
	d := "(edit) Source image(s) to change, as references. " +
		"If the user just sent photos on THIS turn, use \"media#1\", \"media#2\" — numbered in the order they arrived, so media#1 is the first one they sent. " +
		"For a picture from EARLIER — one you generated, found, downloaded, or that arrived on a previous turn — use \"image#1\" (newest first; call action=\"help\" to list them). " +
		"A workspace filename also works. "
	if n := a.maxEditImages(); n > 1 {
		d += "Up to " + strconv.Itoa(n) + ". ORDER MATTERS: the first is the base/subject, later ones composite onto it. "
	}
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
				note += ", takes up to " + strconv.Itoa(c.MaxImages) + " images"
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
	desc := "Work with images — single entry point; pick the action matching intent. actions: "
	params := map[string]ToolParam{
		"action": {Type: "string", Enum: names, Description: strings.Join(names, " | ") + "."},
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
		params["prompt"] = ToolParam{Type: "string", Description: "(generate) Detailed description of the image to create."}
	}
	if a.edit {
		desc += "edit (change an EXISTING photo, or combine several — retouch, restyle, replace a background, composite; needs source image(s), not a blank canvas), "
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
	desc += "help. Each saves into your session workspace and returns the path — it does NOT deliver; follow up with workspace(action=\"attach\", path=..., cleanup=true) to ship the file."
	// The decision rule only helps when there's a decision to make.
	if len(names) > 1 {
		desc += " Decision:"
		if a.find {
			desc += " wants a picture of something, no URL → find."
		}
		if a.fetch {
			desc += " Gave an image URL → fetch."
		}
		if a.generate {
			desc += " Wants something drawn / created / imagined → generate."
		}
		if a.edit {
			desc += " Points at an EXISTING picture (\"this photo\", \"the one you just made\", \"combine these\") → edit."
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
		if !avail.edit {
			return "", fmt.Errorf("the edit action is unavailable — no image backend here is wired for image input (img2img / inpaint). Tell the user editing isn't set up; do NOT retry")
		}
		return editImage(sess, args, avail)
	case "", "help":
		help := "image actions: " + strings.Join(avail.names(), " | ") + ". Each saves to your workspace and returns the path; deliver with workspace(action=\"attach\", path=...)."
		if m := RecentImageManifest(sess); m != "" {
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
		if ref := RecordRecentImage(sess, data, "found: "+truncate(query, 60)); ref != "" {
			msg += fmt.Sprintf(" It is also kept as %s — pass that to image(action=\"edit\") to change it.", ref)
		}
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
	var bestData []byte
	var bestMeta SerperImageResult
	bestScore := -1
	usable := 0
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
		// No vision configured → can't screen the pixels; take the first
		// text-matching result (or the first usable one at all).
		if sess.LLM == nil {
			if textMatch || bestScore < 0 {
				return saveAndReturn(data, r)
			}
			continue
		}
		score := scoreImageMatch(sess, data, query)
		Log("[imagefetch/find_image] query=%q candidate %d (title %q) text=%v vision=%d/100", query, usable, r.Title, textMatch, score)
		if textMatch && score >= imageMatchThreshold {
			return saveAndReturn(data, r) // confident match — stop here
		}
		if score > bestScore {
			bestData, bestMeta, bestScore = data, r, score
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
						bestData, bestMeta, bestScore = rdata, r, rscore
					}
				}
			} else {
				Log("[imagefetch/find_image] query=%q browser render failed for %q: %v", query, r.Link, rerr)
			}
		}
	}
	// Nothing both text- and vision-matched. Use the best vision match if it's
	// a confident depiction; otherwise reject rather than return a wrong image.
	if bestScore >= imageMatchThreshold {
		Log("[imagefetch/find_image] query=%q no text+vision match; using best vision match %d/100", query, bestScore)
		return saveAndReturn(bestData, bestMeta)
	}
	if bestScore < 0 {
		return "", fmt.Errorf("could not download any usable image for %q (sources may be blocking the fetch)", query)
	}
	return "", fmt.Errorf("found image(s) for %q but none clearly depict it (best visual match %d/100) — the search may have surfaced lookalikes or unrelated results; refine the query, or use fetch_image with a specific image URL", query, bestScore)
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
	return generateImageInto(sess, StringArg(args, "prompt"), backend)
}

// editImage runs the edit action: resolve the backend, hand the caller's image
// references to the connector (which verifies and uploads them), and save the
// result the same way generation does.
func editImage(sess *ToolSession, args map[string]any, avail imageActions) (string, error) {
	prompt := strings.TrimSpace(StringArg(args, "prompt"))
	refs := stringsArg(args, "images")
	if len(refs) == 0 {
		hint := "pass the image to change"
		if m := RecentImageManifest(sess); m != "" {
			hint = m
		}
		return "", fmt.Errorf("edit needs at least one source image — %s", hint)
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
			return "", fmt.Errorf("no image backend here can edit photos")
		}
		return "", fmt.Errorf("image backend %q generates from text and can't edit a photo — use one of: %s, or switch to action=\"generate\"", backend, strings.Join(editorNames(avail), ", "))
	}
	// Some editing workflows have no text node at all — a blend or an upscale is
	// pure pixel work. Demanding a prompt there makes the model invent one that
	// goes nowhere.
	if prompt == "" && backendNeedsPrompt(avail, backend) {
		return "", fmt.Errorf("prompt is required for this backend — describe what should CHANGE (e.g. \"make it snowy\", \"put the subject on a beach\")")
	}
	// ENFORCEMENT, same as generate: the enum is a hint, this is the boundary.
	if !ImageBackendReachable(sess, backend) {
		return "", fmt.Errorf("image backend %q is not available to you — use one of: %s", backend, strings.Join(editorNames(avail), ", "))
	}
	result, err := EditImageWithBackend(sess, EditImageRequest{
		Backend: backend,
		Prompt:  prompt,
		Images:  refs,
		Mask:    StringArg(args, "mask"),
	})
	if err != nil {
		return "", fmt.Errorf("image edit via %q failed: %w", backend, err)
	}
	return saveImageResult(sess, result, "edit", "edited "+strings.Join(refs, "+")+": "+truncate(prompt, 60))
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
	return saveImageResult(sess, result, "gen", "generated: "+truncate(prompt, 60))
}

// saveImageResult lands a finished image in both places it needs to be: the
// session workspace (so workspace(attach) can deliver it) and the image space
// (so a LATER turn can edit it by id).
//
// The image space is what replaced telling the model to clean up after itself.
// It keeps a bounded ring and prunes on write, so the reply no longer has to
// push cleanup=true — the file is retained on purpose and named something the
// model can actually refer back to.
func saveImageResult(sess *ToolSession, result *ImageGenResult, prefix, note string) (string, error) {
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

	msg := fmt.Sprintf("Stored at %q (%d bytes). This is normally meant for delivery — call workspace(action=\"attach\", path=%q) and then write a short line describing it. (Skip the attach only if the user explicitly asked you NOT to send it — rare.)", name, len(data), name)
	if ref := RecordRecentImage(sess, data, note); ref != "" {
		msg += fmt.Sprintf(" It is also kept as %s: pass that to image(action=\"edit\") later to change it. Do NOT delete it — recent images are pruned automatically.", ref)
	}
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

// pageMentionsSubject reports whether a page (already lowercased) references
// the search subject — every significant query word for short queries, a
// ~60% majority for longer ones. An uncheckable query (no significant words)
// passes so it never blocks the result.
func pageMentionsSubject(pageLower, query string) bool {
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
	if ref := RecordRecentImage(sess, data, "downloaded: "+truncate(rawURL, 60)); ref != "" {
		msg += fmt.Sprintf(" It is also kept as %s — pass that to image(action=\"edit\") to change it.", ref)
	}
	return msg, nil
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
