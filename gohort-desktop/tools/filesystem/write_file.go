// filesystem.write_file — create/overwrite/append a text file on the
// host, gated by the SEPARATE write-allowlist (core.PathWriteAllowedOrConsent).
// Read access never implies write access; the first write to a new
// folder asks the user for permission on its own track. Symlink-safe:
// resolves the parent dir before the allowlist check so a symlinked
// directory can't smuggle a write outside an approved root.

package filesystem

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
)

func init() { core.RegisterTool(&write_file_tool{}) }

type write_file_tool struct{}

func (t *write_file_tool) Name() string { return "filesystem.write_file" }

func (t *write_file_tool) Desc() string {
	return "Create, overwrite, or append to a text file on the host filesystem of the connected gohort-desktop client. Gated by a SEPARATE write-allowlist from reads — the first write to a new folder prompts the user to approve write access there. Use to save output, edit a config file, or create a file the user asked for. Set append=true to add to the end of an existing file instead of replacing it. Returns a confirmation with the byte count."
}

func (t *write_file_tool) Params() map[string]core.ToolParam {
	return map[string]core.ToolParam{
		"path":    {Type: "string", Description: "Absolute path to the file to write. Its folder must be approved for writing (the user is prompted on first write to a new folder)."},
		"content": {Type: "string", Description: "The text content to write."},
		"append":  {Type: "boolean", Description: "If true, append to the end of the file instead of overwriting it. Default false (overwrite/create)."},
	}
}

func (t *write_file_tool) Required() []string { return []string{"path", "content"} }
func (t *write_file_tool) Enabled() bool      { return true }

func (t *write_file_tool) Handler() core.ToolHandler {
	return func(args map[string]any) (string, error) {
		path, _ := args["path"].(string)
		path = strings.TrimSpace(path)
		if path == "" {
			return "", errors.New("filesystem.write_file: path is required")
		}
		content, _ := args["content"].(string)
		appendMode, _ := args["append"].(bool)

		abs, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("filesystem.write_file: resolve path: %w", err)
		}
		// Resolve the PARENT (the file itself may not exist yet) so a
		// symlinked directory can't redirect the write past the allowlist.
		parent := filepath.Dir(abs)
		if real, err := filepath.EvalSymlinks(parent); err == nil {
			abs = filepath.Join(real, filepath.Base(abs))
		}
		if !core.PathWriteAllowedOrConsent(abs) {
			return "", fmt.Errorf("filesystem.write_file refused: %s is not under an approved write folder (approved: %v)", abs, core.AllowedWriteRoots())
		}

		flag := os.O_CREATE | os.O_WRONLY
		if appendMode {
			flag |= os.O_APPEND
		} else {
			flag |= os.O_TRUNC
		}
		f, err := os.OpenFile(abs, flag, 0o644)
		if err != nil {
			return "", fmt.Errorf("filesystem.write_file: open: %w", err)
		}
		defer f.Close()
		n, err := f.WriteString(content)
		if err != nil {
			return "", fmt.Errorf("filesystem.write_file: write: %w", err)
		}
		verb := "Wrote"
		if appendMode {
			verb = "Appended"
		}
		return fmt.Sprintf("%s %d bytes to %s", verb, n, abs), nil
	}
}
