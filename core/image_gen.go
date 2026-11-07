package core

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/cmcoffee/gohort/core/ollama"
)

// --- pluggable image backends ------------------------------------------------
//
// Every picture comes from a connector. A rest_image connector or a service
// giving image.generate registers itself here so the SAME native pipeline —
// the image tool, writer-app illustrations (GenerateImage), and the admin
// image-provider setting — can target it by name, with no per-service code.
// Gemini and OpenAI are templates of the service kind, not code here.

// ImageBackendFunc produces an image for a named backend. landscape asks for
// a wide picture rather than a square one.
type ImageBackendFunc func(ctx context.Context, prompt string, landscape bool) (*ImageGenResult, error)

var (
	imageBackendMu sync.RWMutex
	imageBackends  = map[string]ImageBackendFunc{}
)

// RegisterImageBackend installs (or replaces) a named image backend. Idempotent:
// re-registering the same name (e.g. a connector re-materialize on restart) just
// refreshes the closure.
func RegisterImageBackend(name string, fn ImageBackendFunc) {
	name = strings.TrimSpace(name)
	if name == "" || fn == nil {
		return
	}
	imageBackendMu.Lock()
	imageBackends[name] = fn
	imageBackendMu.Unlock()
}

// UnregisterImageBackend drops a named backend. Teardown used to be a no-op
// here, so an unapproved or deleted connector kept a live closure for the rest
// of the process — ImageBackendRegistered still said yes, and the admin's
// image-provider picker went on offering a backend that no longer existed.
func UnregisterImageBackend(name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	imageBackendMu.Lock()
	delete(imageBackends, name)
	imageBackendMu.Unlock()
}

// ImageEditFunc edits one picture for a named backend of another kind than
// rest_image (a service, core/service): the prompt and the source picture's
// verified bytes in, the edited picture out.
type ImageEditFunc func(sess *ToolSession, prompt string, image []byte) (*ImageGenResult, error)

var imageEditBackends = map[string]ImageEditFunc{}

// RegisterImageEditBackend installs (or replaces) a named edit backend, or
// with fn nil drops it.
func RegisterImageEditBackend(name string, fn ImageEditFunc) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	imageBackendMu.Lock()
	defer imageBackendMu.Unlock()
	if fn == nil {
		delete(imageEditBackends, name)
		return
	}
	imageEditBackends[name] = fn
}

func lookupImageEditBackend(name string) (ImageEditFunc, bool) {
	imageBackendMu.RLock()
	defer imageBackendMu.RUnlock()
	fn, ok := imageEditBackends[strings.TrimSpace(name)]
	return fn, ok
}

// ImageBackendRegistered reports whether a named backend exists — lets the admin
// UI show approved connector backends as image-provider choices.
func ImageBackendRegistered(name string) bool {
	imageBackendMu.RLock()
	defer imageBackendMu.RUnlock()
	_, ok := imageBackends[strings.TrimSpace(name)]
	return ok
}

func lookupImageBackend(name string) (ImageBackendFunc, bool) {
	imageBackendMu.RLock()
	defer imageBackendMu.RUnlock()
	fn, ok := imageBackends[strings.TrimSpace(name)]
	return fn, ok
}

// ImageDir returns the directory where generated images are stored.
// Defaults to os.TempDir()/gohort-images if not set via SetImageDir.
func ImageDir() string {
	if imageDir != "" {
		return imageDir
	}
	return filepath.Join(os.TempDir(), "gohort-images")
}

// SetImageDir configures the persistent image storage directory.
func SetImageDir(dir string) {
	imageDir = dir
	os.MkdirAll(dir, 0755)
}

var imageDir string

// BrowserDir returns the directory where go-rod stores its Chromium binary and
// user data. Defaults to os.TempDir()/gohort-browser if not set via SetBrowserDir.
func BrowserDir() string {
	if browserDir != "" {
		return browserDir
	}
	return filepath.Join(os.TempDir(), "gohort-browser")
}

