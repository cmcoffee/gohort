package orchestrate

// An app's assets from the authoring side: a file in the workspace (a script's
// output, a downloaded image, generated music saved there) becomes one of the
// app's images, fonts or sounds, served to its page at assets/<name>. Before
// this the read route existed and nothing could write to it, so an app's art
// had to be drawn in code or pasted in as a data: URI.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/cmcoffee/gohort/core"
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
	if path == "" {
		return "", fmt.Errorf("path is required: the workspace file to add, e.g. \"art/hero.png\"")
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
	return fmt.Sprintf("Saved %q as an asset of %q. Reference it from the page by the RELATIVE path assets/%s: <img src=\"assets/%s\">, <audio src=\"assets/%s\">, or fetch(\"assets/%s\") then URL.createObjectURL on the blob for an image or sound made in JS (new Image() and new Audio() with a path are not relayed). Replacing it later is the same call with the same asset name.", path, spec.Slug, name, name, name, name), nil
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
