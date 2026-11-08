package core

import (
	"context"
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

// Image backend uses: what an image backend that both makes and edits
// pictures is used for, set on Media Generation (ImageTable "uses", by
// backend). Absent is both.
const (
	ImageUseBoth     = ""
	ImageUseEdit     = "edit"
	ImageUseGenerate = "generate"
)

// ImageBackendUse is what backend name is used for: ImageUseEdit (editing and
// blending only), ImageUseGenerate (generating only), or ImageUseBoth.
func ImageBackendUse(name string) string {
	if RootDB == nil {
		return ImageUseBoth
	}
	var uses map[string]string
	RootDB.Get(ImageTable, "uses", &uses)
	switch u := uses[strings.TrimSpace(name)]; u {
	case ImageUseEdit, ImageUseGenerate:
		return u
	}
	return ImageUseBoth
}

// ImageEditFunc edits pictures for a named backend of another kind than
// rest_image (a service, core/service): the prompt and the source pictures'
// verified bytes in, in order, the edited picture out. Several pictures are a
// blend or a composite.
type ImageEditFunc func(sess *ToolSession, prompt string, images [][]byte) (*ImageGenResult, error)

// imageEditBackend is a registered edit backend and how many source pictures
// one call takes, read on each use: a setting can change it without the
// backend registering again.
type imageEditBackend struct {
	fn    ImageEditFunc
	limit func() int
}

// max is how many source pictures one call takes now: at least one.
func (b imageEditBackend) max() int {
	if b.limit == nil {
		return 1
	}
	if n := b.limit(); n > 1 {
		return n
	}
	return 1
}

var imageEditBackends = map[string]imageEditBackend{}

// RegisterImageEditBackend installs (or replaces) a named edit backend taking
// up to limit() source pictures a call (at least one), or with fn nil drops
// it. limit is read on each use.
func RegisterImageEditBackend(name string, limit func() int, fn ImageEditFunc) {
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
	imageEditBackends[name] = imageEditBackend{fn: fn, limit: limit}
}

func lookupImageEditBackend(name string) (imageEditBackend, bool) {
	imageBackendMu.RLock()
	defer imageBackendMu.RUnlock()
	b, ok := imageEditBackends[strings.TrimSpace(name)]
	return b, ok
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

func init() {
	// The hard cap is a runaway guard on renders per turn, set well above a
	// genuine multi-image turn ("make me 4 logos"); the count is turn-scoped
	// with the session (CountImageRender).
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

// ImageGenHardCap is the absolute number of renders one turn may run.
//
// The grouped `image` tool in tools/imagefetch checks it on every render
// (checkRenderBudget). When it lived only on the image tool that came before,
// and that tool was unregistered, the live render path had no runaway guard.
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

// ImageGenResult holds the output of an image generation call.
type ImageGenResult struct {
	URL    string // URL or local path to the generated image
	Prompt string // the prompt that was used
}

// generateWithBackend renders through a registered backend: a connector's,
// by its name. Every image comes from one; none is written in Go.
func generateWithBackend(ctx context.Context, backend, prompt string, landscape bool) (*ImageGenResult, error) {
	if backend == "" {
		return nil, fmt.Errorf("image generation is not set up: choose an image connector under Admin > Services > Media Generation")
	}
	if ImageBackendUse(backend) == ImageUseEdit {
		return nil, fmt.Errorf("image backend %s is set to editing and blending only (Admin > Services > Media Generation): pass the pictures to work from, or make the picture with another backend", backend)
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
