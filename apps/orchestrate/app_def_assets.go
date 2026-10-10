package orchestrate

// An app's assets from the authoring side: a file in the workspace (a script's
// output, a downloaded image, generated music saved there) becomes one of the
// app's images, fonts or sounds, served to its page at assets/<name>. Before
// this the read route existed and nothing could write to it, so an app's art
// had to be drawn in code or pasted in as a data: URI.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/oddjob/tools/imagefetch"
)

// appDefAddAsset saves the workspace file at path as the app's asset `asset`
// (default: the file's own name).
func (t *chatTurn) appDefAddAsset(args map[string]any) (string, error) {
	key := slugify(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "slug"), stringArg(args, "name")))
	spec, ok := LoadAppSpec(t.user, key)
	if !ok {
		return "", appNotFound(args, "to add an asset to")
	}
	path := strings.TrimSpace(stringArg(args, "path"))
	if prompt := strings.TrimSpace(stringArg(args, "prompt")); prompt != "" && path == "" {
		return t.appDefGenerateAsset(spec, prompt, strings.TrimSpace(stringArg(args, "asset")))
	}
	if path == "" {
		return "", fmt.Errorf("path is required: the workspace file to add, e.g. \"art/hero.png\" (or prompt, to have the image generator draw it)")
	}
	dir, _, _ := t.turnWorkspace()
	full, err := ResolveWorkspacePath(dir, path)
	if err != nil {
		return "", fmt.Errorf("path: %w", err)
	}
	st, err := os.Stat(full)
	if err != nil || st.IsDir() {
		return "", fmt.Errorf("no file at %q in your workspace", path)
	}
	if st.Size() > MaxAppAssetBytes {
		return "", fmt.Errorf("%q is %.1f MiB: an asset is at most %d MiB", path, float64(st.Size())/(1<<20), MaxAppAssetBytes>>20)
	}
	name := strings.TrimSpace(stringArg(args, "asset"))
	if name == "" {
		name = filepath.Base(full)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	if _, err := SaveAppAsset(t.user, spec.Slug, name, data); err != nil {
		return "", err
	}
	return fmt.Sprintf("Saved %q as an asset of %q. Reference it from the page by the RELATIVE path assets/%s, the way you would any file: <img src=\"assets/%s\">, new Audio(\"assets/%s\"), fetch(\"assets/%s\"), or a library loader such as three's TextureLoader. Replacing it later is the same call with the same asset name.", path, spec.Slug, name, name, name, name), nil
}

// Seams over the deployment's image generator, for a test to stand in.
var (
	assetImageAvailable = ImageGenerationAvailable
	assetImageGenerate  = GenerateImageWithBackend
)

// appDefGenerateAsset has the deployment's image generator draw prompt and
// saves the picture as the app's asset `name`. One call, because the chat
// image tool is built to DELIVER a picture: it attaches it to the reply and
// its workspace copy is consumed and reaped, so the art an app asked for
// reached the chat and never the app. Renders count against the same
// per-turn ceiling the image tool keeps, since each one costs the owner.
func (t *chatTurn) appDefGenerateAsset(spec AppSpec, prompt, name string) (string, error) {
	if !assetImageAvailable() {
		return "", fmt.Errorf("no image generator is configured on this deployment, so nothing was drawn. Make the picture instead (inline <svg>, emoji, CSS, or a PNG a script writes) or find one (fetch_url with save_to, then add_asset path=)")
	}
	t.assetRenders++
	if limit := ImageGenHardCap(); t.assetRenders > limit {
		return "", fmt.Errorf("no picture was drawn: %d generated assets is this turn's ceiling. Use what you have, or draw the rest in code", limit)
	}
	ctx := t.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	result, err := assetImageGenerate(ctx, "", prompt, false)
	if err != nil {
		return "", fmt.Errorf("image generation failed: %w", err)
	}
	var data []byte
	if strings.HasPrefix(result.URL, "http://") || strings.HasPrefix(result.URL, "https://") {
		data, err = imagefetch.FetchImageBytes(result.URL, "", 30)
	} else {
		data, err = os.ReadFile(result.URL)
		os.Remove(result.URL)
	}
	if err != nil {
		return "", fmt.Errorf("could not retrieve the generated image: %w", err)
	}
	ext := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp", "image/gif": ".gif"}[http.DetectContentType(data)]
	if ext == "" {
		return "", fmt.Errorf("the image generator returned something that is not a PNG, JPEG, WebP or GIF")
	}
	renamed := ""
	if name == "" {
		name = strings.Trim(slugify(prompt), "-")
		if len(name) > 40 {
			name = strings.Trim(name[:40], "-")
		}
		name += ext
	} else if cur := strings.ToLower(filepath.Ext(name)); cur != ext && !(cur == ".jpeg" && ext == ".jpg") {
		renamed = name
		name = strings.TrimSuffix(name, filepath.Ext(name)) + ext
	}
	if _, err := SaveAppAsset(t.user, spec.Slug, name, data); err != nil {
		return "", err
	}
	msg := fmt.Sprintf("Generated a %d KB picture from the prompt and saved it as an asset of %q: reference it as assets/%s. You have not seen it: describe it to the user as made from your prompt, not as checked.", len(data)>>10, spec.Slug, name)
	if renamed != "" {
		msg += fmt.Sprintf(" It came back as %s, so it is named %s, not %s: use that name.", strings.TrimPrefix(ext, "."), name, renamed)
	}
	return msg, nil
}

// appDefRemoveAsset deletes the app's asset `asset`.
func (t *chatTurn) appDefRemoveAsset(args map[string]any) (string, error) {
	key := slugify(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "slug"), stringArg(args, "name")))
	spec, ok := LoadAppSpec(t.user, key)
	if !ok {
		return "", appNotFound(args, "to remove an asset from")
	}
	name := strings.TrimSpace(stringArg(args, "asset"))
	if name == "" {
		return "", fmt.Errorf("asset is required: the asset's name, as get lists them")
	}
	if err := DeleteAppAsset(t.user, spec.Slug, name); err != nil {
		return "", err
	}
	return fmt.Sprintf("Removed asset %q from %q.", name, spec.Slug), nil
}

// appAssetNames is the app's assets, for get.
func appAssetNames(owner, slug string) []string {
	names, _ := ListAppAssets(owner, slug)
	if names == nil {
		names = []string{}
	}
	return names
}
