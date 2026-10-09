package temptool

// A script tool as a project folder in the author's workspace.
//
// A script tool was authored as one tool_def call carrying its whole script
// escaped into a JSON string, and every fix re-sent or patched that string.
// Apps had the same trouble and moved to a folder (orchestrate's app_folder.go);
// this is the same move for a shell tool, which is the one kind of tool with a
// real program inside it. One file per part, edited in place with workspace
// write / edit, the script tried on sample args without saving (run), and the
// folder saved through the same create / update and checks as before (publish).
// tool_def stays the one engine; the folder is only how its input is written.
//
//	<name>.tool/
//	  tool.json   description, params, required, capabilities, timeout, the
//	              entry script's filename, and cases to try it with
//	  script.py   the entry script (any name tool.json gives)
//	  *.py ...    helpers it imports, carried with the tool
//	  NOTES.md    for whoever edits it next: what it works around, why each
//	              choice, what was tried and failed, what its output looks like
//
// API, toolbox and pipeline tools stay where they are: they are a few fields
// of JSON already, and a folder adds nothing to them.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// toolFolderManifest is tool.json.
type toolFolderManifest struct {
	Name             string           `json:"name"`
	Description      string           `json:"description"`
	Category         string           `json:"category,omitempty"`
	Params           json.RawMessage  `json:"params,omitempty"`
	Required         *[]string        `json:"required,omitempty"`
	Script           string           `json:"script"`
	CommandTemplate  string           `json:"command_template,omitempty"`
	HookCapabilities []string         `json:"hook_capabilities,omitempty"`
	TimeoutSec       int              `json:"timeout_sec,omitempty"`
	StatePath        string           `json:"state_path,omitempty"`
	RawNetwork       bool             `json:"raw_network,omitempty"`
	ConfirmInChat    bool             `json:"confirm_in_chat,omitempty"`
	Cases            []map[string]any `json:"cases,omitempty"`
}

const (
	toolManifestFile = "tool.json"
	toolNotesFile    = "NOTES.md"
	// toolFolderBaseFile records which state of the live tool the folder
	// holds, the same guard the app folder keeps (see app_folder.go).
	toolFolderBaseFile = ".live"
)

const toolStarterScript = `# TOOL_NAME: what it does, in a line.
#
# Each param arrives as an ENVIRONMENT VARIABLE named after it:
#   query = os.environ.get("query", "")
# Print the result to stdout, JSON when the caller reads fields from it.
import json, os


def main():
    print(json.dumps({"ok": True}))


main()
`

// toolFolderDir resolves the folder: dir, or "<name>.tool".
func toolFolderDir(sess *ToolSession, args map[string]any) (abs, rel string, err error) {
	root, err := EnsureSessionWorkspace(sess)
	if err != nil {
		return "", "", fmt.Errorf("workspace: %w", err)
	}
	rel = strings.TrimSpace(StringArg(args, "dir"))
	if rel == "" {
		name := strings.TrimSpace(StringArg(args, "name"))
		if name == "" {
			return "", "", errors.New("name the tool (name) or its folder (dir, e.g. \"weather_lookup.tool\")")
		}
		rel = name + ".tool"
	}
	abs, err = ResolveWorkspacePath(root, rel)
	return abs, rel, err
}

// toolCheckout writes a tool into its folder, or starts a new one.
func toolCheckout(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil {
		return "", errors.New("requires a session")
	}
	abs, rel, err := toolFolderDir(sess, args)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(abs, toolManifestFile)); err == nil && !BoolArg(args, "overwrite") {
		return "", fmt.Errorf("%s already holds a tool folder: edit it, publish it, or pass overwrite=true to replace it with the live tool", rel)
	}
	name := strings.TrimSpace(StringArg(args, "name"))
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(rel), ".tool")
	}
	write := func(file string, data []byte) error {
		p := filepath.Join(abs, file)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		return os.WriteFile(p, data, 0o644)
	}
	existing, ok := loadExistingToolRecord(sess, name)
	if !ok {
		m := toolFolderManifest{Name: name, Script: "script.py", Params: json.RawMessage("{}")}
		manifest, _ := json.MarshalIndent(m, "", "  ")
		for f, data := range map[string][]byte{
			toolManifestFile: manifest,
			"script.py":      []byte(strings.ReplaceAll(toolStarterScript, "TOOL_NAME", name)),
			toolNotesFile:    []byte("# " + name + "\n\nFor whoever edits this tool next: what it works around, why each choice was made, what was tried and failed, and what its output looks like.\n"),
		} {
			if err := write(f, data); err != nil {
				return "", err
			}
		}
		return fmt.Sprintf("Started a new tool folder %s/ for %q: tool.json (fill in description and params; each param reaches the script as an environment variable), a starter script.py, and NOTES.md. Edit them with workspace write / edit, put helper modules beside the script, try it with tool_def(action=\"run\", dir=%q, args={...}), then tool_def(action=\"publish\", dir=%q) creates the tool.", rel, name, rel, rel), nil
	}
	if effectiveTempToolMode(existing) != TempToolModeShell || strings.TrimSpace(existing.ScriptBody) == "" {
		return "", fmt.Errorf("%q is a %s tool, not a script tool: a folder holds a script tool. Edit this one with tool_def(action=\"update\")", name, effectiveTempToolMode(existing))
	}
	m, err := writeToolFolder(abs, existing, toolFingerprint(existing))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Checked out tool %q into %s/: tool.json, %s, %d helper file(s), NOTES.md. Read NOTES.md first. Edit the files (workspace write, or workspace edit for a few lines), try it with tool_def(action=\"run\", dir=%q, args={...}), and save it with tool_def(action=\"publish\", dir=%q).",
		existing.Name, rel, m.Script, len(existing.WorkspaceFiles), rel, rel), nil
}

