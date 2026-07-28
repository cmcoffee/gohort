// Per-app static assets — the missing third of the image pipeline.
//
// Before this, an app could not reference a picture at all. generate_image
// hands the CHAT a rendered image and deletes the local file behind it; a
// script could write bytes into the workspace, but nothing served the
// workspace to a browser. So an app that wanted artwork had exactly one
// option — draw it in canvas code — and any request for real art dead-ended.
//
// This gives an app a small directory of its own, served read-only at
// /custom/<slug>/assets/<name>. Files on disk rather than bytes in the record:
// an app spec is read, diffed, exported, and re-saved constantly, and carrying
// base64 sprites through all of that would bloat every one of those paths for
// data nothing but an <img> tag ever reads.

package core

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

var (
	appAssetsDirMu sync.RWMutex
	appAssetsDir   string
)

// SetAppAssetsDir configures the base directory holding per-app assets. Wired
// at startup alongside SetWorkspacesDir / SetImageDir.
func SetAppAssetsDir(dir string) {
	appAssetsDirMu.Lock()
	appAssetsDir = dir
	appAssetsDirMu.Unlock()
}

// AppAssetsDir returns the configured base, or "" when unset — which callers
// must treat as "assets disabled" rather than falling back to a default that
// could escape the intended tree.
func AppAssetsDir() string {
	appAssetsDirMu.RLock()
	defer appAssetsDirMu.RUnlock()
	return appAssetsDir
}

// appAssetExts is the allowlist of storable asset types.
//
// An allowlist, not a denylist: this directory is served to browsers, so the
// question is not "what is dangerous today" but "what do we vouch for". Images
// and fonts render; .html and .js would execute in the app's own origin, which
// is a different and much larger promise than "an app can have a picture".
var appAssetExts = map[string]string{
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".gif":   "image/gif",
	".webp":  "image/webp",
	".svg":   "image/svg+xml",
	".ico":   "image/x-icon",
	".woff":  "font/woff",
	".woff2": "font/woff2",
}

// MaxAppAssetBytes caps one asset. Generated art lands well under this; the
// cap exists so a runaway write can't fill the disk an app's records live on.
const MaxAppAssetBytes = 8 << 20 // 8 MiB

// MaxAppAssets caps how many assets one app may hold.
const MaxAppAssets = 64

// AppAssetContentType returns the content type for a stored asset name, and
// whether the extension is allowed at all.
func AppAssetContentType(name string) (string, bool) {
	ct, ok := appAssetExts[strings.ToLower(filepath.Ext(name))]
	return ct, ok
}

// ValidAppAssetName reports whether name is a safe flat asset filename.
//
// Flat names only. A path separator here would let a write escape the app's
// directory and a read reach anything the process can open, and this name
// arrives from an LLM-authored tool call — exactly the input that should never
// be trusted to stay inside its lane.
func ValidAppAssetName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 {
		return false
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return false
	}
	if strings.HasPrefix(name, ".") {
		return false
	}
	_, ok := AppAssetContentType(name)
	return ok
}

// appAssetDir resolves (and optionally creates) one app's asset directory.
// Scoped per owner AND slug so two users' apps of the same name never collide.
func appAssetDir(owner, slug string, create bool) (string, error) {
	base := AppAssetsDir()
	if base == "" {
		return "", fmt.Errorf("app assets are not configured on this deployment")
	}
	owner = strings.TrimSpace(owner)
	slug = strings.TrimSpace(slug)
	if owner == "" || slug == "" {
		return "", fmt.Errorf("owner and slug are required")
	}
	// Validate rather than transform, matching EnsureWorkspaceDir: both
	// components go straight into a path, so anything that could traverse is
	// refused outright instead of being quietly rewritten into something that
	// looks safe but no longer matches what the caller asked for.
	for label, part := range map[string]string{"owner": owner, "slug": slug} {
		if strings.ContainsAny(part, `/\`) || strings.Contains(part, "..") || part == "." {
			return "", fmt.Errorf("invalid %s for asset path: %q", label, part)
		}
	}
	dir := filepath.Join(base, owner, slug)
	if create {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("create asset dir: %w", err)
		}
	}
	return dir, nil
}

// SaveAppAsset writes one asset for an app, replacing any file of the same
// name. Returns the relative URL path the app should reference.
func SaveAppAsset(owner, slug, name string, data []byte) (string, error) {
	if !ValidAppAssetName(name) {
		return "", fmt.Errorf("invalid asset name %q — use a flat filename with one of these extensions: %s", name, allowedAppAssetExts())
	}
	if len(data) == 0 {
		return "", fmt.Errorf("asset %q is empty", name)
	}
	if len(data) > MaxAppAssetBytes {
		return "", fmt.Errorf("asset %q is %d bytes, over the %d-byte limit", name, len(data), MaxAppAssetBytes)
	}
	dir, err := appAssetDir(owner, slug, true)
	if err != nil {
		return "", err
	}
	existing, _ := ListAppAssets(owner, slug)
	if len(existing) >= MaxAppAssets && !containsName(existing, name) {
		return "", fmt.Errorf("app already holds %d assets (the limit) — delete one before adding another", MaxAppAssets)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		return "", fmt.Errorf("write asset: %w", err)
	}
	return "assets/" + name, nil
}

// ReadAppAsset returns one asset's bytes and content type.
func ReadAppAsset(owner, slug, name string) ([]byte, string, error) {
	if !ValidAppAssetName(name) {
		return nil, "", fmt.Errorf("invalid asset name")
	}
	dir, err := appAssetDir(owner, slug, false)
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return nil, "", err
	}
	ct, _ := AppAssetContentType(name)
	return data, ct, nil
}

// ListAppAssets returns the app's asset filenames, sorted.
func ListAppAssets(owner, slug string) ([]string, error) {
	dir, err := appAssetDir(owner, slug, false)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no assets yet is not an error
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !ValidAppAssetName(e.Name()) {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

// DeleteAppAsset removes one asset. A missing file is not an error — the
// caller wanted it gone and it is gone.
func DeleteAppAsset(owner, slug, name string) error {
	if !ValidAppAssetName(name) {
		return fmt.Errorf("invalid asset name")
	}
	dir, err := appAssetDir(owner, slug, false)
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// DeleteAppAssets removes an app's whole asset directory — called when the app
// itself is deleted, so assets don't outlive the thing that served them.
func DeleteAppAssets(owner, slug string) error {
	dir, err := appAssetDir(owner, slug, false)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func allowedAppAssetExts() string {
	var exts []string
	for e := range appAssetExts {
		exts = append(exts, e)
	}
	sort.Strings(exts)
	return strings.Join(exts, " ")
}

func containsName(list []string, name string) bool {
	for _, n := range list {
		if n == name {
			return true
		}
	}
	return false
}
