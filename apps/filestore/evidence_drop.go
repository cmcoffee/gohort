package filestore

// Evidence another app holds, put into a store as a new folder: Servitor
// moving an evidence bundle (appstores.EvidenceDrop). The folder is written in
// a hidden staging folder and put in place only once complete, so a store's
// listing, sweep and index never see half of it, and is then indexed like an
// upload.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appstores"
	"github.com/cmcoffee/gohort/core/pathhold"
)

// EvidenceStores lists the stores indexing evidence that user may upload to.
func (h storeHost) EvidenceStores(user string, isAdmin bool) []appstores.EvidenceStore {
	var out []appstores.EvidenceStore
	for _, st := range ListStores(h.app.DB) {
		if st.Evidence && st.UploadsAllowedFor(user, isAdmin) && st.Valid() == nil {
			out = append(out, appstores.EvidenceStore{Store: st.Slug, Name: st.Name, About: st.Description})
		}
	}
	return out
}

// AddEvidenceFolder makes a new folder in store from what fill writes.
func (h storeHost) AddEvidenceFolder(ctx context.Context, user string, isAdmin bool, slug, name string, need int64, fill func(dir string) error) (string, error) {
	st, ok := LoadStore(h.app.DB, slug)
	if !ok || !st.AllowsUser(user) {
		return "", Error("no such file store you can reach")
	}
	if !st.Evidence {
		return "", Error(st.Name + " does not index its folders as evidence")
	}
	if !st.UploadsAllowedFor(user, isAdmin) {
		return "", Error("uploads are turned off for " + st.Name + ". An admin can enable them for assigned users on the store's settings (Admin, File stores).")
	}
	if err := st.Valid(); err != nil {
		return "", err
	}
	if err := st.roomFor(need); err != nil {
		return "", err
	}
	staging, err := os.MkdirTemp(st.Path, stagingPrefix)
	if err != nil {
		return "", fmt.Errorf("cannot make a folder in %s: %w", st.Name, err)
	}
	defer pathhold.Hold(staging)()
	defer st.wrote()
	if err := fill(staging); err != nil {
		os.RemoveAll(staging)
		return "", err
	}
	if err := ctx.Err(); err != nil {
		os.RemoveAll(staging)
		return "", err
	}
	folder := freeFolderName(st.Path, name)
	if err := os.Rename(staging, filepath.Join(st.Path, folder)); err != nil {
		os.RemoveAll(staging)
		return "", fmt.Errorf("cannot put the folder in place in %s: %w", st.Name, err)
	}
	Log("[filestore] %s added evidence folder %s/%s", user, st.Name, folder)
	noteChanged(h.app.DB, st, folder)
	return folder, nil
}

// folderNameUnsafe is what a folder name made from someone's label keeps out:
// anything but letters, digits, dot, dash and underscore.
var folderNameUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// freeFolderName is name made safe for a folder, and unique in root: "case",
// then "case-2" and on.
func freeFolderName(root, name string) string {
	base := strings.Trim(folderNameUnsafe.ReplaceAllString(strings.TrimSpace(name), "-"), "-.")
	if base == "" {
		base = "evidence"
	}
	folder := base
	for n := 2; ; n++ {
		if _, err := os.Lstat(filepath.Join(root, folder)); os.IsNotExist(err) {
			return folder
		}
		folder = fmt.Sprintf("%s-%d", base, n)
	}
}
