// LLM-facing workspace tool. Single grouped tool covering both
// workspace lifecycle (create / use / list / pin / unpin / delete /
// info) and file/shell operations inside the active workspace
// (ls / cat / write / rm / run). Replaces the older `local` + `workspace`
// split — both layers share the same sandbox model, so one tool keeps
// the mental model tight and eliminates the "which one?" speculation
// problem.
//
// Mental model: ephemeral by default ("with TemporaryDirectory()"),
// pinned when the user wants to keep work product around. Auto-cleaned
// by the core reconciler when unpinned and idle > 24h. File ops are
// scoped to whatever workspace is currently active; switch via `use`
// or mint a fresh one via `create`.

package workspace

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/files"
)

const (
	// runTimeout caps wall-clock time for shell commands invoked via
	// action=run. Long-running commands are killed.
	runTimeout = 90 * time.Second

	// runMaxOutput is the per-command output cap, beyond which output is
	// truncated and a notice is appended so the LLM knows to narrow.
	runMaxOutput = 10000
)

func init() {
	gt := NewGroupedTool("workspace",
		"Sandboxed scratch space for tasks that need files or shell. Lifecycle actions (create / use / list / pin / unpin / delete / info) manage the workspace itself; file actions (ls / cat / write / rm / run) operate inside the active workspace. Workspaces are ephemeral by default — auto-cleaned 24h after last use; pin to keep. Use only when the user has explicitly asked to write a script, run code, build a custom tool, or save work product — for information lookup use web_search or fetch_url instead.")
	gt.SetFrameworkTool(true) // owns the universal attach action; auto-wired by the runner

	// ---- lifecycle actions ----

	gt.AddAction("create", &GroupedToolAction{
		Description: "Create a new workspace and switch the session's active path to it. All subsequent file-writing tools (fetch_url save_to, generate_image, workspace write, etc.) put their output here for the rest of this turn. Returns the workspace id. Optional: pin=true creates it pinned (survives session end + idle timeout); name is a human-friendly label.",
		Params: map[string]ToolParam{
			"name": {Type: "string", Description: "Human-friendly label for the workspace (snake_case recommended). Optional but useful for `list` and admin views."},
			"pin":  {Type: "boolean", Description: "If true, creates the workspace as pinned — won't be auto-cleaned. Default false (ephemeral)."},
		},
		Caps:    []Capability{CapWrite},
		Handler: handleCreate,
	})

	gt.AddAction("use", &GroupedToolAction{
		Description: "Switch the session's active workspace to an existing one. Subsequent file actions read/write here for the rest of this turn. Touches the workspace's last-used time so the cleanup reconciler doesn't reap it.",
		Params: map[string]ToolParam{
			"id": {Type: "string", Description: "Workspace ID (from list or create)."},
		},
		Required: []string{"id"},
		Caps:     []Capability{CapWrite},
		Handler:  handleUse,
	})

	gt.AddAction("list", &GroupedToolAction{
		Description: "List your workspaces (ephemeral + pinned). Shows id, name, pinned status, size, last-used age.",
		Params:      map[string]ToolParam{},
		Caps:        []Capability{CapRead},
		Handler:     handleList,
	})

	gt.AddAction("pin", &GroupedToolAction{
		Description: "Mark a workspace as pinned — survives session end and is not auto-cleaned by the idle reconciler. Use when you want to keep work product around for a future session.",
		Params: map[string]ToolParam{
			"id": {Type: "string", Description: "Workspace ID."},
		},
		Required: []string{"id"},
		Caps:     []Capability{CapWrite},
		Handler:  func(args map[string]any, sess *ToolSession) (string, error) { return setPinned(args, sess, true) },
	})

	gt.AddAction("unpin", &GroupedToolAction{
		Description: "Revert a workspace to ephemeral. The cleanup reconciler will reap it after 24h idle.",
		Params: map[string]ToolParam{
			"id": {Type: "string", Description: "Workspace ID."},
		},
		Required: []string{"id"},
		Caps:     []Capability{CapWrite},
		Handler:  func(args map[string]any, sess *ToolSession) (string, error) { return setPinned(args, sess, false) },
	})

	gt.AddAction("delete", &GroupedToolAction{
		Description: "Permanently delete a workspace and all its files. Irreversible. Use when you're done with a task and don't want to wait for the cleanup reconciler. To remove a single file inside the active workspace use rm instead.",
		Params: map[string]ToolParam{
			"id": {Type: "string", Description: "Workspace ID."},
		},
		Required:     []string{"id"},
		Caps:         []Capability{CapWrite},
		NeedsConfirm: true,
		Handler:      handleDelete,
	})

	gt.AddAction("info", &GroupedToolAction{
		Description: "Detailed info on one workspace: metadata, on-disk size, age. Useful before deciding whether to delete.",
		Params: map[string]ToolParam{
			"id": {Type: "string", Description: "Workspace ID."},
		},
		Required: []string{"id"},
		Caps:     []Capability{CapRead},
		Handler:  handleInfo,
	})

	// ---- file-ops actions (scoped to the active workspace) ----

	gt.AddAction("ls", &GroupedToolAction{
		Description: "List entries in the active workspace. Use \".\" for the workspace root.",
		Params: map[string]ToolParam{
			"path": {Type: "string", Description: "Workspace-relative directory path. Default \".\""},
		},
		Caps: []Capability{CapRead},
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			return (&files.ListDirectoryTool{}).RunWithSession(args, sess)
		},
	})

	gt.AddAction("cat", &GroupedToolAction{
		Description: "Read a file from the active workspace as text. Returns up to 64 KB; longer files are truncated with a notice. For large files (logs, JSON dumps, spilled tool results) prefer the targeted query actions — head / tail / read_lines / grep / stat — which return only the slice you need rather than the whole file.",
		Params: map[string]ToolParam{
			"path": {Type: "string", Description: "Workspace-relative path to read."},
		},
		Required: []string{"path"},
		Caps:     []Capability{CapRead},
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			return (&files.ReadFileTool{}).RunWithSession(args, sess)
		},
	})

	gt.AddAction("head", &GroupedToolAction{
		Description: "Return the first N lines of a workspace file (default 50). Cheap targeted slice — use this when you need the start of a log, the header of a CSV, or just want to see the shape of a file without reading the whole thing.",
		Params: map[string]ToolParam{
			"path":  {Type: "string", Description: "Workspace-relative path to read."},
			"lines": {Type: "integer", Description: "Number of lines to return (default 50)."},
		},
		Required: []string{"path"},
		Caps:     []Capability{CapRead},
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			return files.HeadFileWS(sess, StringArg(args, "path"), intArgOr(args, "lines", 0))
		},
	})

	gt.AddAction("tail", &GroupedToolAction{
		Description: "Return the last N lines of a workspace file (default 50). Use for log tails, recently-appended output, or the final summary of a tool spill.",
		Params: map[string]ToolParam{
			"path":  {Type: "string", Description: "Workspace-relative path to read."},
			"lines": {Type: "integer", Description: "Number of lines to return (default 50)."},
		},
		Required: []string{"path"},
		Caps:     []Capability{CapRead},
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			return files.TailFileWS(sess, StringArg(args, "path"), intArgOr(args, "lines", 0))
		},
	})

	gt.AddAction("read_lines", &GroupedToolAction{
		Description: "Return a specific line range from a workspace file. 1-indexed, inclusive on both ends. Use after stat / grep tells you roughly where to look; lets you pull exactly the slice you need without dragging the rest into context.",
		Params: map[string]ToolParam{
			"path":  {Type: "string", Description: "Workspace-relative path to read."},
			"start": {Type: "integer", Description: "First line to return (1-indexed)."},
			"end":   {Type: "integer", Description: "Last line to return (inclusive). Capped at start+1000."},
		},
		Required: []string{"path", "start"},
		Caps:     []Capability{CapRead},
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			return files.ReadLinesWS(sess, StringArg(args, "path"), intArgOr(args, "start", 0), intArgOr(args, "end", 0))
		},
	})

	gt.AddAction("grep", &GroupedToolAction{
		Description: "Search a workspace file for a regex pattern. Returns matching lines with line numbers; optional `context` includes lines before/after each match (max 5). Right tool for finding specific records inside a spilled log, a large JSON dump, or any text you DON'T want to read end-to-end. Pattern is RE2 (Go's regexp) — no PCRE-only constructs.",
		Params: map[string]ToolParam{
			"path":        {Type: "string", Description: "Workspace-relative path to search."},
			"pattern":     {Type: "string", Description: "RE2 regex to match against each line."},
			"context":     {Type: "integer", Description: "Lines of context before and after each match (default 0, max 5)."},
			"max_matches": {Type: "integer", Description: "Stop after this many matches (default 200)."},
		},
		Required: []string{"path", "pattern"},
		Caps:     []Capability{CapRead},
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			return files.GrepFileWS(sess, StringArg(args, "path"), StringArg(args, "pattern"),
				intArgOr(args, "context", 0), intArgOr(args, "max_matches", 0))
		},
	})

	gt.AddAction("stat", &GroupedToolAction{
		Description: "Inspect a workspace file: size, mtime, line count, and a kind hint (json-like / log-lines / csv-like / html / xml-like / text / binary / empty). Call FIRST when you encounter an unfamiliar file (e.g. a spilled tool result) — the kind hint tells you which query action to reach for next (json-like → grep for the field you want; log-lines → tail; csv-like → head for the header).",
		Params: map[string]ToolParam{
			"path": {Type: "string", Description: "Workspace-relative path to inspect."},
		},
		Required: []string{"path"},
		Caps:     []Capability{CapRead},
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			return files.StatFileWS(sess, StringArg(args, "path"))
		},
	})

	gt.AddAction("write", &GroupedToolAction{
		Description: "Write content to a file in the active workspace (creates or overwrites). Auto-mints a workspace on first call if none is active. Use to drop scripts you want to test via run, or to save work product you'll attach later.",
		Params: map[string]ToolParam{
			"path":    {Type: "string", Description: "Workspace-relative path."},
			"content": {Type: "string", Description: "Bytes to write."},
		},
		Required:     []string{"path", "content"},
		Caps:         []Capability{CapWrite},
		NeedsConfirm: true,
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			return (&files.WriteFileTool{}).RunWithSession(args, sess)
		},
	})

	gt.AddAction("rm", &GroupedToolAction{
		Description: "Delete a single file inside the active workspace. Rejects paths outside the workspace. For removing a whole workspace use delete instead.",
		Params: map[string]ToolParam{
			"path": {Type: "string", Description: "Workspace-relative path to delete."},
		},
		Required:     []string{"path"},
		Caps:         []Capability{CapWrite},
		NeedsConfirm: true,
		Handler:      handleRm,
	})

	gt.AddAction("run", &GroupedToolAction{
		Description: "Run a shell command inside the active workspace via bwrap. The workspace is the only writable path; reads outside silently fail. Auto-mints a workspace if none is active. 90s timeout, output capped at 10KB. NOTE: each call requires user confirmation — use sparingly. For just CHECKING whether a binary exists (e.g. `command -v ffmpeg`), call workspace(action=\"probe\", name=\"ffmpeg\") instead — no-confirmation, validated-input, purpose-built for that check.",
		Params: map[string]ToolParam{
			"command": {Type: "string", Description: "Shell command to execute. Standard sh -c semantics — pipes, redirects, quoting work normally."},
			"env":     {Type: "object", Description: "Optional {\"KEY\":\"value\"} map of environment variables exposed to the command — reachable as $KEY in shell or os.environ.get(\"KEY\") in Python. Use to feed a debug script the same inputs a registered shell tool would receive as params."},
		},
		Required:     []string{"command"},
		Caps:         []Capability{CapExecute, CapRead, CapWrite, CapNetwork},
		NeedsConfirm: true,
		Handler:      handleRun,
	})

	gt.AddAction("probe", &GroupedToolAction{
		Description: "Check whether a binary is available in the shell-mode tool sandbox. Returns the path if found, or a 'not available' message if not. Use this BEFORE authoring a shell-mode tool that depends on a non-POSIX binary (ImageMagick's `convert`, `ffmpeg`, `yt-dlp`, etc.) — if the probe says not available, the tool will fail at dispatch, so pivot to a different design. Safe and cheap; no user confirmation required (binary name is validated as identifier-only).",
		Params: map[string]ToolParam{
			"name": {Type: "string", Description: "Binary name to probe (e.g. \"ffmpeg\", \"convert\", \"yt-dlp\", \"python3\"). Identifier-only: letters, digits, _, -, +, ."},
		},
		Required: []string{"name"},
		Caps:     []Capability{CapRead},
		Handler:  handleProbe,
	})

	// ---- delivery + inspection actions ----

	gt.AddAction("attach", &GroupedToolAction{
		Description: "Deliver a file from the active workspace to the user as an attachment. This is the ONLY action that puts a file in front of the user — every other action just stages or inspects locally. Any file the workspace contains can be attached, regardless of which tool produced it (find_image / fetch_image / generate_image / workspace write / a Builder-authored shell tool / etc.). After this call the file ships with your reply. Cap: 20MB per file.",
		Params: map[string]ToolParam{
			"path":    {Type: "string", Description: "Workspace-relative filename to deliver (e.g. \"meme.jpg\", \"report.pdf\")."},
			"name":    {Type: "string", Description: "Optional display name shown to the user. Defaults to the filename."},
			"cleanup": {Type: "boolean", Description: "If true, deletes the file from the workspace after successful delivery. Default false (keep). Producer tools that return one-shot files (find_image, fetch_image, generate_image) recommend cleanup=true — workspace stays tidy."},
		},
		Required: []string{"path"},
		Caps:     []Capability{CapRead, CapWrite},
		Handler:  handleAttach,
	})

	gt.AddAction("view_image", &GroupedToolAction{
		Description: "Run a vision-LLM look at an image in your workspace and return a description. Use to verify a file matches what you want before attaching, or to extract details from it. The image must be image/* mime.",
		Params: map[string]ToolParam{
			"path":   {Type: "string", Description: "Workspace-relative path to the image."},
			"prompt": {Type: "string", Description: "Optional prompt to focus the vision-LLM (e.g. 'is this a cat?'). Defaults to a neutral 'describe this image' request."},
		},
		Required: []string{"path"},
		Caps:     []Capability{CapRead, CapNetwork},
		Handler:  handleViewImage,
	})

	RegisterChatTool(gt)
}