// writeToolFolder writes a script tool into a folder: tool.json, the script,
// its helpers and NOTES.md. base is the live state the folder holds
// (toolFingerprint), or "" when it holds none (an unpacked bundle). Shared by
// checkout and unpack.
func writeToolFolder(abs string, existing TempTool, base string) (toolFolderManifest, error) {
	write := func(file string, data []byte) error {
		p := filepath.Join(abs, file)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		return os.WriteFile(p, data, 0o644)
	}
	m := toolFolderManifest{Name: existing.Name, Description: existing.Description, Category: existing.Category,
		Script: chFirstStr(existing.ScriptName, "script.py"), HookCapabilities: existing.HookCapabilities,
		TimeoutSec: existing.TimeoutSec, StatePath: existing.StatePath, RawNetwork: existing.RawNetwork, ConfirmInChat: existing.ConfirmInChat}
	if b, err := json.Marshal(existing.Params); err == nil {
		m.Params = b
	}
	req := append([]string{}, existing.Required...)
	m.Required = &req
	// Kept as stored: it may pass params some way inference would not. A
	// script renamed in tool.json needs it changed too, and create says so.
	m.CommandTemplate = existing.CommandTemplate
	manifest, _ := json.MarshalIndent(m, "", "  ")
	files := map[string][]byte{toolManifestFile: manifest, m.Script: []byte(existing.ScriptBody), toolNotesFile: []byte(existing.Notes)}
	for _, wf := range existing.WorkspaceFiles {
		files[cleanBase(wf.Path)] = []byte(wf.Content)
	}
	for f, data := range files {
		if err := write(f, data); err != nil {
			return m, err
		}
	}
	if base != "" {
		if err := write(toolFolderBaseFile, []byte(base)); err != nil {
			return m, err
		}
	} else {
		os.Remove(filepath.Join(abs, toolFolderBaseFile))
	}
	return m, nil
}

