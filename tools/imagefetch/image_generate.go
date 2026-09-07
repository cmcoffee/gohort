package imagefetch

import (
	"encoding/base64"
	"fmt"
	_ "image/gif"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	_ "golang.org/x/image/webp" // register the WebP decoder for image.Decode
)

// --- GenerateImageTool ---

type GenerateImageTool struct{}

func (t *GenerateImageTool) Name() string { return "generate_image" }

func (t *GenerateImageTool) Caps() []Capability { return []Capability{CapNetwork, CapRead} }

// image-gen API call
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

// checkGeneratePrompt is the subject check for a render with NO source images.
//
// The strongest case of the two: asked for a picture of somebody whose likeness
// is sitting in the library, a text-only generate cannot produce them and will
// produce a confident stranger instead. There is nothing to rewrite here — no
// attached picture to point a name at — so the whole check is the refusal, and
// the fix it names turns the call into an edit.
func checkGeneratePrompt(sess *ToolSession, args map[string]any) error {
	return refuseUnpassedPeople(sess, strings.TrimSpace(StringArg(args, "prompt")), nil)
}

// planGenerate resolves and checks the backend a generate call will use. Split
// from generateImage for the same reason planEdit is: these errors have to
// reach the model before the call detaches, not a minute after it. See
// ImageTool.Preflight.
func planGenerate(sess *ToolSession, args map[string]any, avail imageActions) (string, error) {
	if !avail.generate {
		return "", fmt.Errorf("the generate action is unavailable — no image-generation provider is configured. Tell the user image generation isn't set up; do NOT retry")
	}
	if err := checkGeneratePrompt(sess, args); err != nil {
		return "", err
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
		return "", fmt.Errorf("image backend %q works from source pictures and can't create from text alone — use one of: %s, or pass the pictures to work from in images", backend, strings.Join(generatorNames(avail), ", "))
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
		if ref, stable := RecordRecentImageStable(sess, data, note, origin); ref != "" {
			// The STABLE id only, and deliberately no position. This render
			// finished in the background, between rounds — naming a position
			// here would be stating a number for a picture that was never in
			// any list the model read, while every position it IS holding has
			// silently moved down one to make room. The framework keeps this
			// picture out of the numbering until the ring is listed again
			// (see core.SnapshotImageRefs); the id is what works meanwhile,
			// and it works afterwards too.
			if stable != "" {
				msg += fmt.Sprintf(" Refer to it as %s — that id always means this picture. It has no image#N position yet:"+
					" it finished in the background, after the list you were last shown.", stable)
			} else {
				msg += fmt.Sprintf(" It is %s right now — a POSITION, which moves when the next picture is saved.%s", ref, stableRefNote(stable))
			}
		}
		// Show it, on the round that writes the line about it.
		//
		// showToModel refuses on a detached session, and for its other callers
		// that is right — there is no round left to look. But a detached RENDER
		// still ends with the model composing "here it is, it shows X", and
		// without the picture that sentence is written from the prompt by an
		// agent that never saw the result. So the one case where being wrong is
		// invisible — an async render that came back subtly wrong — was the one
		// case with no self-check. The view channel is separate from the
		// delivery channel, so this cannot send the image twice.
		sess.AppendViewImageAs(data, "the finished render (the AFTER picture)")
		msg += " LOOK AT IT FIRST: the picture is included with this result. Describe what is actually there, not what was asked for — if the render came back wrong (blank, garbled, the wrong number of things, an edit that did nothing), say so plainly instead of announcing it as a match."
		return msg, nil
	}

	msg := fmt.Sprintf("Stored at %q (%d bytes). This is normally meant for delivery — call workspace(action=\"attach\", path=%q, cleanup=true) and then write a short line describing it. (Skip the attach only if the user explicitly asked you NOT to send it — rare.)", name, len(data), name)
	// Spell out that the workspace copy is one-shot. Saying "do not delete it"
	// next to a path the model is told to clean up reads as a contradiction, and
	// what it did instead was attach with cleanup and then try to attach the
	// same path AGAIN — which errors, because the file is gone. From there it
	// regenerated the picture and delivered the wrong one.
	if ref, stable := RecordRecentImageStable(sess, data, note, origin); ref != "" {
		msg += fmt.Sprintf(" The workspace copy is consumed by that attach and the path stops working. This picture is %s RIGHT NOW, and that is a POSITION: whatever is saved next becomes image#1 and this one moves down.%s Never re-attach the workspace path after a cleanup — it is already delivered.", ref, stableRefNote(stable))
	}
	// A render is a guess at the prompt, not a rendering of it: the wrong
	// number of people, the text unreadable, the edit applied to nothing. The
	// agent used to find that out when the user did.
	msg += showToModel(sess, data, "the finished render (the AFTER picture)", "This is what the backend produced, which is not always what was asked for")
	return msg, nil
}
