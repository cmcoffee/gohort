package temptool

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// canonicalScriptName builds the on-disk filename for a tool's
// script_body. Format: "<tool_name>_<short_content_hash>.<ext>".
// The hash is a sha256 prefix (8 hex chars = ~32 bits, 4B unique
// values — collisions are astronomically unlikely in a single-
// operator workspace). Combined with the tool name, this gives:
//   - Readable filename (debug: "see get_meme_a4f2b8e1.py in
//     workspace, that's the get_meme tool's current script")
//   - Deterministic per content (same body → same filename, so
//     redeploy is idempotent: no file = write; matching file =
//     no-op; different file = different name, no collision)
//   - Drift detection (if a tool's record shows hash "a4f2b8e1"
//     but the file on disk hashes differently, content drifted —
//     redeploy from the record overwrites)
//   - Re-author safety (delete + recreate a tool with same name
//     but different script body gets a different filename; old
//     file isn't touched, no stale-content risk)
//
// Extension is derived from (in priority order):
//  1. The LLM's script_name suffix, if a recognized language ext.
//  2. A shebang line in script_body ("#!/bin/sh" → .sh, etc.).
//  3. Default .py (Python is the dominant pattern).
func canonicalScriptName(toolName, llmHint, body string) string {
	ext := ".py"
	// First try the LLM's hint suffix.
	if i := strings.LastIndexByte(llmHint, '.'); i >= 0 {
		suffix := strings.ToLower(llmHint[i:])
		switch suffix {
		case ".py", ".sh", ".bash", ".jq", ".awk", ".sed", ".pl", ".rb", ".js", ".ts":
			ext = suffix
		}
	}
	// Shebang as fallback / override when LLM gave no hint.
	if firstLine := body; firstLine != "" {
		if nl := strings.IndexByte(firstLine, '\n'); nl >= 0 {
			firstLine = firstLine[:nl]
		}
		switch {
		case strings.Contains(firstLine, "python"):
			ext = ".py"
		case strings.Contains(firstLine, "/sh"), strings.Contains(firstLine, "/bash"):
			ext = ".sh"
		case strings.Contains(firstLine, "/jq"):
			ext = ".jq"
		}
	}
	// Sanitize the tool name for filesystem use. Tool names should
	// already be snake_case validated, but defense in depth.
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return -1
	}, toolName)
	if safe == "" {
		safe = "tool"
	}
	// Short content hash — sha256 first 8 hex chars. Deterministic
	// per body content so identical scripts always map to the same
	// filename; different content forces a different filename and
	// avoids any chance of a stale on-disk file masking a real
	// update.
	sum := sha256.Sum256([]byte(body))
	hashSuffix := hex.EncodeToString(sum[:4])
	return safe + "_" + hashSuffix + ext
}

// scriptExtensions lists the file suffixes the framework treats as
// "this is a script" for command_template validation purposes. Used
// by missingWorkspaceScriptRefs to distinguish script references
// (where missing → tool will be silently broken) from arbitrary
// workspace_dir output paths like data.json or screenshot.png
// (which the command itself produces and shouldn't be expected to
// pre-exist).
var scriptExtensions = map[string]bool{
	".py": true, ".sh": true, ".bash": true, ".jq": true,
	".awk": true, ".sed": true, ".pl": true, ".rb": true,
	".js": true, ".ts": true,
}

// workspaceScriptRefRe captures {workspace_dir}/<path>.<ext>
// references. The path may contain word chars, hyphens, dots, and
// slashes (subdirectories allowed). Extension match is checked
// against scriptExtensions; anything else is treated as a non-script
// reference and skipped (false-positive avoidance).
var workspaceScriptRefRe = regexp.MustCompile(`\{workspace_dir\}/([A-Za-z0-9_./\-]+\.[A-Za-z0-9]+)`)

