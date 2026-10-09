package temptool

// A tool as one file, and back into a folder: the same bundle Extensions'
// Export writes (gohort.bundle/v1), whose tool recipe is the whole tool
// record, script, helpers and notes included. pack writes it into the
// workspace from the tool as saved; unpack writes a bundled script tool into
// a project folder to read and change before anything is installed.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

const toolBundleExt = ".gohorttool"

// toolPack writes a tool's bundle into the workspace.
func toolPack(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil {
		return "", errors.New("requires a session")
	}
	name := strings.TrimSpace(StringArg(args, "name"))
	if name == "" {
		return "", errors.New("name is required: the tool to pack")
	}
	if _, ok := loadExistingToolRecord(sess, name); !ok {
		return "", fmt.Errorf("no tool %q: a bundle is made from a saved tool, so publish its folder first", name)
	}
	bundle, err := ExportArtifactBundleAsUser(RootDB, sess.Username, []ArtifactSel{{Type: "tool", Name: name}}, UserExportOptions{IncludeDeps: true})
	if err != nil {
		return "", fmt.Errorf("could not pack %q: %w", name, err)
	}
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return "", err
	}
	root, err := EnsureSessionWorkspace(sess)
	if err != nil {
		return "", err
	}
	out := strings.TrimSpace(StringArg(args, "file"))
	if out == "" {
		out = name + toolBundleExt
	}
	p, err := ResolveWorkspacePath(root, out)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("Packed tool %q into %s (%d KB): the tool as saved, with its script, helper files and notes. Anyone can import it at Extensions, Tools, Import. If %s.tool/ has changes since its last publish, publish it and pack again.",
		name, out, (len(data)+1023)/1024, name), nil
}

// toolUnpack writes the script tool in a bundle into a project folder,
// installing nothing.
func toolUnpack(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil {
		return "", errors.New("requires a session")
	}
	file := strings.TrimSpace(StringArg(args, "file"))
	if file == "" {
		return "", errors.New("file is required: the bundle in your workspace, e.g. \"weather_lookup.gohorttool\"")
	}
	root, err := EnsureSessionWorkspace(sess)
	if err != nil {
		return "", err
	}
	p, err := ResolveWorkspacePath(root, file)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("no bundle %s in your workspace", file)
	}
	bundle, err := ParseArtifactBundle(data)
	if err != nil {
		return "", fmt.Errorf("%s is not a gohort bundle: %v", file, err)
	}
	want := strings.TrimSpace(StringArg(args, "name"))
	var tt *TempTool
	var kinds []string
	for _, a := range bundle.Artifacts {
		kinds = append(kinds, a.Type+" "+a.Name)
		if a.Type != "tool" || tt != nil || (want != "" && a.Name != want) {
			continue
		}
		var t TempTool
		if err := json.Unmarshal(a.Recipe, &t); err != nil {
			return "", fmt.Errorf("the tool %q in %s cannot be read: %v", a.Name, file, err)
		}
		tt = &t
	}
	if tt == nil {
		return "", fmt.Errorf("%s holds no tool to unpack (it holds: %s)", file, strings.Join(kinds, ", "))
	}
	if effectiveTempToolMode(*tt) != TempToolModeShell || strings.TrimSpace(tt.ScriptBody) == "" {
		return "", fmt.Errorf("%q is a %s tool, not a script tool: a folder holds a script tool. Import the bundle at Extensions, Tools, Import instead", tt.Name, effectiveTempToolMode(*tt))
	}
	rel := strings.TrimSpace(StringArg(args, "dir"))
	if rel == "" {
		rel = tt.Name + ".tool"
	}
	abs, err := ResolveWorkspacePath(root, rel)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(abs, toolManifestFile)); err == nil && !BoolArg(args, "overwrite") {
		return "", fmt.Errorf("%s already holds a tool folder: pass overwrite=true to replace it with the bundle's tool, or dir=\"...\" for another folder", rel)
	}
	if _, err := writeToolFolder(abs, *tt, ""); err != nil {
		return "", err
	}
	note := ""
	if _, live := loadExistingToolRecord(sess, tt.Name); live {
		note = fmt.Sprintf(" You already have a tool %q: publishing this folder over it is refused unless you mean it (overwrite_live=true).", tt.Name)
	}
	return fmt.Sprintf("Unpacked tool %q from %s into %s/: tool.json, %s, %d helper file(s), NOTES.md. Nothing was installed. Read NOTES.md and the script first; then run and publish it like any tool folder.%s",
		tt.Name, file, rel, chFirstStr(tt.ScriptName, "script.py"), len(tt.WorkspaceFiles), note), nil
}
