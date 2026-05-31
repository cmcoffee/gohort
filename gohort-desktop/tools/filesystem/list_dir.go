// filesystem.list_directory — list entries in a directory on the host
// filesystem of the connected gohort-desktop. Shares the read-allowlist
// with read_local_file (core.PathAllowed) so adding a folder once
// exposes it to both tools.
//
// Output shape is a compact, ls-like text grid — one entry per line:
//
//	dir   2025-04-12 14:33    -  Documents
//	file  2025-04-12 14:33  4321  notes.md
//
// Chose text-rather-than-JSON because the consumer is an LLM that
// reads ls output natively; a structured response would just get
// re-stringified. Capped at MAX_LIST_ENTRIES with a TRUNCATED marker
// so a directory of 50,000 files doesn't flood context.
//
// Special path: passing path="" or "/" returns the configured root
// list itself — gives an agent a starting point for "what can I look
// at?" without needing a separate enumerate-roots tool.

package filesystem

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
)

// MAX_LIST_ENTRIES caps the number of rows returned. Past this point
// the response is truncated with a [TRUNCATED — N more] marker.
// Sized to a generous typical "Logs" directory while keeping the
// response from drowning the LLM's context.
const MAX_LIST_ENTRIES = 500

func init() {
	core.RegisterTool(&list_dir_tool{})
}

type list_dir_tool struct{}

func (t *list_dir_tool) Name() string {
	return "filesystem.list_directory"
}

func (t *list_dir_tool) Desc() string {
	return "List entries in a directory on the host filesystem of the " +
		"connected gohort-desktop client. Returns one entry per line as " +
		"\"<kind> <mtime> <size> <name>\" — kind is dir/file/symlink, " +
		"size is bytes for files (- for dirs), mtime is YYYY-MM-DD HH:MM. " +
		"Gated by the operator's allowlist — paths outside it are refused. " +
		"Pass path=\"\" (or omit it) to see the configured allowlist roots " +
		"themselves; that's the safe starting point when you don't know " +
		"what's exposed. Capped at 500 entries with a [TRUNCATED — N more] " +
		"marker if the directory is larger. Use to discover what files " +
		"exist before calling filesystem.read_local_file."
}

func (t *list_dir_tool) Params() map[string]core.ToolParam {
	return map[string]core.ToolParam{
		"path": {
			Type:        "string",
			Description: "Absolute path to the directory. Must resolve under one of the allowlisted roots. Omit (or pass empty / \"/\") to list the allowlist roots themselves — useful as the first call to see what's available.",
		},
	}
}

func (t *list_dir_tool) Required() []string { return nil } // path is optional — empty = list roots

func (t *list_dir_tool) Enabled() bool { return true }

func (t *list_dir_tool) Handler() core.ToolHandler {
	return func(args map[string]any) (string, error) {
		path, _ := args["path"].(string)
		path = strings.TrimSpace(path)

		// Roots-listing mode: when the caller hasn't specified a real
		// target, return the configured allowlist so the LLM has a map
		// of where to drill in.
		if path == "" || path == "/" {
			return render_roots_listing(), nil
		}

		abs, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("filesystem.list_directory: resolve path: %w", err)
		}
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			abs = real
		}
		if !core.PathAllowed(abs) {
			return "", fmt.Errorf("filesystem.list_directory refused: %s is not under an allowed read root (allowed: %v) — operator can add a root via the Account → Add Allowed Folder… menu in gohort-desktop", abs, core.AllowedReadRoots())
		}
		info, err := os.Stat(abs)
		if err != nil {
			return "", fmt.Errorf("filesystem.list_directory: stat: %w", err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("filesystem.list_directory: %s is a file, not a directory — use filesystem.read_local_file for files", abs)
		}
		entries, err := os.ReadDir(abs)
		if err != nil {
			return "", fmt.Errorf("filesystem.list_directory: read: %w", err)
		}
		// Sort: dirs first, then files; alphabetical within each
		// group. Matches typical ls --group-directories-first behavior
		// and keeps a "scroll for the file I want" workflow predictable.
		sort.Slice(entries, func(i, j int) bool {
			ia, ja := entries[i].IsDir(), entries[j].IsDir()
			if ia != ja {
				return ia // dirs first
			}
			return entries[i].Name() < entries[j].Name()
		})
		core.Debug("[filesystem.list_directory] %s → %d entries", abs, len(entries))

		var b strings.Builder
		fmt.Fprintf(&b, "Listing of %s (%d %s):\n", abs, len(entries), plural(len(entries), "entry", "entries"))
		shown := 0
		for _, e := range entries {
			if shown >= MAX_LIST_ENTRIES {
				break
			}
			line := format_entry(abs, e)
			if line == "" {
				continue
			}
			b.WriteString(line)
			b.WriteByte('\n')
			shown++
		}
		if len(entries) > MAX_LIST_ENTRIES {
			fmt.Fprintf(&b, "\n[TRUNCATED — %d more entries; refine the path or filter caller-side]\n", len(entries)-MAX_LIST_ENTRIES)
		}
		return b.String(), nil
	}
}

// format_entry renders one directory entry as the canonical
// "<kind> <mtime> <size> <name>" line. Best-effort: stat failures
// fall back to "?" so a single broken entry doesn't kill the listing.
func format_entry(parent string, e os.DirEntry) string {
	info, err := e.Info()
	if err != nil {
		return fmt.Sprintf("?     ?                 ?  %s", e.Name())
	}
	kind := "file"
	sizeCol := fmt.Sprintf("%d", info.Size())
	switch {
	case info.IsDir():
		kind = "dir"
		sizeCol = "-"
	case info.Mode()&os.ModeSymlink != 0:
		kind = "symlink"
		sizeCol = "-"
	case info.Mode()&os.ModeDevice != 0,
		info.Mode()&os.ModeNamedPipe != 0,
		info.Mode()&os.ModeSocket != 0:
		kind = "other"
		sizeCol = "-"
	}
	mtime := info.ModTime().Format("2006-01-02 15:04")
	return fmt.Sprintf("%-7s %s %10s  %s", kind, mtime, sizeCol, e.Name())
}

// render_roots_listing produces the "what folders am I allowed to
// see?" view. Lists every configured root with a one-line existence/
// type marker so the LLM knows which are real, missing, or wrong-type.
func render_roots_listing() string {
	roots := core.AllowedReadRoots()
	if len(roots) == 0 {
		return "No folders are currently exposed by gohort-desktop. The operator can add one via the Account → Add Allowed Folder… menu."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Allowed root folders on the connected gohort-desktop (%d):\n", len(roots))
	b.WriteString("Pass any of these as path= to list its contents.\n\n")
	for _, r := range roots {
		marker := "ok"
		if info, err := os.Stat(r); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				marker = "missing"
			} else {
				marker = "error: " + err.Error()
			}
		} else if !info.IsDir() {
			marker = "not-a-dir"
		}
		fmt.Fprintf(&b, "  %s   [%s]\n", r, marker)
	}
	return b.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