// toolFolderRead loads a folder into create/update arguments.
func toolFolderRead(abs, rel string) (map[string]any, toolFolderManifest, error) {
	var m toolFolderManifest
	raw, err := os.ReadFile(filepath.Join(abs, toolManifestFile))
	if err != nil {
		return nil, m, fmt.Errorf("no %s in %s: start one with tool_def(action=\"checkout\", name=...)", toolManifestFile, rel)
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, m, fmt.Errorf("%s/%s is not valid JSON: %v", rel, toolManifestFile, err)
	}
	if strings.TrimSpace(m.Name) == "" {
		return nil, m, fmt.Errorf("%s/%s needs a name", rel, toolManifestFile)
	}
	if m.Script == "" {
		m.Script = "script.py"
	}
	if strings.ContainsAny(m.Script, `/\`) {
		return nil, m, fmt.Errorf("%s/%s: script is a filename in the folder, not a path", rel, toolManifestFile)
	}
	body, err := os.ReadFile(filepath.Join(abs, m.Script))
	if err != nil {
		return nil, m, fmt.Errorf("%s/%s names the script %s, which is not in the folder", rel, toolManifestFile, m.Script)
	}
	args := map[string]any{
		"name": m.Name, "description": m.Description, "mode": TempToolModeShell,
		"script_body": string(body), "script_name": m.Script,
	}
	if len(m.Params) > 0 {
		var p any
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return nil, m, fmt.Errorf("%s/%s: params is not valid JSON: %v", rel, toolManifestFile, err)
		}
		args["params"] = p
	}
	if m.Required != nil {
		req := make([]any, len(*m.Required))
		for i, r := range *m.Required {
			req[i] = r
		}
		args["required"] = req
	}
	for k, v := range map[string]string{"category": m.Category, "command_template": m.CommandTemplate, "state_path": m.StatePath} {
		if v != "" {
			args[k] = v
		}
	}
	if m.HookCapabilities != nil {
		caps := make([]any, len(m.HookCapabilities))
		for i, c := range m.HookCapabilities {
			caps[i] = c
		}
		args["hook_capabilities"] = caps
	}
	if m.TimeoutSec > 0 {
		args["timeout_sec"] = float64(m.TimeoutSec)
	}
	if m.RawNetwork {
		args["raw_network"] = true
	}
	if m.ConfirmInChat {
		args["confirm_in_chat"] = true
	}
	if notes, err := os.ReadFile(filepath.Join(abs, toolNotesFile)); err == nil {
		args["notes"] = strings.TrimSpace(string(notes))
	}
	// Every other plain file beside the script is a helper it may import.
	entries, _ := os.ReadDir(abs)
	var helpers []any
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || strings.HasPrefix(n, ".") || n == toolManifestFile || n == toolNotesFile || n == m.Script {
			continue
		}
		b, err := os.ReadFile(filepath.Join(abs, n))
		if err != nil {
			return nil, m, err
		}
		helpers = append(helpers, map[string]any{"path": n, "content": string(b)})
	}
	sort.Slice(helpers, func(i, j int) bool {
		return helpers[i].(map[string]any)["path"].(string) < helpers[j].(map[string]any)["path"].(string)
	})
	if helpers == nil {
		helpers = []any{}
	}
	args["workspace_files"] = helpers
	return args, m, nil
}

// toolRun runs the folder's script on sample args, saving nothing.
func toolRun(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil {
		return "", errors.New("requires a session")
	}
	abs, rel, err := toolFolderDir(sess, args)
	if err != nil {
		return "", err
	}
	in, m, err := toolFolderRead(abs, rel)
	if err != nil {
		return "", err
	}
	tt, err := folderTempTool(in, m)
	if err != nil {
		return "", fmt.Errorf("%s: %w", rel, err)
	}
	var runs []map[string]any
	if a, ok := args["args"].(map[string]any); ok {
		runs = append(runs, a)
	}
	for _, c := range casesArg(args["cases"], m.Cases) {
		runs = append(runs, c)
	}
	if len(runs) == 0 {
		runs = append(runs, map[string]any{})
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Ran %s from %s/ (not published).\n", m.Script, rel)
	failed := 0
	for i, a := range runs {
		shown, _ := json.Marshal(a)
		out, err := DispatchTempToolDirect(sess, tt, a)
		status := "ok"
		if err != nil {
			status, failed = "FAILED: "+err.Error(), failed+1
		}
		if r := []rune(out); len(r) > 2000 {
			out = string(r[:2000]) + "... (cut)"
		}
		fmt.Fprintf(&b, "\n%d. args %s: %s\n%s\n", i+1, shown, status, strings.TrimSpace(out))
	}
	if failed > 0 {
		b.WriteString("\nFix the script (workspace edit) and run it again.")
	} else {
		b.WriteString("\nIt ran. When the output is right, publish the folder.")
	}
	// What the script printed is whatever it fetched: third-party content.
	return UntrustedToolResultFence + b.String(), nil
}

// casesArg is the args of each case given, else of the folder's own.
func casesArg(raw any, own []map[string]any) []map[string]any {
	var list []map[string]any
	if arr, ok := raw.([]any); ok {
		for _, c := range arr {
			if m, ok := c.(map[string]any); ok {
				list = append(list, m)
			}
		}
	} else {
		list = own
	}
	var out []map[string]any
	for _, c := range list {
		if a, ok := c["args"].(map[string]any); ok {
			out = append(out, a)
		}
	}
	return out
}

// folderTempTool builds the tool a run dispatches, the way create would.
func folderTempTool(in map[string]any, m toolFolderManifest) (*TempTool, error) {
	params, err := parseParamsArg(in["params"])
	if err != nil {
		return nil, fmt.Errorf("params: %w", err)
	}
	body := in["script_body"].(string)
	cmd := strings.TrimSpace(m.CommandTemplate)
	if cmd == "" {
		order, _ := paramNamesInDefinitionOrder(in["params"])
		cmd = inferCommandTemplate(m.Script, body, order)
	}
	if cmd == "" {
		return nil, fmt.Errorf("no command to run %s: give tool.json a command_template, or name the script with a .py/.sh/.js/.rb extension", m.Script)
	}
	caps := append([]string{}, m.HookCapabilities...)
	for _, def := range []string{"fetch", "log", "browse_page"} {
		if !containsStr(caps, def) {
			caps = append(caps, def)
		}
	}
	files, err := workspaceFilesArg(in["workspace_files"])
	if err != nil {
		return nil, err
	}
	return &TempTool{
		Name: m.Name, Mode: TempToolModeShell, Params: params, CommandTemplate: cmd,
		ScriptBody: body, ScriptName: m.Script, CanonicalScriptName: canonicalScriptName(m.Name, m.Script, body),
		HookCapabilities: caps, WorkspaceFiles: files, TimeoutSec: m.TimeoutSec, StatePath: m.StatePath, RawNetwork: m.RawNetwork,
	}, nil
}

// toolPublish saves the folder as the tool: created if new, updated if not,
// through the same create / update and checks, then tried with its cases.
func toolPublish(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil {
		return "", errors.New("requires a session")
	}
	abs, rel, err := toolFolderDir(sess, args)
	if err != nil {
		return "", err
	}
	in, m, err := toolFolderRead(abs, rel)
	if err != nil {
		return "", err
	}
	if len(m.Cases) > 0 {
		cases := make([]any, len(m.Cases))
		for i, c := range m.Cases {
			cases[i] = c
		}
		in["cases"] = cases
	}
	live, exists := loadExistingToolRecord(sess, m.Name)
	var out string
	if exists {
		if effectiveTempToolMode(live) != TempToolModeShell {
			return "", fmt.Errorf("%q is a %s tool, and %s/ is a script tool: publishing would replace one kind with the other. Use a different name in %s, or delete the live tool first", m.Name, effectiveTempToolMode(live), rel, toolManifestFile)
		}
		if err := toolFolderStale(abs, rel, live); err != nil && !BoolArg(args, "overwrite_live") {
			return "", err
		}
		delete(in, "mode")
		out, err = toolDefUpdate(in, sess)
	} else {
		out, err = toolDefCreate(in, sess)
	}
	if err != nil {
		return "", fmt.Errorf("%s not published: %w", rel, err)
	}
	if saved, ok := loadExistingToolRecord(sess, m.Name); ok {
		os.WriteFile(filepath.Join(abs, toolFolderBaseFile), []byte(toolFingerprint(saved)), 0o644)
	}
	return fmt.Sprintf("Published %s/ to tool %q.\n\n%s", rel, m.Name, out), nil
}

// toolFingerprint is a digest of what a tool is, as stored: what its folder
// holds and a publish writes.
func toolFingerprint(tt TempTool) string {
	b, _ := json.Marshal([]any{tt.Description, tt.Category, tt.Params, tt.Required, tt.CommandTemplate,
		tt.ScriptBody, tt.ScriptName, tt.HookCapabilities, tt.WorkspaceFiles, tt.Notes, tt.TimeoutSec,
		tt.StatePath, tt.RawNetwork, tt.ConfirmInChat})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// toolFolderStale says why publishing would undo a change made to the live
// tool outside the folder, or nil when the folder holds the tool as it is.
func toolFolderStale(abs, rel string, live TempTool) error {
	base, err := os.ReadFile(filepath.Join(abs, toolFolderBaseFile))
	if err == nil && strings.TrimSpace(string(base)) == toolFingerprint(live) {
		return nil
	}
	what := "the live tool was changed since this folder was checked out or last published"
	if err != nil {
		what = "this folder has no record of being checked out from the live tool (it was started new while a tool of this name exists)"
	}
	return fmt.Errorf("NOT PUBLISHED: %s, and publishing %s/ would undo what is live and not in the folder. Either make those changes in the folder's files too and publish with overwrite_live=true, or take the live tool back into the folder with tool_def(action=\"checkout\", name=%q, overwrite=true) (that replaces the folder's files, so redo any change in them that is not live) and publish from there", what, rel, live.Name)
}

// toolFolderOwnsEdit refuses a live edit of a tool whose folder holds it as it
// is: the change belongs in the folder, or its next publish undoes it.
func toolFolderOwnsEdit(sess *ToolSession, name string) error {
	if sess == nil || name == "" || sess.WorkspaceDir == "" {
		return nil
	}
	rel := name + ".tool"
	abs, err := ResolveWorkspacePath(sess.WorkspaceDir, rel)
	if err != nil {
		return nil
	}
	live, ok := loadExistingToolRecord(sess, name)
	if !ok || toolFolderStale(abs, rel, live) != nil {
		return nil
	}
	return fmt.Errorf("NOT CHANGED: tool %q is built in %s/, which holds it as it is live: make this change in the folder's files (workspace edit on %s/%s, %s, %s) and tool_def(action=\"publish\", dir=%q). An edit made here would be undone by the next publish of the folder", name, rel, rel, chFirstStr(live.ScriptName, "script.py"), toolManifestFile, toolNotesFile, rel)
}

func chFirstStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