// ----------------------------------------------------------------------
// handlers
// ----------------------------------------------------------------------

func handleCreate(args map[string]any, sess *ToolSession) (string, error) {
	owner := ownerFor(sess)
	if owner == "" {
		return "", fmt.Errorf("managed workspaces require an authenticated session (no owner)")
	}
	name := strings.TrimSpace(StringArg(args, "name"))
	pin := boolArg(args, "pin")
	w, dir, err := CreateManagedWorkspace(name, owner, sessionIDFor(sess), pin)
	if err != nil {
		return "", err
	}
	// Switch the session's active workspace dir to the new one for
	// the remainder of this turn. Tools that resolve paths against
	// sess.WorkspaceDir will land here automatically. WorkspaceID is
	// captured so tool_def(persist=true) can bind the tool to this
	// workspace at create time.
	if sess != nil {
		sess.WorkspaceDir = dir
		sess.WorkspaceID = w.ID
	}
	pinNote := ""
	if w.Pinned {
		pinNote = " (PINNED — survives session end)"
	}
	return fmt.Sprintf("Workspace created (id=%s, name=%q)%s. Active for this session — file-writing tools will land here. Path: %s",
		w.ID, w.Name, pinNote, dir), nil
}

func handleUse(args map[string]any, sess *ToolSession) (string, error) {
	id := strings.TrimSpace(StringArg(args, "id"))
	w, ok := LoadManagedWorkspace(id)
	if !ok {
		return "", fmt.Errorf("workspace %q not found", id)
	}
	if w.Owner != ownerFor(sess) {
		return "", fmt.Errorf("workspace %q is not yours", id)
	}
	dir, err := ManagedWorkspaceDir(w.Owner, w.ID)
	if err != nil {
		return "", err
	}
	if sess != nil {
		sess.WorkspaceDir = dir
		sess.WorkspaceID = w.ID
	}
	TouchManagedWorkspace(id)
	return fmt.Sprintf("Switched active workspace to %q (id=%s, path=%s).", w.Name, w.ID, dir), nil
}

