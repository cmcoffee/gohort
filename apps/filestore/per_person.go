// A store that gives each person their own folder.
//
// A store is one folder on the host, and everyone who reaches it reaches the
// same one. That is right for a folder of build logs and wrong for a working
// area: an app granted write on it shares one folder with everyone who uses
// the app, so one person's uploads are the next person's search results.
//
// With PerPerson on, every way a person reaches the store (an attached agent's
// tools, an app's mounted folder, commands, pulls, uploads) reaches only
// people/<them>/ under the store's folder, made on first use. It is done by
// handing out a copy of the store whose Path IS that folder (forPerson), so
// every check downstream, which already holds a path to Path, holds it to the
// person's folder without knowing this exists.
//
// What stays on the store's own folder: the admin page, the size cap (one cap
// for the store, not one per person), and the cleanup sweeps, which walk each
// person's folders in turn.

package filestore

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// peopleDir is the folder under a per-person store's own that holds one
// folder per person. A folder of its own rather than the top level, so files
// already in the store never collide with somebody's name.
const peopleDir = "people"

// personFolder is the folder name for user: their name when it is a plain
// one, and otherwise the plain part of it, a + (which a plain name never
// holds) and a short hash of the whole, so two names can never share a folder
// and no name can be a path. Lower case, because a store matches names that
// way (accessFor).
func personFolder(user string) string {
	name := strings.ToLower(strings.TrimSpace(user))
	var b strings.Builder
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '@':
			b.WriteRune(c)
		default:
			b.WriteByte('_')
		}
	}
	plain := b.String()
	if plain == name && name != "" && !strings.HasPrefix(name, ".") {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	return strings.TrimLeft(plain, ".") + "+" + hex.EncodeToString(sum[:4])
}

// forPerson is the store as user reaches it. For a store that is not per
// person, the store itself, noted as being for user (pullSource reads that).
// For one that is, a copy whose Path is user's own folder, made if it is not
// there yet. Never saved: SaveStore refuses a copy like this.
func (s Store) forPerson(user string) (Store, error) {
	user = strings.TrimSpace(user)
	s.viewer = user
	if !s.PerPerson || s.root != "" {
		return s, nil
	}
	if user == "" {
		return Store{}, Error(s.Name + " gives each person their own folder, and this was not asked for by a person")
	}
	root, err := filepath.EvalSymlinks(s.Path)
	if err != nil {
		return Store{}, Error("the folder of " + s.Name + " is unreadable: " + err.Error())
	}
	people, err := ownFolder(root, peopleDir)
	if err != nil {
		return Store{}, Error(s.Name + ": " + err.Error())
	}
	mine, err := ownFolder(people, personFolder(user))
	if err != nil {
		return Store{}, Error(s.Name + ": " + err.Error())
	}
	s.root, s.Path = root, mine
	return s, nil
}

// ownFolder is parent/name as a real folder, made when absent. A link there
// is refused rather than followed: people/alice pointing at people/bob stays
// inside the store, so containment alone would not catch it.
func ownFolder(parent, name string) (string, error) {
	p := filepath.Join(parent, name)
	if err := os.Mkdir(p, 0o755); err != nil && !os.IsExist(err) {
		return "", Error("cannot make the folder " + name + ": " + err.Error())
	}
	info, err := os.Lstat(p)
	if err != nil {
		return "", Error("cannot read the folder " + name + ": " + err.Error())
	}
	if !info.Mode().IsDir() {
		return "", Error(name + " is not a plain folder, so it is not used")
	}
	return p, nil
}

// rootPath is the store's own folder, for a copy made for one person too.
func (s Store) rootPath() string {
	if s.root != "" {
		return s.root
	}
	return s.Path
}

// forPersonKey is what tells one person's run on item apart from another's in
// the same store: the pull registry keys a job by it.
func (s Store) forPersonKey(item string) string {
	if s.root != "" {
		return s.Slug + "/" + personFolder(s.viewer) + "/" + item
	}
	return s.Slug + "/" + item
}

// personRoot is one person's folder in a per-person store, for a sweep.
type personRoot struct {
	name, dir string
}

// personRoots lists the folders under people/, links left out.
func (s Store) personRoots() []personRoot {
	root, err := filepath.EvalSymlinks(s.rootPath())
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(root, peopleDir))
	if err != nil {
		return nil
	}
	var out []personRoot
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, personRoot{name: e.Name(), dir: filepath.Join(root, peopleDir, e.Name())})
		}
	}
	return out
}

// folderCountLabel is the store's folder count for a list: "12", or for a
// per-person store "3 people", or "unreadable".
func (s Store) folderCountLabel() string {
	if s.PerPerson && s.root == "" {
		n := len(s.personRoots())
		if n == 1 {
			return "1 person"
		}
		return strconv.Itoa(n) + " people"
	}
	n, err := CountFolders(s.Path)
	if err != nil {
		return "unreadable"
	}
	return strconv.Itoa(n)
}
