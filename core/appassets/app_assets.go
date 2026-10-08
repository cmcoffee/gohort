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

package appassets

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
	// Sound, for an app that plays it (a game's effects and music, an alert
	// tone). Inert data like the images: nothing here executes.
	".mp3": "audio/mpeg",
	".ogg": "audio/ogg",
	".wav": "audio/wav",
	".m4a": "audio/mp4",
	// 3D models, for a WebGL game or viewer: a .glb is one self-contained
	// file; a .gltf is JSON that names a .bin of buffers (and textures)
	// beside it by relative path. Inert data a loader parses, nothing that
	// runs, and served sandboxed like the rest.
	".glb":  "model/gltf-binary",
	".gltf": "model/gltf+json",
	".bin":  "application/octet-stream",
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

// assetMagic is how each binary type's file begins. An author with no image
// library wrote a one-pixel GIF as base64 TEXT into "wood-grain.png" and it
// was saved: every load of it then failed in the browser, a long way from the
// write that caused it. A file that does not start the way its extension
// says is refused here, where the message can say what it is instead.
var assetMagic = map[string][]string{
	".png":   {"\x89PNG\r\n\x1a\n"},
	".jpg":   {"\xff\xd8\xff"},
	".jpeg":  {"\xff\xd8\xff"},
	".gif":   {"GIF87a", "GIF89a"},
	".ico":   {"\x00\x00\x01\x00"},
	".woff":  {"wOFF"},
	".woff2": {"wOF2"},
	".ogg":   {"OggS"},
	".glb":   {"glTF"},
}

// assetMatchesType reports why data is not the type name's extension says,
// or nil. The text types are checked for being text of the right kind; .bin
// is any bytes by definition.
func assetMatchesType(name string, data []byte) error {
	ext := strings.ToLower(filepath.Ext(name))
	s := string(data)
	ok := true
	switch ext {
	case ".webp":
		ok = len(s) >= 12 && s[:4] == "RIFF" && s[8:12] == "WEBP"
	case ".wav":
		ok = len(s) >= 12 && s[:4] == "RIFF" && s[8:12] == "WAVE"
	case ".mp3":
		ok = strings.HasPrefix(s, "ID3") || (len(data) >= 2 && data[0] == 0xff && data[1]&0xe0 == 0xe0)
	case ".m4a":
		ok = len(s) >= 8 && s[4:8] == "ftyp"
	case ".svg":
		ok = strings.Contains(strings.ToLower(s[:min(len(s), 4096)]), "<svg")
	case ".gltf":
		ok = strings.HasPrefix(strings.TrimSpace(s), "{")
	default:
		if prefixes, has := assetMagic[ext]; has {
			ok = false
			for _, p := range prefixes {
				if strings.HasPrefix(s, p) {
					ok = true
					break
				}
			}
		}
	}
	if ok {
		return nil
	}
	start := s[:min(len(s), 12)]
	return fmt.Errorf("asset %q is not a %s file: its bytes start %q. Write the file's real bytes (a script that encodes the format, e.g. zlib+struct for a PNG, the wave module for a WAV), not text or base64 of it", name, strings.TrimPrefix(ext, "."), start)
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
		return "", fmt.Errorf("invalid asset name %q, use a flat filename with one of these extensions: %s", name, allowedAppAssetExts())
	}
	if len(data) == 0 {
		return "", fmt.Errorf("asset %q is empty", name)
	}
	if len(data) > MaxAppAssetBytes {
		return "", fmt.Errorf("asset %q is %d bytes, over the %d-byte limit", name, len(data), MaxAppAssetBytes)
	}
	if err := assetMatchesType(name, data); err != nil {
		return "", err
	}
	dir, err := appAssetDir(owner, slug, true)
	if err != nil {
		return "", err
	}
	existing, _ := ListAppAssets(owner, slug)
	if len(existing) >= MaxAppAssets && !containsName(existing, name) {
		return "", fmt.Errorf("app already holds %d assets (the limit): delete one before adding another", MaxAppAssets)
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