// SetBrowserDir configures the directory used for Chromium binary caching.
func SetBrowserDir(dir string) {
	browserDir = dir
	os.MkdirAll(dir, 0755)
}

var browserDir string

// ImageProviderFunc returns the configured image backend: a connector's
// name, or "" or "none" for off. Set by the main package.
var ImageProviderFunc func() string

// ImageGenerationAvailable reports whether a default image backend is set.
func ImageGenerationAvailable() bool {
	return defaultImageBackend() != ""
}

// defaultImageBackend is the configured backend, "" when none is.
func defaultImageBackend() string {
	if ImageProviderFunc == nil {
		return ""
	}
	if p := strings.TrimSpace(ImageProviderFunc()); p != "none" {
		return p
	}
	return ""
}

// generateImageChatTool is no longer registered — the canonical
// generate_image tool lives in tools/imagefetch/. The struct +
// methods remain in case private code references them directly,
// but there's no global registration to compete with the
// imagefetch version.
func init() {
	// Tier-2 auto-retry knobs. The soft budget drives how hard the tool nudges
	// the model to regenerate a checkable miss; the hard cap is a runaway guard
	// set well above it so genuine multi-image turns ("make me 4 logos") aren't
	// blocked. Both are per-turn (the attempt counter is session-scoped).
	RegisterTunable(TunableSpec{Key: "tune_image_max_regens", Category: "Images", Label: "Image auto-regeneration budget", Help: "How many times the model may regenerate an image to fix a CHECKABLE miss.",
		Detail: "A checkable miss is a specific count, a named object, or required text. After the budget it must deliver the best result. 0 disables auto-retry: the model still SEES the image (Tier 1) but is told to caveat rather than retry.", Kind: KindInt, Default: 2, Min: 0, Max: 5})
	RegisterTunable(TunableSpec{Key: "tune_image_gen_hard_cap", Category: "Images", Label: "Image generations per turn (hard cap)", Help: "Absolute ceiling on generate_image calls in one turn: a runaway guard, not the retry budget.",
		Detail: "Set it well above the auto-regeneration budget, so legitimate multi-image requests still work.", Kind: KindInt, Default: 10, Min: 1, Max: 50})
	// Whether a render waits or goes to the background. On, the conversation is
	// never held open by a picture: the reply comes straight back and each
	// finished image arrives as its own message. Off, the generic duration rule
	// applies and only renders slower than the detach threshold go background —
	// which for a twenty-second generate means never.
	RegisterTunable(TunableSpec{Key: "tune_image_always_detach", Category: "Images",
		Label:  "Run image renders in the background",
		Help:   "Send every image generate and edit to the background instead of holding the turn open.",
		Detail: "The agent answers immediately and each finished picture arrives as its own message, so a set of variations is delivered one at a time rather than after the whole batch. Turn it off to keep renders inline unless they are slower than the detach threshold.",
		Kind:   KindBool, Default: 1, Min: 0, Max: 1})
}

// imageMaxRegens / imageGenHardCap read the Tier-2 knobs with safe fallbacks.
func imageMaxRegens() int {
	n := TuneInt("tune_image_max_regens")
	if n < 0 {
		return 0
	}
	return n
}

// ImageGenHardCap is the absolute number of renders one turn may run.
//
// Exported because the tool it was written for is no longer registered. The cap
// and its counter lived on generateImageChatTool, the grouped `image` tool in
// tools/imagefetch replaced it, and the ceiling did not come along — so the
// live render path had no runaway guard at all.
//
// What that cost: a request for "a few variations" rendered EIGHTEEN pictures
// across seven minutes in a single turn, delivered five of them, and told the
// user all eighteen were attached. Nothing in the loop was checking a number,
// because the only thing that checked one had been unregistered for months.
func ImageGenHardCap() int {
	if n := TuneInt("tune_image_gen_hard_cap"); n >= 1 {
		return n
	}
	return 10
}