// missingWorkspaceScriptRefs scans cmd for {workspace_dir}/<script>
// patterns and returns the filenames whose extension marks them as
// scripts AND which do not exist on disk under workspaceDir. Empty
// workspaceDir or empty cmd returns nil (no validation possible).
// De-duplicates so a script referenced twice surfaces once in the
// error message.
func missingWorkspaceScriptRefs(cmd, workspaceDir string) []string {
	if cmd == "" || workspaceDir == "" {
		return nil
	}
	matches := workspaceScriptRefRe.FindAllStringSubmatch(cmd, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var missing []string
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		rel := m[1]
		if seen[rel] {
			continue
		}
		seen[rel] = true
		ext := strings.ToLower(filepath.Ext(rel))
		if !scriptExtensions[ext] {
			continue // arbitrary output path, not a script invocation
		}
		full := filepath.Join(workspaceDir, rel)
		if _, err := os.Stat(full); errors.Is(err, fs.ErrNotExist) {
			missing = append(missing, rel)
		}
	}
	return missing
}

// presentWorkspaceScriptRefs is the complement of missingWorkspaceScriptRefs:
// it returns the {workspace_dir}/<script> references whose extension marks
// them as scripts AND which DO exist on disk under workspaceDir. Used at
// authoring time to capture a local(write)-authored script back into the
// tool record (so it travels with export and survives workspace wipes).
// De-duplicated; preserves first-seen order.
func presentWorkspaceScriptRefs(cmd, workspaceDir string) []string {
	if cmd == "" || workspaceDir == "" {
		return nil
	}
	matches := workspaceScriptRefRe.FindAllStringSubmatch(cmd, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var present []string
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		rel := m[1]
		if seen[rel] {
			continue
		}
		seen[rel] = true
		ext := strings.ToLower(filepath.Ext(rel))
		if !scriptExtensions[ext] {
			continue // arbitrary output path, not a script invocation
		}
		full := filepath.Join(workspaceDir, rel)
		if info, err := os.Stat(full); err == nil && !info.IsDir() {
			present = append(present, rel)
		}
	}
	return present
}

// maxWorkspaceHelpers caps the transitive helper walk so a pathological
// import graph can't stuff an unbounded pile of files into a tool record.
const maxWorkspaceHelpers = 24

// pyImportRe matches Python `import foo` / `import foo, bar` /
// `from foo import x` / `from .foo import x` at the start of a (possibly
// indented) line. It captures the FIRST module token; comma-lists and
// dotted packages are handled by the caller splitting on the module name.
var pyImportRe = regexp.MustCompile(`(?m)^[ \t]*(?:from[ \t]+\.?([A-Za-z_][A-Za-z0-9_]*)|import[ \t]+([A-Za-z_][A-Za-z0-9_]*(?:[ \t]*,[ \t]*[A-Za-z_][A-Za-z0-9_]*)*))`)

// shSourceRe matches bash `source foo.sh` / `. foo.sh` (optionally
// ./-prefixed or quoted), capturing the referenced filename.
var shSourceRe = regexp.MustCompile(`(?m)^[ \t]*(?:source|\.)[ \t]+["']?(?:\./)?([A-Za-z0-9_./\-]+\.sh)["']?`)

// scriptHelperRefs returns the LITERAL sibling filenames a script body pulls
// in that could resolve to a helper file next to it — Python module imports
// (foo -> foo.py) and bash sources (foo.sh). Best-effort and language-scoped
// to Python/bash (where env-var params + gohort helpers already steer
// authoring); anything it doesn't recognize simply isn't followed, which is a
// no-op (the helper still sits in the shared workspace at runtime, it just
// doesn't travel). ext picks which import grammar to scan.
func scriptHelperRefs(body, ext string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	switch ext {
	case ".py":
		for _, m := range pyImportRe.FindAllStringSubmatch(body, -1) {
			// m[1] = from-import module; m[2] = import list (comma-separated).
			if m[1] != "" {
				add(m[1] + ".py")
			}
			if m[2] != "" {
				for _, mod := range strings.Split(m[2], ",") {
					if mod = strings.TrimSpace(mod); mod != "" {
						add(mod + ".py")
					}
				}
			}
		}
	case ".sh", ".bash":
		for _, m := range shSourceRe.FindAllStringSubmatch(body, -1) {
			if len(m) >= 2 {
				add(m[1])
			}
		}
	}
	return out
}