func handleList(args map[string]any, sess *ToolSession) (string, error) {
	owner := ownerFor(sess)
	if owner == "" {
		return "", fmt.Errorf("workspaces require an authenticated session")
	}
	ws := ListManagedWorkspaces(owner)
	if len(ws) == 0 {
		return "No workspaces — use action=create to mint one.", nil
	}
	sort.Slice(ws, func(i, j int) bool {
		// Pinned first, then most-recently-used.
		if ws[i].Pinned != ws[j].Pinned {
			return ws[i].Pinned
		}
		return ws[i].LastUsedAt.After(ws[j].LastUsedAt)
	})
	var b strings.Builder
	for _, w := range ws {
		size := ManagedWorkspaceSize(w.Owner, w.ID)
		state := "ephemeral"
		if w.Pinned {
			state = "PINNED"
		}
		idle := time.Since(w.LastUsedAt).Round(time.Minute)
		fmt.Fprintf(&b, "- id=%s  name=%q  %s  size=%dKB  idle=%s\n",
			w.ID, w.Name, state, size/1024, idle)
	}
	return b.String(), nil
}

func setPinned(args map[string]any, sess *ToolSession, pinned bool) (string, error) {
	id := strings.TrimSpace(StringArg(args, "id"))
	w, ok := LoadManagedWorkspace(id)
	if !ok {
		return "", fmt.Errorf("workspace %q not found", id)
	}
	if w.Owner != ownerFor(sess) {
		return "", fmt.Errorf("workspace %q is not yours", id)
	}
	if err := SetManagedWorkspacePinned(id, pinned); err != nil {
		return "", err
	}
	state := "ephemeral"
	if pinned {
		state = "pinned"
	}
	return fmt.Sprintf("Workspace %q is now %s.", w.Name, state), nil
}