// imageVerifyText is the attempt-aware tool result: it puts the checkable-criteria
// GATE in front of the model (which alone can judge whether the request has
// objective criteria and whether they were met) and tracks the remaining
// regeneration budget so the retry chain is bounded.
func imageVerifyText(attempt, maxRegens int) string {
	const base = "IMAGE:generated. The image is shown to you now, verify it against what the user EXPLICITLY asked for: a specific count, a named object, or required/visible text. "
	const tail = " Do not describe or try to open a file path."
	if maxRegens <= 0 {
		return base + "If it misses an explicit requirement, tell the user plainly; otherwise deliver it." + tail
	}
	if regensLeft := maxRegens - (attempt - 1); regensLeft > 0 {
		return base + fmt.Sprintf("If it FAILS one of those checkable requirements, call generate_image again with a CORRECTED prompt that fixes the specific miss (do not repeat the same prompt) and refine_of_previous=true: %d regeneration(s) left. Regenerate ONLY for such explicit requirements, never for aesthetic or stylistic preference. Otherwise deliver it.", regensLeft) + tail
	}
	return base + "The auto-regeneration budget for this image is spent: do NOT call generate_image again to retry it. Deliver this result; if it still falls short of an explicit requirement, tell the user plainly rather than retrying." + tail
}

type generateImageChatTool struct{}

func (t *generateImageChatTool) Name() string { return "generate_image" }
func (t *generateImageChatTool) Desc() string {
	return "Generate a NEW image from a text description via the configured image-generation backend and return the image URL. USE ONLY when the user explicitly asks to CREATE, DRAW, MAKE, or GENERATE a fresh image: e.g. \"draw me a dragon\", \"create a logo\", \"generate an illustration\". DO NOT use this for finding existing images (use find_image), downloading from a known URL (use fetch_image), or capturing a webpage (use screenshot_page). For real-world reference images, generation is the wrong tool: it produces invented content, not real photos."
}
func (t *generateImageChatTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"prompt":             {Type: "string", Description: "A detailed description of the image to generate."},
		"refine_of_previous": {Type: "boolean", Description: "Set true ONLY when this call regenerates the PREVIOUS image to fix a specific checkable miss (a failed count, named object, or required text). Leave false or omit for a new, distinct image, that starts a fresh regeneration budget."},
	}
}
func (t *generateImageChatTool) Run(args map[string]any) (string, error) {
	if !ImageGenerationAvailable() {
		return "", fmt.Errorf("image generation is not configured")
	}
	prompt, _ := args["prompt"].(string)
	if prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}
	result, err := GenerateImage(context.Background(), prompt)
	if err != nil {
		return "", err
	}
	// Return a short reference instead of embedding the full base64 payload
	// — that would bloat every future LLM request with megabytes of image data.
	if strings.HasPrefix(result.URL, "http://") || strings.HasPrefix(result.URL, "https://") {
		return "IMAGE:" + result.URL, nil
	}
	os.Remove(result.URL)
	return "IMAGE:generated (local file: " + result.URL + ")", nil
}

