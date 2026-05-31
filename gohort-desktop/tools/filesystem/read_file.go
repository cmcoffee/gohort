// filesystem.read_local_file — read a text file from the host
// filesystem, gated by the shared read-allowlist in core. Symlink-safe
// (resolves the real path before allowlist check). Caps at
// MAX_READ_BYTES with a TRUNCATED marker for partial reads.
//
// Allowlist mutations (operator adds/removes a folder via the Account
// menu) take effect immediately — this tool re-queries on every call.
//
// Adding more filesystem tools (list_directory, tail, write_file, etc.):
// drop a new .go file in this package, share the same allowlist via
// core.PathAllowed. write_file should NOT share this allowlist — make
// a sibling allowlist for write access when that tool lands.

package filesystem

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
)

// MAX_READ_BYTES caps any single read response. Sized to be the
// "I really did want the whole file" budget — large config / log
// previews — without becoming a vector for blowing the LLM's context
// window with one megabyte of unintentional text.
//
// Files BIGGER than this aren't truncated silently anymore — they
// return an error directing the LLM to the targeted query tools
// (stat / head / tail / grep / read_file_range), which run on the
// host and ship only the small slice back across the WS bridge.
// That's almost always what was intended; if it really wasn't, the
// LLM can call read_file_range(start=1, end=N) to walk it.
const MAX_READ_BYTES = 64 * 1024

func init() {
	core.RegisterTool(&read_file_tool{})
}

type read_file_tool struct{}

func (t *read_file_tool) Name() string {
	return "filesystem.read_local_file"
}

func (t *read_file_tool) Desc() string {
	return "Read a text file from the host filesystem of the connected " +
		"gohort-desktop client. Gated by the operator's allowlist — paths " +
		"outside it are refused. Symlink-safe (resolves the real path " +
		"before checking). Files larger than 10MiB return truncated " +
		"content with a [TRUNCATED — file is N bytes] marker. Use for " +
		"reading log files, configuration files, recent output captured " +
		"by other tools, anything the operator has authorized the " +
		"desktop to expose. Returns the file contents as a string. " +
		"On refused-by-allowlist the error lists the currently allowed " +
		"roots so the LLM can pick a valid path or report back."
}

func (t *read_file_tool) Params() map[string]core.ToolParam {
	return map[string]core.ToolParam{
		"path": {
			Type:        "string",
			Description: "Absolute path to the file. Must resolve under one of the allowlisted roots. If unsure what's allowed, call filesystem.list_directory first to see the available roots.",
		},
	}
}

func (t *read_file_tool) Required() []string { return []string{"path"} }

func (t *read_file_tool) Enabled() bool { return true }

func (t *read_file_tool) Handler() core.ToolHandler {
	return func(args map[string]any) (string, error) {
		path, _ := args["path"].(string)
		path = strings.TrimSpace(path)
		if path == "" {
			return "", errors.New("filesystem.read_local_file: path is required")
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("filesystem.read_local_file: resolve path: %w", err)
		}
		// Symlink-safe: resolve the real path so an attacker can't
		// smuggle /tmp/sneaky → /etc/shadow past the allowlist.
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			abs = real
		}
		if !core.PathAllowed(abs) {
			return "", fmt.Errorf("filesystem.read_local_file refused: %s is not under an allowed read root (allowed: %v) — operator can add a root via the Account → Add Allowed Folder… menu in gohort-desktop", abs, core.AllowedReadRoots())
		}
		f, err := os.Open(abs)
		if err != nil {
			return "", fmt.Errorf("filesystem.read_local_file: open: %w", err)
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return "", fmt.Errorf("filesystem.read_local_file: stat: %w", err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("filesystem.read_local_file: %s is a directory, not a file — use filesystem.list_directory for directories", abs)
		}
		// Hard-stop for files larger than the inline cap. Truncating
		// silently and returning a partial body is the worst of both
		// worlds: the LLM sees something, assumes it's the whole file,
		// and acts on incomplete data. Direct to the targeted query
		// tools instead — they ship only the slice the LLM actually
		// needs and run on the host so the bridge stays cheap.
		if info.Size() > MAX_READ_BYTES {
			return "", fmt.Errorf("filesystem.read_local_file: %s is %d bytes (cap %d); too large to inline. Use a targeted query instead:\n"+
				"  filesystem.stat_file(path=%q)                       → size, line count, kind hint\n"+
				"  filesystem.head_file(path=%q, lines=N)               → first N lines\n"+
				"  filesystem.tail_file(path=%q, lines=N)               → last N lines\n"+
				"  filesystem.read_file_range(path=%q, start=A, end=B)  → lines A–B\n"+
				"  filesystem.grep_file(path=%q, pattern=\"…\")           → matching lines\n"+
				"Pick the one matching what you need — the whole file never has to cross the WS bridge.",
				abs, info.Size(), MAX_READ_BYTES, abs, abs, abs, abs, abs)
		}
		buf, err := io.ReadAll(io.LimitReader(f, MAX_READ_BYTES))
		if err != nil {
			return "", fmt.Errorf("filesystem.read_local_file: read: %w", err)
		}
		core.Debug("[filesystem.read_local_file] %s → %d bytes", abs, len(buf))
		return string(buf), nil
	}
}