func handleDelete(args map[string]any, sess *ToolSession) (string, error) {
	id := strings.TrimSpace(StringArg(args, "id"))
	w, ok := LoadManagedWorkspace(id)
	if !ok {
		return "", fmt.Errorf("workspace %q not found", id)
	}
	if w.Owner != ownerFor(sess) {
		return "", fmt.Errorf("workspace %q is not yours", id)
	}
	if err := DeleteManagedWorkspace(id); err != nil {
		return "", err
	}
	// If the deleted workspace was the session's active path, blank it
	// so subsequent tool calls don't try to write into a missing dir.
	dir, _ := ManagedWorkspaceDir(w.Owner, w.ID)
	if sess != nil && sess.WorkspaceDir == dir {
		sess.WorkspaceDir = ""
	}
	return fmt.Sprintf("Workspace %q deleted.", w.Name), nil
}

func handleInfo(args map[string]any, sess *ToolSession) (string, error) {
	id := strings.TrimSpace(StringArg(args, "id"))
	w, ok := LoadManagedWorkspace(id)
	if !ok {
		return "", fmt.Errorf("workspace %q not found", id)
	}
	if w.Owner != ownerFor(sess) {
		return "", fmt.Errorf("workspace %q is not yours", id)
	}
	size := ManagedWorkspaceSize(w.Owner, w.ID)
	dir, _ := ManagedWorkspaceDir(w.Owner, w.ID)
	out, _ := json.MarshalIndent(map[string]any{
		"id":           w.ID,
		"name":         w.Name,
		"owner":        w.Owner,
		"pinned":       w.Pinned,
		"session_id":   w.SessionID,
		"created_at":   w.CreatedAt.UTC().Format(time.RFC3339),
		"last_used_at": w.LastUsedAt.UTC().Format(time.RFC3339),
		"size_bytes":   size,
		"path":         dir,
	}, "", "  ")
	return string(out), nil
}