// RunWithSession generates an image, delivers it to the user, and shows it to the
// LLM to self-verify (Tier 1). Tier 2 bounds the retry chain: a per-turn attempt
// counter drives an escalating, checkable-criteria-gated instruction and a hard
// runaway cap. Returns a short reference to avoid bloating LLM context with base64.
func (t *generateImageChatTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if !ImageGenerationAvailable() {
		return "", fmt.Errorf("image generation is not configured")
	}
	prompt, _ := args["prompt"].(string)
	if prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}
	// Tier-2 accounting. Count this call BEFORE generating so the runaway guard
	// can refuse without burning a diffusion run. `refine` continues the retry
	// chain (bounded budget); a new subject resets the chain but the absolute
	// `total` still climbs, so the hard cap can't be evaded by mislabeling.
	refine, _ := args["refine_of_previous"].(bool)
	attempt, total := sess.NextImageAttempt(refine)
	if hardCap := ImageGenHardCap(); total > hardCap {
		return "", fmt.Errorf("image generation limit reached for this turn (%d calls): deliver the best result so far or tell the user what fell short instead of regenerating again", hardCap)
	}
	// On a refine (chain length > 1), tell the user the wait is deliberate
	// (nil-safe: apps without live status just ignore it).
	if attempt > 1 && sess.StatusCallback != nil {
		sess.StatusCallback("Refining the image to better match your request…")
	}

	result, err := GenerateImage(context.Background(), prompt)
	if err != nil {
		return "", err
	}
	// For HTTP URLs (a backend that answers with an address), return the URL — no local bytes to show or verify.
	if strings.HasPrefix(result.URL, "http://") || strings.HasPrefix(result.URL, "https://") {
		return "IMAGE:" + result.URL, nil
	}
	// For local files (self-hosted ComfyUI / Gemini), read the bytes once and use
	// them for two independent channels: AppendImage delivers the image to the
	// USER (outbox), and AppendViewImage shows it to the LLM on its next round so
	// it can VERIFY the result matches the request before presenting it. The bytes
	// never bloat persisted history — the vision message is injected for that one
	// round and drained. We do NOT echo the file path: it's removed immediately
	// below, and a stale path only invites the model to hunt for a file that no
	// longer exists (see connector_restimage note).
	data, err := os.ReadFile(result.URL)
	if err == nil {
		sess.AppendImage(base64.StdEncoding.EncodeToString(data)) // → user (outbox)
		// Labelled with the prompt it came from: a round can hold several
		// generations at once ("three ducks"), and they finish in whatever order
		// the backend returns them, not the order they were asked for.
		sess.AppendViewImageAs(data, "the image you generated for: "+truncateRunes(prompt, 120)) // → LLM (self-verify)
	}
	os.Remove(result.URL)
	if err != nil {
		return "IMAGE:generated", nil
	}
	return imageVerifyText(attempt, imageMaxRegens()), nil
}

func (t *generateImageChatTool) IsInternetTool() bool { return true }

// ImageGenResult holds the output of an image generation call.
type ImageGenResult struct {
	URL    string // URL or local path to the generated image
	Prompt string // the prompt that was used
}

// generateWithBackend renders through a registered backend: a connector's,
// by its name. Every image comes from one; none is written in Go.
func generateWithBackend(ctx context.Context, backend, prompt string, landscape bool) (*ImageGenResult, error) {
	if backend == "" {
		return nil, fmt.Errorf("image generation is not set up: choose an image connector under Admin > Services > Image Generation")
	}
	fn, ok := lookupImageBackend(backend)
	if !ok {
		return nil, fmt.Errorf("no approved image connector named %s", backend)
	}
	result, err := fn(ctx, prompt, landscape)
	if err == nil && result != nil {
		ProcessUsage().AddImageCall()
		if t := RequestUsage(ctx); t != nil {
			t.AddImageCall()
		}
	}
	return result, err
}

// GenerateImage generates a wide 16:9 image through the configured backend.
func GenerateImage(ctx context.Context, prompt string) (*ImageGenResult, error) {
	return GenerateImageWithBackend(ctx, "", prompt, true)
}

// renderCallerKey labels a render for the queue, the way WithEmbedCaller does
// for embeddings. A separate key because the two queues are separate and one
// request can be in both.
type renderCallerKey struct{}

// WithRenderCaller labels renders made under ctx so the image scheduler can
// round-robin between them and everyone else. Without a label every render
// looks like the same caller and fair-share has nothing to be fair between — a
// peer rendering in a loop would take the GPU as surely as if there were no
// queue at all.
func WithRenderCaller(ctx context.Context, caller string) context.Context {
	if caller = strings.TrimSpace(caller); caller == "" {
		return ctx
	}
	return context.WithValue(ctx, renderCallerKey{}, caller)
}

