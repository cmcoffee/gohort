// Download changes: everything that differs from a project's last commit, as
// one patch file to apply elsewhere with git apply. The way back out for a
// repository brought as a bundle (bundle.go), and useful on any project
// under git.
package anvil

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/sandbox"
)

// workingPatch is the project's changes against HEAD, untracked files
// included, binary ones too. A throwaway index holds the working tree (git
// add -A into it), so the project's own index is untouched, and the patch is
// that index against HEAD. It runs in the sandbox, as git in a checkout the
// run tool can write must, and writes the patch to a folder of the server's
// that nothing in the project can reach.
func workingPatch(ctx context.Context, base string) ([]byte, error) {
	if info := inspectRepo(base); !info.IsRepo {
		return nil, fmt.Errorf("this project is not under git, so there is no last commit to differ from")
	} else if info.Head == "" && info.Branch == "" {
		return nil, fmt.Errorf("nothing has been committed in this project yet")
	}
	out, err := os.MkdirTemp("", "anvil-patch-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(out)
	git := "git"
	for _, a := range gitSafety {
		git += " '" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	script := "set -e\n" +
		"export GIT_INDEX_FILE=\"$ANVIL_OUT/index\"\n" +
		git + " read-tree HEAD\n" +
		git + " add -A -- .\n" +
		git + " diff --cached --binary --no-ext-diff --no-textconv HEAD > \"$ANVIL_OUT/patch\"\n"
	env := map[string]string{"ANVIL_OUT": out}
	for _, kv := range gitQuietEnv {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	res := sandbox.RunSandboxedShellIn(ctx, sandbox.ShellRun{Command: script, WorkspaceDir: base, Env: env, Writable: []string{out}})
	if res.Err != nil {
		return nil, fmt.Errorf("the patch could not be made: %s", firstLine(strings.TrimSpace(res.Output+" "+res.Err.Error())))
	}
	return os.ReadFile(filepath.Join(out, "patch"))
}

var patchNameUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// handleDownloadChanges answers the patch as a file to save. Anyone who may
// open the project may take its changes: they can already read every diff.
func (T *Anvil) handleDownloadChanges(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	p, found := T.findProject(udb, user, callerIsAdmin(r), r.PathValue("id"))
	if !found {
		writeErr(w, http.StatusNotFound, "no such project")
		return
	}
	base, err := p.baseDir()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	patch, err := workingPatch(r.Context(), base)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	if len(patch) == 0 {
		writeErr(w, http.StatusConflict, "nothing differs from the last commit, so there is nothing to download")
		return
	}
	name := strings.Trim(patchNameUnsafe.ReplaceAllString(p.Name, "-"), "-.")
	if name == "" {
		name = "changes"
	}
	w.Header().Set("Content-Type", "text/x-diff; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.patch"`, name, time.Now().Format("20060102")))
	w.Write(patch)
}