// ----------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------

func ownerFor(sess *ToolSession) string {
	if sess == nil {
		return ""
	}
	return sess.Username
}

// sessionIDFor extracts a stable session identifier from the ToolSession.
// Prefers ChatSessionID (chat app) then RoutingTarget (for phantom convs);
// returns empty when neither is set, in which case the workspace is
// treated as cross-session by default.
func sessionIDFor(sess *ToolSession) string {
	if sess == nil {
		return ""
	}
	if sess.ChatSessionID != "" {
		return "chat:" + sess.ChatSessionID
	}
	if sess.RoutingTarget != "" {
		return sess.RoutingTarget
	}
	return ""
}

// intArgOr extracts an integer-valued arg, accepting the common
// shapes LLMs emit (real int, JSON-decoded float64, stringified
// digits). Returns def on missing/invalid input.
func intArgOr(args map[string]any, key string, def int) int {
	v, ok := args[key]
	if !ok || v == nil {
		return def
	}
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(t), "%d", &n); err == nil {
			return n
		}
	}
	return def
}

func boolArg(args map[string]any, key string) bool {
	v, ok := args[key]
	if !ok || v == nil {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(strings.TrimSpace(t), "true")
	}
	return false
}

// ----------------------------------------------------------------------
// file-op handlers (rm / run)
// ----------------------------------------------------------------------