// imageRenderYieldKey is the LLMTable setting for whether background renders
// wait for people's: "" (the default) yes, "never" first come, first served.
const imageRenderYieldKey = "image_render_yield"

// rendersYield says whether a background render (a schedule's, a monitor's)
// goes behind a render a person is waiting on. On by default; an operator
// whose renders are all equally urgent, or who runs them on hardware with
// room to spare, turns it off.
func rendersYield() bool {
	if RootDB == nil {
		return true
	}
	var v string
	RootDB.Get(LLMTable, imageRenderYieldKey, &v)
	return v != "never"
}

// renderLaneKey carries the lane of the turn a render was asked for in.
type renderLaneKey struct{}

// WithRenderLane records, on a job detached from its turn, the lane that turn
// ran on, for the render queue alone. A render outlives its turn, so it runs
// detached, on a context of its own that carries no lane and reads as
// background; without this a render a person asked for in a chat would queue
// behind a schedule's. The job's model calls keep the background lane.
func WithRenderLane(ctx context.Context, lane RunLane) context.Context {
	return context.WithValue(ctx, renderLaneKey{}, lane)
}

// renderLaneOf is the lane a render queues on: the asking turn's, when a
// detached job recorded it, else the context's own.
func renderLaneOf(ctx context.Context) RunLane {
	if ctx != nil {
		if l, ok := ctx.Value(renderLaneKey{}).(RunLane); ok && l != "" {
			return l
		}
	}
	return RunLaneOf(ctx)
}

// renderCaller reads the label, defaulting to local.
func renderCaller(ctx context.Context) string {
	if ctx == nil {
		return "local"
	}
	if s, ok := ctx.Value(renderCallerKey{}).(string); ok && strings.TrimSpace(s) != "" {
		return s
	}
	return "local"
}

// imageQueueOnce starts the render limiter on first use.
//
// Started lazily rather than at boot because nothing knows a deployment renders
// until it does, and a scheduler goroutine for a capability nobody uses is
// noise. An operator who wants renders unqueued calls StartImageScheduler(0).
var imageQueueOnce sync.Once

// queueRender takes a render slot and returns its release. Always returns a
// usable release, so callers can defer without a nil check.
func queueRender(ctx context.Context) func() {
	imageQueueOnce.Do(func() { StartImageScheduler(defaultImageParallel) })
	// A render a person is waiting on goes ahead of a schedule's: the same
	// lanes the model queue keeps, applied to the render queue.
	release, err := ollama.AcquireImageSlotLane(ctx, renderCaller(ctx), renderLaneOf(ctx) != LaneInteractive && rendersYield())
	if err != nil {
		// The caller went away or its deadline passed while queued. Not a
		// reason to refuse the render — the request below will fail on the same
		// context and report it in its own terms.
		return func() {}
	}
	return release
}

// defaultImageParallel is how many renders run at once. One, because image
// backends serialize internally anyway (A1111 and ComfyUI run a job at a time
// per GPU), so the requests already queue — just at the far end, where nothing
// here can see them or take turns fairly.
const defaultImageParallel = 1

// GenerateImageWithBackend generates through a NAMED backend: a connector
// (rest_image, or a service giving image.generate). An empty name (or
// "default") uses whatever the admin configured.
//
// The caller is responsible for checking the name is reachable first
// (ImageBackendReachable); this routes, it doesn't authorize.
func GenerateImageWithBackend(ctx context.Context, backend, prompt string, landscape bool) (*ImageGenResult, error) {
	// Renders took no slot at all until now: the heaviest thing on the box, and
	// the only one racing instead of queueing.
	defer queueRender(ctx)()

	backend = strings.TrimSpace(backend)
	if backend == "" || backend == "default" {
		backend = defaultImageBackend()
	}
	return generateWithBackend(ctx, backend, prompt, landscape)
}
