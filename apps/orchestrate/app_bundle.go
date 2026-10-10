package orchestrate

// An app as one file, and back into a folder.
//
// The bundle is the one My Apps' Export writes (oddjob.bundle/v1): the app's
// spec, its assets, its notes, and what it depends on (the agent it binds).
// pack writes it into the workspace from the app as published, so Builder can
// hand an app over as a file; unpack writes a bundle somebody sent into a
// project folder to read and change before anything is installed. Installing
// a bundle stays where it was: My Apps, Import, which lands it switched off.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/cmcoffee/oddjob/core"
)

// appBundleExt is an app bundle's file extension.
const appBundleExt = ".oddjobapp"

// appDefPack writes an app's bundle into the workspace.
func (t *chatTurn) appDefPack(args map[string]any) (string, error) {
	id := slugify(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "slug"), stringArg(args, "name")))
	if id == "" {
		if abs, rel, err := t.appFolderDir(args); err == nil {
			if _, m, err := t.appFolderRead(abs, rel); err == nil {
				id = m.Slug
			}
		}
	}
	if id == "" {
		return "", errors.New("name the app (id) or its folder (dir)")
	}
	spec, ok := LoadAppSpec(t.user, id)
	if !ok {
		return "", fmt.Errorf("no app %q is published: a bundle is made from the app as it is live, so publish its folder first", id)
	}
	bundle, err := ExportArtifactBundleAsUser(RootDB, t.user, []ArtifactSel{{Type: "custom_app", Name: spec.Slug}}, UserExportOptions{IncludeDeps: true})
	if err != nil {
		return "", fmt.Errorf("could not pack %q: %w", spec.Slug, err)
	}
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return "", err
	}
	out := strings.TrimSpace(stringArg(args, "file"))
	if out == "" {
		out = spec.Slug + appBundleExt
	}
	ws, _, _ := t.turnWorkspace()
	p, err := ResolveWorkspacePath(ws, out)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return "", err
	}
	var also []string
	for _, a := range bundle.Artifacts {
		if a.Type != "custom_app" {
			also = append(also, a.Type+" "+a.Name)
		}
	}
	carries := ""
	if len(also) > 0 {
		carries = " It also carries what the app depends on: " + strings.Join(also, ", ") + "."
	}
	stale := ""
	if abs, rel, err := t.appFolderDir(map[string]any{"id": spec.Slug}); err == nil {
		if _, err := os.Stat(filepath.Join(abs, "app.json")); err == nil {
			stale = fmt.Sprintf(" It holds the app AS PUBLISHED: if %s/ has changes since its last publish, publish it and pack again.", rel)
		}
	}
	return fmt.Sprintf("Packed app %q into %s (%d KB): its page, scripts, shared modules, settings, notes and assets.%s Anyone can import it at My Apps, Import, where it lands switched off for review. Records never travel.%s",
		spec.Name, out, (len(data)+1023)/1024, carries, stale), nil
}

// appDefUnpack writes the app in a bundle into a project folder, installing
// nothing.
func (t *chatTurn) appDefUnpack(args map[string]any) (string, error) {
	file := strings.TrimSpace(stringArg(args, "file"))
	if file == "" {
		return "", errors.New("file is required: the bundle in your workspace, e.g. \"voidrunner.oddjobapp\"")
	}
	ws, _, _ := t.turnWorkspace()
	p, err := ResolveWorkspacePath(ws, file)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("no bundle %s in your workspace", file)
	}
	bundle, err := ParseArtifactBundle(data)
	if err != nil {
		return "", fmt.Errorf("%s is not a oddjob bundle: %v", file, err)
	}
	want := strings.TrimSpace(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "name")))
	var recipe json.RawMessage
	var kinds, also []string
	for _, a := range bundle.Artifacts {
		kinds = append(kinds, a.Type)
		if a.Type == "custom_app" && recipe == nil && (want == "" || strings.EqualFold(a.Name, want) || slugify(a.Name) == slugify(want)) {
			recipe = a.Recipe
			continue
		}
		also = append(also, a.Type+" "+a.Name)
	}
	if recipe == nil {
		return "", fmt.Errorf("%s holds no app to unpack (it holds: %s)", file, strings.Join(kinds, ", "))
	}
	var spec AppSpec
	if err := json.Unmarshal(recipe, &spec); err != nil {
		return "", fmt.Errorf("the app in %s cannot be read: %v", file, err)
	}
	if strings.TrimSpace(spec.Slug) == "" {
		spec.Slug = slugify(spec.Name)
	}
	rel := strings.TrimSpace(stringArg(args, "dir"))
	if rel == "" {
		rel = spec.Slug + ".app"
	}
	abs, err := ResolveWorkspacePath(ws, rel)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(abs, "app.json")); err == nil && !boolArg(args, "overwrite") {
		return "", fmt.Errorf("%s already holds an app folder: pass overwrite=true to replace it with the bundle's app, or dir=\"...\" for another folder", rel)
	}
	w, err := writeAppFolder(abs, spec, bundleAssets(recipe), "")
	if err != nil {
		return "", err
	}
	note := ""
	if _, live := LoadAppSpec(t.user, spec.Slug); live {
		note = fmt.Sprintf(" You already have an app %q: publishing this folder over it is refused unless you mean it (overwrite_live=true).", spec.Slug)
	}
	carries := ""
	if len(also) > 0 {
		carries = " The bundle also carries " + strings.Join(also, ", ") + ": unpacking does not install those; importing the bundle (My Apps, Import) does."
	}
	return fmt.Sprintf("Unpacked app %q from %s into %s/: %d page file(s), %d data source(s), %d action(s), %d shared module(s), %d asset(s), NOTES.md. Nothing was installed. Read NOTES.md and the scripts first; then run, change and publish it like any app folder.%s%s",
		spec.Name, file, rel, w.pages, len(w.manifest.DataSources), len(w.manifest.Actions), len(spec.Libraries), w.assets, note, carries), nil
}

// bundleAssets reads the assets an app recipe carries ({"assets": {name:
// base64}}), dropping any that does not decode.
func bundleAssets(recipe json.RawMessage) map[string][]byte {
	var r struct {
		Assets map[string]string `json:"assets"`
	}
	if json.Unmarshal(recipe, &r) != nil {
		return nil
	}
	out := map[string][]byte{}
	for n, enc := range r.Assets {
		if data, err := base64.StdEncoding.DecodeString(enc); err == nil {
			out[n] = data
		}
	}
	return out
}