func handleRm(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil || sess.WorkspaceDir == "" {
		return "", fmt.Errorf("rm requires an active workspace (call workspace(action=create) first or workspace(action=use, id=...))")
	}
	rel := strings.TrimSpace(StringArg(args, "path"))
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	abs, err := ResolveWorkspacePath(sess.WorkspaceDir, rel)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat %q: %w", rel, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%q is a directory; use run with `rm -r` if you really need to remove a directory", rel)
	}
	if err := os.Remove(abs); err != nil {
		return "", fmt.Errorf("remove %q: %w", rel, err)
	}
	return fmt.Sprintf("Deleted %s.", filepath.Base(rel)), nil
}

func handleRun(args map[string]any, sess *ToolSession) (string, error) {
	if _, err := EnsureSessionWorkspace(sess); err != nil {
		return "", fmt.Errorf("run: %w", err)
	}
	cmd := strings.TrimSpace(StringArg(args, "command"))
	if cmd == "" {
		return "", fmt.Errorf("command is required")
	}
	// Optional env: {"KEY":"val"} exposed to the command as $KEY / os.environ —
	// so a debug run can supply the same variables a registered shell tool gets.
	var extraEnv map[string]string
	if raw, ok := args["env"].(map[string]any); ok && len(raw) > 0 {
		extraEnv = make(map[string]string, len(raw))
		for k, v := range raw {
			extraEnv[strings.TrimSpace(k)] = fmt.Sprint(v)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	// Run with the iterate-and-test hook attached so `from gohort
	// import fetch` works exactly as it does when this same script
	// gets dispatched later as a registered shell-mode tool. Without
	// this the script would raise HookError("GOHORT_HOOK_PATH not
	// set"), Builder would conclude fetch doesn't work in the shell,
	// and rewrite the tool wrongly. Capabilities: fetch / log /
	// browse_page — the common probe surface. secret:* and fetch_via:*
	// stay explicit so a tool that needs credentials still has to
	// declare them in its tool record.
	res := RunSandboxedShellWithHookEnv(ctx, cmd, sess.WorkspaceDir, sess,
		[]string{"fetch", "log", "browse_page"}, extraEnv)
	output := strings.TrimSpace(res.Output)
	if len(output) > runMaxOutput {
		totalLines := strings.Count(output, "\n") + 1
		truncated := output[:runMaxOutput]
		shown := strings.Count(truncated, "\n") + 1
		output = truncated + fmt.Sprintf(
			"\n... [TRUNCATED: showing lines 1–%d of %d total (%d chars).]",
			shown, totalLines, len(output))
	}
	if res.TimedOut {
		notice := fmt.Sprintf("\n[TIMED OUT after %s — command killed.]", runTimeout)
		if output == "" {
			return strings.TrimPrefix(notice, "\n"), nil
		}
		return output + notice, nil
	}
	if res.Err != nil {
		if output == "" {
			return fmt.Sprintf("[exit: %v — no output]", res.Err), nil
		}
		return output + fmt.Sprintf("\n[exit: %v]", res.Err), nil
	}
	return output, nil
}

// validProbeName: letters/digits/underscore/dash/dot/plus, length 1-64.
// Mirrors typical Unix binary naming and rejects anything that could
// inject shell metachars into the `command -v` invocation below.
var validProbeName = regexp.MustCompile(`^[a-zA-Z0-9_\-+.]{1,64}$`)

// handleProbe answers "is binary X installed in the sandbox?" without
// exposing arbitrary shell execution. Runs `command -v <validated>` in
// the same bubblewrap sandbox temp-tool dispatch uses, so what this
// reports matches what an authored tool would see at dispatch time.
// Safe to call without user confirmation: name is validated to
// identifier characters before reaching the shell.
//
// Replaces the standalone sandbox_probe tool — same logic, folded
// into workspace so the LLM's catalog has one fewer top-level entry.
func handleProbe(args map[string]any, _ *ToolSession) (string, error) {
	name := strings.TrimSpace(StringArg(args, "name"))
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	if !validProbeName.MatchString(name) {
		return "", fmt.Errorf("invalid binary name %q — must be identifier characters only (letters, digits, _, -, +, .)", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := "command -v " + name + " 2>/dev/null || true"
	res := RunSandboxedShellPipe(ctx, cmd, "")
	output := strings.TrimSpace(res.Output)
	if output == "" {
		return fmt.Sprintf("%q is NOT available in the sandbox. Pivot your design — either use a different binary or switch the tool's mode (api / pipeline / different shell tool).", name), nil
	}
	return fmt.Sprintf("%q is available at %s. Safe to use in shell-mode tools.", name, output), nil
}

// handleAttach delivers a workspace file to the user as an
// attachment. Routes by mime: image/* → sess.Images (inline-rendered
// in chat, bridged in phantom), audio/video → sess.Videos, anything
// else → sess.Files (download link in chat). Optional cleanup=true
// removes the file from the workspace after successful delivery —
// matches the one-shot lifecycle of producer-tool outputs (find /
// fetch / generate) where the file is no longer needed.
// AttachWorkspaceFile reads a file from the active session workspace,
// validates it, queues it onto the appropriate delivery channel
// (sess.Images / sess.Videos / sess.Files based on detected MIME), and
// optionally deletes the source after a successful queue. Returns a
// human-readable summary suitable for display in a tool result OR a log
// line.
//
// Shared between workspace(action="attach") and any reply-marker path
// (e.g. phantom's `[ATTACH: filename]` parser) so the plumbing — size
// cap, MIME routing, base64 encoding, cleanup semantics — is identical
// regardless of which surface invoked the attach.
func AttachWorkspaceFile(sess *ToolSession, relPath, displayName string, cleanup bool) (string, error) {
	if sess == nil {
		return "", fmt.Errorf("attach requires a session")
	}
	ws, err := EnsureSessionWorkspace(sess)
	if err != nil {
		return "", fmt.Errorf("session workspace unavailable: %w", err)
	}
	relPath = strings.TrimSpace(relPath)
	if relPath == "" {
		return "", fmt.Errorf("path is required")
	}
	abs, err := ResolveWorkspacePath(ws, relPath)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat %q: %w", relPath, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%q is a directory, not a file", relPath)
	}
	const maxAttach = 20 * 1024 * 1024
	if info.Size() > maxAttach {
		return "", fmt.Errorf("file too large: %d bytes (cap %d)", info.Size(), maxAttach)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("read %q: %w", relPath, err)
	}
	mime := http.DetectContentType(data)
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = filepath.Base(relPath)
	}
	channel := "file"
	switch {
	case strings.HasPrefix(mime, "image/"):
		sess.AppendImage(base64.StdEncoding.EncodeToString(data))
		channel = "image"
	case strings.HasPrefix(mime, "audio/"), strings.HasPrefix(mime, "video/"):
		sess.AppendVideo(base64.StdEncoding.EncodeToString(data))
		channel = "audio/video"
	default:
		sess.AppendFile(FileAttachment{
			Name:     displayName,
			MimeType: mime,
			Data:     base64.StdEncoding.EncodeToString(data),
			Size:     len(data),
		})
	}
	var suffix string
	if cleanup {
		if err := os.Remove(abs); err != nil {
			suffix = fmt.Sprintf(" (cleanup failed: %v)", err)
		} else {
			suffix = " (cleaned up from workspace)"
		}
	}
	return fmt.Sprintf("Attached %q (%s, %s) via %s channel.%s",
		displayName, mime, humanSize(info.Size()), channel, suffix), nil
}

func handleAttach(args map[string]any, sess *ToolSession) (string, error) {
	return AttachWorkspaceFile(sess,
		StringArg(args, "path"),
		StringArg(args, "name"),
		boolArg(args, "cleanup"))
}

// handleViewImage runs the session LLM with vision on a workspace
// image file and returns its description. Used by the LLM to inspect
// a candidate before deciding to attach.
func handleViewImage(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil {
		return "", fmt.Errorf("view_image requires a session")
	}
	ws, err := EnsureSessionWorkspace(sess)
	if err != nil {
		return "", fmt.Errorf("session workspace unavailable: %w", err)
	}
	rel := strings.TrimSpace(StringArg(args, "path"))
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	abs, err := ResolveWorkspacePath(ws, rel)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("read %q: %w", rel, err)
	}
	mime := http.DetectContentType(data)
	if !strings.HasPrefix(mime, "image/") {
		return "", fmt.Errorf("%q is %s, not an image — use cat for text", rel, mime)
	}
	if sess.LLM == nil {
		return "", fmt.Errorf("view_image requires an LLM bound to the session")
	}
	prompt := strings.TrimSpace(StringArg(args, "prompt"))
	if prompt == "" {
		prompt = "Describe what's in this image briefly (1-2 sentences). Focus on subject, scene, anything notable."
	}
	resp, err := sess.LLM.Chat(context.Background(),
		[]Message{{Role: "user", Content: prompt, Images: [][]byte{data}}},
		WithCaller("workspace/view_image"),
		WithMaxRetries(0),
		WithThink(false),
	)
	if err != nil {
		return "", fmt.Errorf("vision-LLM call: %w", err)
	}
	if resp == nil || strings.TrimSpace(resp.Content) == "" {
		return "(no response from vision-LLM)", nil
	}
	return strings.TrimSpace(resp.Content), nil
}

// humanSize formats bytes for human-readable display in tool results.
func humanSize(n int64) string {
	const (
		kb = int64(1024)
		mb = kb * 1024
	)
	switch {
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