// gatherWorkspaceHelpers walks the dependency graph of a primary script and
// returns the helper files (as RecipeFile{Path,Content}) it pulls in that
// EXIST on disk under workspaceDir. Starting from primaryBody it follows
// Python imports / bash sources transitively (bounded), reading each resolved
// sibling and scanning IT for further helpers. primaryName is excluded so the
// entry script (which travels as ScriptBody) isn't duplicated. Best-effort:
// a helper that can't be resolved or read is simply skipped.
func gatherWorkspaceHelpers(primaryName, primaryBody, workspaceDir string) []RecipeFile {
	if workspaceDir == "" || primaryBody == "" {
		return nil
	}
	collected := map[string]bool{primaryName: true}
	var out []RecipeFile
	queue := scriptHelperRefs(primaryBody, strings.ToLower(filepath.Ext(primaryName)))
	for len(queue) > 0 && len(out) < maxWorkspaceHelpers {
		rel := queue[0]
		queue = queue[1:]
		if collected[rel] || strings.ContainsAny(rel, "/\\") {
			// Already have it, or a sub-path we won't chase (helpers live
			// beside the entry script in the flat workspace root).
			continue
		}
		collected[rel] = true
		full := filepath.Join(workspaceDir, rel)
		info, err := os.Stat(full)
		if err != nil || info.IsDir() {
			continue // unresolved import (stdlib / third-party / typo) — skip
		}
		content, err := os.ReadFile(full)
		if err != nil || len(content) == 0 {
			continue
		}
		out = append(out, RecipeFile{Path: rel, Content: string(content), Mode: 0700})
		queue = append(queue, scriptHelperRefs(string(content), strings.ToLower(filepath.Ext(rel)))...)
	}
	return out
}

// rawNetworkPatterns names script-side APIs that bypass the hook and
// reach the network directly. Tools that use any of these MUST
// declare raw_network=true (to leave the bwrap namespace networked)
// or migrate to the hook (gohort.fetch / gohort.fetch_via). Detection
// is substring-match against the script_body — coarse but
// catches the common authoring mistakes (mostly Python urllib and
// shell curl/wget) without false positives in normal prose.
var rawNetworkPatterns = []string{
	"urllib.request",
	"urlopen(",
	"http.client",
	"requests.get",
	"requests.post",
	"requests.put",
	"requests.delete",
	"requests.request",
	"requests.Session",
	"socket.create_connection",
	"socket.connect",
	"curl ",
	"wget ",
}

// networkGrantMismatch returns a directive error string when the
// tool's script_body uses a raw-network API but the tool record
// doesn't declare a network grant (HookCapabilities including
// "fetch" or "fetch_via:..." OR RawNetwork=true). Returns "" when
// the grant matches the script's usage. The post-strict-network
// guard: --unshare-net would cause urllib / curl / etc. to fail
// with "Name or service not known" on every dispatch; catch at
// authoring time instead.
//
// Hook-only tools (HookCapabilities=["log"] with no fetch) still
// trigger the lint if the script tries to do raw HTTP — log alone
// doesn't grant network reach.
func networkGrantMismatch(tt *TempTool) string {
	if tt == nil || tt.ScriptBody == "" {
		return ""
	}
	if tt.RawNetwork {
		return ""
	}
	// Hook grants "fetch" or any "fetch_via:..." count as a network
	// grant via the proxy path — the script SHOULD use gohort.fetch,
	// but we can't easily tell whether it does without parsing.
	// Accept the grant and move on; the actual mismatch (script uses
	// urllib AND has fetch capability) is a possible authoring smell
	// but valid in principle (mixed-use tools).
	for _, c := range tt.HookCapabilities {
		if c == "fetch" || strings.HasPrefix(c, "fetch_via:") {
			return ""
		}
	}
	// No grant — check whether the script needs one.
	var found []string
	seen := map[string]bool{}
	for _, pat := range rawNetworkPatterns {
		if strings.Contains(tt.ScriptBody, pat) {
			if !seen[pat] {
				found = append(found, pat)
				seen[pat] = true
			}
		}
	}
	if len(found) == 0 {
		return ""
	}
	return fmt.Sprintf("script_body uses raw-network API(s) %v but the tool has no network grant. Either (a) re-author with the hook: `from gohort import fetch` then `fetch(url)` instead of urllib.request.urlopen(url) — and declare hook_capabilities=[\"fetch\"]; or (b) declare raw_network=true (escape hatch for persistent-mode REPLs and non-HTTP TCP). Without one of these the sandbox runs --unshare-net and every outbound call fails with a DNS-resolution error", found)
}
