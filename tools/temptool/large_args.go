package temptool

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// maxEnvArgBytes is the largest parameter value handed to a shell tool as an
// environment variable. Every parameter reaches the sandbox as a --setenv
// argument, and Linux refuses any single argument over 128 KiB
// (MAX_ARG_STRLEN), so a whole API response could never arrive: a pipeline
// that handed a 262 KB music-generation response to its extraction step
// failed with "Argument list too long" on every run.
const maxEnvArgBytes = 100 * 1024

// maxFileArgBytes bounds a {"file": ...} value read from the workspace.
const maxFileArgBytes = 64 << 20

// fileArg is a parameter that went to the script as a file.
type fileArg struct {
	param string
	path  string
	size  int
}

// passLargeArgs moves parameter values that cannot be environment variables
// into files. A string value over maxEnvArgBytes is written under
// <workspace>/.tool_args/, $<param> is left empty, and $<param>_file holds the
// path. A string parameter may also be given as {"file": "<workspace path>"}
// (a saved response, say), which is read and then passed the same way: inline
// when it fits, by file when it does not. Returns what went by file, for the
// caller to clean up and to name in a failure.
func passLargeArgs(tt *TempTool, args map[string]any, envArgs map[string]string, sess *ToolSession, workspaceDir string) ([]fileArg, error) {
	for k, v := range args {
		if !isValidEnvVarName(k) {
			continue
		}
		rel, ok := fileArgRef(v)
		if !ok || (tt.Params[k].Type != "" && tt.Params[k].Type != "string") {
			continue
		}
		content, err := readWorkspaceFileArg(sess, workspaceDir, rel)
		if err != nil {
			return nil, fmt.Errorf("param %q names file %q: %v", k, rel, err)
		}
		envArgs[k] = content
	}
	var moved []fileArg
	for k, val := range envArgs {
		if len(val) <= maxEnvArgBytes || strings.HasPrefix(k, "GOHORT_") {
			continue
		}
		dir := filepath.Join(workspaceDir, ".tool_args")
		if err := os.MkdirAll(dir, 0700); err != nil {
			return moved, fmt.Errorf("param %q is %d KB, too large for an environment variable, and could not be written to a file: %v", k, len(val)/1024, err)
		}
		sum := sha256.Sum256([]byte(val))
		path := filepath.Join(dir, fmt.Sprintf("%s_%s_%s.txt", tt.Name, k, hex.EncodeToString(sum[:4])))
		if err := os.WriteFile(path, []byte(val), 0600); err != nil {
			return moved, fmt.Errorf("param %q is %d KB, too large for an environment variable, and could not be written to a file: %v", k, len(val)/1024, err)
		}
		envArgs[k] = ""
		envArgs[k+"_file"] = path
		moved = append(moved, fileArg{param: k, path: path, size: len(val)})
	}
	return moved, nil
}

// fileArgRef reports whether v is a {"file": "<path>"} reference, given as an
// object or as that object's JSON text (a string param's value may arrive
// already encoded).
func fileArgRef(v any) (string, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		s, isStr := v.(string)
		if !isStr || !strings.HasPrefix(strings.TrimSpace(s), "{") {
			return "", false
		}
		if json.Unmarshal([]byte(s), &m) != nil {
			return "", false
		}
	}
	if len(m) != 1 {
		return "", false
	}
	p, ok := m["file"].(string)
	p = strings.TrimSpace(p)
	return p, ok && p != ""
}

// readWorkspaceFileArg reads rel from the session's workspace, or the run's own
// directory, confined to it.
func readWorkspaceFileArg(sess *ToolSession, workspaceDir, rel string) (string, error) {
	var roots []string
	if sess != nil && sess.WorkspaceDir != "" {
		roots = append(roots, sess.WorkspaceDir)
	}
	if workspaceDir != "" {
		roots = append(roots, workspaceDir)
	}
	var lastErr error = fmt.Errorf("no workspace to read it from")
	for _, root := range roots {
		path, err := ResolveWorkspacePath(root, rel)
		if err != nil {
			lastErr = err
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			lastErr = err
			continue
		}
		if info.IsDir() {
			return "", fmt.Errorf("it is a directory")
		}
		if info.Size() > maxFileArgBytes {
			return "", fmt.Errorf("it is %d MB, over the %d MB a parameter may carry", info.Size()>>20, maxFileArgBytes>>20)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	return "", lastErr
}

// fileArgsNote tells the author which parameters went by file, for a run that
// failed: a script that reads only $<param> sees it empty.
func fileArgsNote(moved []fileArg) string {
	if len(moved) == 0 {
		return ""
	}
	var parts []string
	for _, m := range moved {
		parts = append(parts, fmt.Sprintf("%s (%d KB)", m.param, m.size/1024))
	}
	first := moved[0].param
	return fmt.Sprintf("\n[passed by file: %s. A value that size cannot be an environment variable, so $%s is empty and $%s_file holds the path: the script must read the file, e.g. open(os.environ[\"%s_file\"]).read()]", strings.Join(parts, ", "), first, first, first)
}
