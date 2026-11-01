package main

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/migrate"
	"github.com/cmcoffee/gohort/core/service"
	bolt "github.com/cmcoffee/gohort/core/bbolt"
)

// runStartupMigrations moves what an older gohort saved into the shape this
// one reads, each once (its marker keeps it from running again). Serve runs
// them before it answers; --migrate-dry-run runs the same ones on a copy.
func runStartupMigrations(db Database) {
	// The Source Hooks this instance kept become search connectors. A search
	// connector's research settings move to Research Sources before the
	// hooks arrive there.
	service.MigrateResearchSettings(db)
	service.MigrateSourceHooks(db)
	// A services connector is in use exactly while a page names it.
	service.MigrateTieIns(db)
	// The connections gohort provides, then what predates connections moved
	// onto them.
	if ProvideConnectors != nil {
		ProvideConnectors(db)
	}
	service.MigrateConnections(db)
	// What is wrong with a saved connection is in the log now, not when a
	// call first meets it.
	service.LogSelfCheck(db)
}

// migrationDryRun is --migrate-dry-run: the startup migrations run on a copy
// of this instance's database, and what they changed there is reported.
// gohort must be stopped, since a running one holds the database. What the
// migrations move between other files (the vector store's fold) is not run.
func migrationDryRun(w io.Writer) error {
	init_config_file()
	data_dir := loadPath("data_dir", fmt.Sprintf("%s/data", global.root))
	scratch, err := os.MkdirTemp("", "gohort-migrate-dry-run-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	// Anything a migration writes beside the database goes here too.
	SetImageDir(scratch + "/images")
	SetBrowserDir(scratch + "/browser")
	SetWorkspacesDir(scratch + "/workspaces")
	SetAppAssetsDir(scratch + "/app_assets")
	return dryRunMigrations(mainDBFile(data_dir), scratch, w)
}

// dryRunMigrations copies the database at src into dir, runs the startup
// migrations on the copy, and writes what they changed to w. src is only
// read.
func dryRunMigrations(src, dir string, w io.Writer) error {
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("no database at %s: %w", src, err)
	}
	dst := filepath.Join(dir, filepath.Base(src))
	if err := snapshotDB(src, dst); err != nil {
		return err
	}
	db, err := SecureDatabase(dst)
	if err != nil {
		return err
	}
	savedGlobal, savedRoot, savedAuth := global.db, RootDB, AuthDB
	global.db, RootDB = db, db
	AuthDB = func() Database { return db }
	MigrateToolReleases(db) // init_database runs it before the rest
	runStartupMigrations(db)
	db.Close()
	global.db, RootDB, AuthDB = savedGlobal, savedRoot, savedAuth

	changes, err := diffDatabases(src, dst)
	if err != nil {
		return err
	}
	writeDryRun(w, src, changes)
	return nil
}

