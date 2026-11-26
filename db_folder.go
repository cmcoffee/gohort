package main

// The databases live in data_dir/db, the per-app files in data_dir/db/apps
// (docs/design/data-db-folder.md). An install from before keeps them at the
// top of data_dir; the first start of this version moves them, once, by
// rename, and leaves a link at each old path so an older binary still finds
// them.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// dbLayout is where this start opens its database files.
type dbLayout struct {
	dir  string // gohort.db, and the stores whose setting is unset
	apps string // the per-app files (OpenAppDB)
}

// mainFile is the deployment's own database.
func (l dbLayout) mainFile() string { return filepath.Join(l.dir, APPNAME+".db") }

// appFile is one per-app file, by the name OpenAppDB is asked for.
func (l dbLayout) appFile(name string) string {
	return filepath.Join(l.apps, APPNAME+"_"+name+".db")
}

// storeFiles are the stores that move with gohort.db when their own setting
// is unset: [paths] vector_dir, repo_dir and bundle_dir.
var storeFiles = []struct{ setting, file string }{
	{"vector_dir", APPNAME + "_vectors.db"},
	{"repo_dir", APPNAME + "_repos.db"},
	{"bundle_dir", APPNAME + "_bundles.db"},
}

// dbRelocation is the once-only move into data_dir/db.
type dbRelocation struct {
	dataDir string
	// set reports whether a [paths] store setting is set; a store whose
	// setting is set stays where the setting puts it and is not moved.
	set func(setting string) bool
	// link makes a symbolic link (os.Symlink); a test stands in for a system
	// that refuses one.
	link func(target, name string) error
	// note logs a line.
	note func(vars ...interface{})
}

// dbMove is one file to move.
type dbMove struct{ from, to, target string }

// run moves what the old layout has into db/ and says where to open
// everything. A rename that fails is an error: gohort must not start without
// its database and must not copy one. Where no link can be made the old
// layout is kept, so an older binary run here later still finds its files.
func (r dbRelocation) run() (dbLayout, error) {
	l := dbLayout{dir: filepath.Join(r.dataDir, "db"), apps: filepath.Join(r.dataDir, "db", "apps")}
	moves := r.plan(l)
	if len(moves) == 0 {
		return l, r.ensure(l)
	}
	if err := r.ensure(l); err != nil {
		return l, err
	}
	if why := r.canLink(l); why != nil {
		r.note("Databases stay at the top of %s: no link can be made here (%v), and without one an older version would start empty.", r.dataDir, why)
		return dbLayout{dir: r.dataDir, apps: r.dataDir}, nil
	}
	for _, m := range moves {
		if err := os.Rename(m.from, m.to); err != nil {
			return l, fmt.Errorf("could not move %s to %s (%v): gohort does not copy a database, so move it there yourself (same disk), or point [paths] data_dir at where it is", m.from, m.to, err)
		}
		if err := r.link(m.target, m.from); err != nil {
			if back := os.Rename(m.to, m.from); back != nil {
				return l, fmt.Errorf("moved %s to %s but could not leave a link at the old path (%v), nor move it back (%v)", m.from, m.to, err, back)
			}
			return l, fmt.Errorf("could not leave a link at %s after moving it (%v); it was moved back", m.from, err)
		}
		r.note("Moved %s to %s (a link stays at the old path).", m.from, m.to)
	}
	return l, nil
}

// plan lists the files at their old paths that move: regular files only (a
// link is one this already moved), and only when nothing is at the new path.
// A regular file at both is left alone with a warning: the new one is opened.
func (r dbRelocation) plan(l dbLayout) []dbMove {
	var moves []dbMove
	add := func(name, toDir, target string) {
		from, to := filepath.Join(r.dataDir, name), filepath.Join(toDir, name)
		fi, err := os.Lstat(from)
		if err != nil || !fi.Mode().IsRegular() {
			return
		}
		if _, err := os.Lstat(to); err == nil {
			r.note("Both %s and %s are present: opening %s and leaving the other alone. Remove whichever is stale.", from, to, to)
			return
		}
		moves = append(moves, dbMove{from: from, to: to, target: target})
	}
	add(APPNAME+".db", l.dir, filepath.Join("db", APPNAME+".db"))
	stores := map[string]bool{}
	for _, s := range storeFiles {
		stores[s.file] = true
		if !r.set(s.setting) {
			add(s.file, l.dir, filepath.Join("db", s.file))
		}
	}
	// Every other gohort_*.db at the top is a per-app file.
	names, _ := filepath.Glob(filepath.Join(r.dataDir, APPNAME+"_*.db"))
	for _, p := range names {
		if n := filepath.Base(p); !stores[n] {
			add(n, l.apps, filepath.Join("db", "apps", n))
		}
	}
	return moves
}

// ensure makes the two folders.
func (r dbRelocation) ensure(l dbLayout) error {
	if err := os.MkdirAll(l.apps, 0o755); err != nil {
		return fmt.Errorf("could not make %s: %w", l.apps, err)
	}
	return nil
}

// canLink tries one link in db/ and takes it away again.
func (r dbRelocation) canLink(l dbLayout) error {
	probe := filepath.Join(l.dir, ".link-check")
	os.Remove(probe)
	if err := r.link(".", probe); err != nil {
		return err
	}
	os.Remove(probe)
	return nil
}

// removeAppFile deletes one per-app file and the link left at its old path.
// The handle is closed first (core); a file never made is not an error.
func (l dbLayout) removeAppFile(dataDir, name string) error {
	file := l.appFile(name)
	var errs []string
	if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, err.Error())
	}
	if old := filepath.Join(dataDir, APPNAME+"_"+name+".db"); old != file {
		if fi, err := os.Lstat(old); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			if err := os.Remove(old); err != nil {
				errs = append(errs, err.Error())
			}
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}