// snapshotDB writes a consistent copy of the bolt file src to dst. Opening
// it waits a moment for the lock a running gohort holds, then says so.
func snapshotDB(src, dst string) error {
	db, err := bolt.Open(src, 0600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err == bolt.ErrTimeout {
		return fmt.Errorf("%s is in use: stop gohort, then run this again", src)
	}
	if err != nil {
		return err
	}
	defer db.Close()
	return db.View(func(tx *bolt.Tx) error { return tx.CopyFile(dst, 0600) })
}

// dbChange is one record a dry run changed: Kind is "+" added, "-" removed
// or "~" changed, and Was and Now are its values as they can be shown.
type dbChange struct {
	Table, Key, Kind, Was, Now string
}

// diffDatabases is every record that differs between the bolt files before
// and after, table by table, in order. kvlite's own bookkeeping is left out.
func diffDatabases(before, after string) ([]dbChange, error) {
	read := func(file string) (map[string]map[string][]byte, error) {
		db, err := bolt.Open(file, 0600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
		if err != nil {
			return nil, err
		}
		defer db.Close()
		out := map[string]map[string][]byte{}
		err = db.View(func(tx *bolt.Tx) error {
			return tx.ForEach(func(name []byte, b *bolt.Bucket) error {
				if string(name) == "KVLite" {
					return nil
				}
				rows := map[string][]byte{}
				err := b.ForEach(func(k, v []byte) error {
					rows[string(k)] = append([]byte(nil), v...)
					return nil
				})
				out[string(name)] = rows
				return err
			})
		})
		return out, err
	}
	was, err := read(before)
	if err != nil {
		return nil, err
	}
	now, err := read(after)
	if err != nil {
		return nil, err
	}
	tables := map[string]bool{}
	for t := range was {
		tables[t] = true
	}
	for t := range now {
		tables[t] = true
	}
	names := make([]string, 0, len(tables))
	for t := range tables {
		names = append(names, t)
	}
	sort.Strings(names)
	var out []dbChange
	for _, t := range names {
		keys := map[string]bool{}
		for k := range was[t] {
			keys[k] = true
		}
		for k := range now[t] {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		table := strings.ReplaceAll(t, "\x1f", "/")
		show := shownValue
		if t == migrate.MigrationsTable {
			show = shownMarker
		}
		for _, k := range sorted {
			a, inA := was[t][k]
			b, inB := now[t][k]
			key := k
			if t == migrate.MigrationsTable {
				key = strings.TrimSuffix(k, ":") // app:name:owner, and no owner
			}
			switch {
			case !inA:
				out = append(out, dbChange{Table: table, Key: key, Kind: "+", Now: show(b)})
			case !inB:
				out = append(out, dbChange{Table: table, Key: key, Kind: "-", Was: show(a)})
			case !bytes.Equal(a, b):
				out = append(out, dbChange{Table: table, Key: key, Kind: "~", Was: show(a), Now: show(b)})
			}
		}
	}
	return out, nil
}

// shownValue is a stored value as a dry run shows it: an encrypted one never,
// a text or a list of them as it is, anything else by its size.
func shownValue(raw []byte) string {
	if len(raw) == 0 {
		return "(empty)"
	}
	if raw[0] == 1 {
		return "(encrypted)"
	}
	body := raw[1:]
	var s string
	if gob.NewDecoder(bytes.NewReader(body)).Decode(&s) == nil {
		return clipShown(fmt.Sprintf("%q", s))
	}
	var list []string
	if gob.NewDecoder(bytes.NewReader(body)).Decode(&list) == nil {
		return clipShown(fmt.Sprintf("%q", list))
	}
	var b bool
	if gob.NewDecoder(bytes.NewReader(body)).Decode(&b) == nil {
		return fmt.Sprint(b)
	}
	return fmt.Sprintf("(record, %d bytes)", len(body))
}

// shownMarker is a migration's marker as a dry run shows it: how many
// records it changed, and what it failed with.
func shownMarker(raw []byte) string {
	var m migrate.MigrationMarker
	if len(raw) == 0 || raw[0] != 0 || gob.NewDecoder(bytes.NewReader(raw[1:])).Decode(&m) != nil {
		return shownValue(raw)
	}
	out := fmt.Sprintf("runs, changing %d record(s)", m.Changed)
	if m.Error != "" {
		out += ", and FAILS: " + clipShown(m.Error)
	}
	return out
}

func clipShown(s string) string {
	if r := []rune(s); len(r) > 120 {
		return string(r[:120]) + "..."
	}
	return s
}

// dryRunTableShown is how many of one table's changes a report lists.
const dryRunTableShown = 25

// writeDryRun reports changes table by table.
func writeDryRun(w io.Writer, src string, changes []dbChange) {
	fmt.Fprintf(w, "\nMigration dry run on a copy of %s. Nothing in it was changed.\n", src)
	if len(changes) == 0 {
		fmt.Fprintln(w, "The migrations would change nothing: every one has run, or has nothing to move.")
		return
	}
	tables := 0
	for i, c := range changes {
		if i == 0 || changes[i-1].Table != c.Table {
			tables++
		}
	}
	fmt.Fprintf(w, "They would change %d record(s) in %d table(s):\n", len(changes), tables)
	shown := 0
	for i, c := range changes {
		if i == 0 || changes[i-1].Table != c.Table {
			if i > 0 && shown > dryRunTableShown {
				fmt.Fprintf(w, "    ... and %d more\n", shown-dryRunTableShown)
			}
			fmt.Fprintf(w, "\n  %s\n", c.Table)
			shown = 0
		}
		shown++
		if shown > dryRunTableShown {
			continue
		}
		switch c.Kind {
		case "+":
			fmt.Fprintf(w, "    + %s: %s\n", c.Key, c.Now)
		case "-":
			fmt.Fprintf(w, "    - %s (was %s)\n", c.Key, c.Was)
		default:
			fmt.Fprintf(w, "    ~ %s: %s -> %s\n", c.Key, c.Was, c.Now)
		}
	}
	if shown > dryRunTableShown {
		fmt.Fprintf(w, "    ... and %d more\n", shown-dryRunTableShown)
	}
}
