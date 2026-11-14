// publish puts a release on the dist site the admin page updates from
// (docs/design/software-update.md), and makes the key that signs it.
//
//	publish key -out <private key file> -keys core/selfupdate/release_keys.txt
//	publish release -stage <dir> -dist <dir> -version <v> -commit <c> -key <file> -keys <file>
//
// key makes a signing key pair: the private key to a file only its owner can
// read, the public key added to the built-in key list (commit that).
//
// release copies what was built in -stage (<os>-<arch>/gohort[.exe] with its
// THIRD_PARTY_NOTICES, and LICENSE and NOTICE) into <dist>/<version>/, then writes the signed
// manifest, and version.txt last, so no server sees a version before all of
// it is there. Each file is written under another name and renamed into
// place: on a network share a move from local disk is a copy, and a copy can
// be read half done. Release folders past the newest -keep are removed.
package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/cmcoffee/gohort/core/selfupdate"
)

func main() {
	if len(os.Args) < 2 {
		fail(errors.New("usage: publish key | release (see the comment at the top of scripts/publish)"))
	}
	var err error
	switch os.Args[1] {
	case "key":
		err = runKey(os.Args[2:])
	case "release":
		err = runRelease(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q: key or release", os.Args[1])
	}
	if err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "publish:", err)
	os.Exit(1)
}

func runKey(args []string) error {
	fs := flag.NewFlagSet("key", flag.ExitOnError)
	out := fs.String("out", "", "where to write the private key")
	keys := fs.String("keys", "", "the built-in public key list to add the new key to")
	fs.Parse(args)
	if *out == "" || *keys == "" {
		return errors.New("key needs -out and -keys")
	}
	id, err := makeKey(*out, *keys)
	if err != nil {
		return err
	}
	fmt.Printf("Made release key %s.\n  private: %s (keep it here, and a copy offline; never on the dist share)\n  public:  added to %s (commit it)\n", id, *out, *keys)
	return nil
}

// makeKey writes a new private key to out, readable by its owner only, and
// adds its public key to the key list. It never writes over a key.
func makeKey(out, keyList string) (string, error) {
	if _, err := os.Stat(out); err == nil {
		return "", fmt.Errorf("%s exists: a release key is never written over. Move it first if a new one is meant", out)
	}
	privFile, pubLine, err := selfupdate.GenerateKey()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(privFile); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	list, err := os.OpenFile(keyList, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return "", fmt.Errorf("the key is at %s, but the key list did not open: %v", out, err)
	}
	defer list.Close()
	if _, err := list.WriteString(pubLine); err != nil {
		return "", err
	}
	return strings.Fields(privFile)[1], nil
}

// release is one publish.
type release struct {
	stage, dist     string
	version, commit string
	product         string
	priv            ed25519.PrivateKey
	keep            int
	now             func() time.Time
}

func runRelease(args []string) error {
	fs := flag.NewFlagSet("release", flag.ExitOnError)
	r := release{product: "gohort", now: time.Now}
	fs.StringVar(&r.stage, "stage", "", "the folder the binaries were built into")
	fs.StringVar(&r.dist, "dist", "", "the dist folder: the share the dist site serves")
	fs.StringVar(&r.version, "version", "", "the version being published")
	fs.StringVar(&r.commit, "commit", "", "the commit it was built from")
	fs.IntVar(&r.keep, "keep", 3, "how many release folders to keep")
	keyFile := fs.String("key", "", "the private key file")
	keyList := fs.String("keys", "", "the built-in public key list: the signing key must be in it")
	fs.Parse(args)
	if r.stage == "" || r.dist == "" || r.version == "" || *keyFile == "" || *keyList == "" {
		return errors.New("release needs -stage, -dist, -version, -key and -keys")
	}
	priv, err := loadKey(*keyFile, *keyList)
	if err != nil {
		return err
	}
	r.priv = priv
	files, err := r.publish()
	if err != nil {
		return err
	}
	fmt.Printf("Published %s %s to %s:\n", r.product, r.version, r.dist)
	for _, p := range sortedKeys(files) {
		fmt.Printf("  %-14s %s\n", p, files[p].Path)
	}
	return nil
}

// loadKey reads the private key, and refuses one the built-in key list does
// not have: servers could not check what it signs.
func loadKey(keyFile, keyList string) (ed25519.PrivateKey, error) {
	st, err := os.Stat(keyFile)
	if err != nil {
		return nil, fmt.Errorf("no release key at %s: make one with `make release-key`", keyFile)
	}
	if st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s can be read by others (%v): chmod 600 it", keyFile, st.Mode().Perm())
	}
	b, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	priv, err := selfupdate.ParsePrivateKey(string(b))
	if err != nil {
		return nil, err
	}
	lb, err := os.ReadFile(keyList)
	if err != nil {
		return nil, err
	}
	keys, err := selfupdate.ParseKeys(string(lb))
	if err != nil {
		return nil, fmt.Errorf("%s: %v", keyList, err)
	}
	id := selfupdate.KeyID(priv.Public().(ed25519.PublicKey))
	for _, k := range keys {
		if k.ID == id {
			return priv, nil
		}
	}
	return nil, fmt.Errorf("key %s is not in %s: servers could not check what it signs. Add its public key there, commit, and build again", id, keyList)
}

// versionDir is a release folder's name: a version.
var versionDir = regexp.MustCompile(`^[0-9]+(\.[0-9]+)+([-.][0-9A-Za-z]+)*$`)

// publish copies the release in, then the signed manifest, then version.txt.
func (r release) publish() (map[string]selfupdate.File, error) {
	if !versionDir.MatchString(r.version) {
		return nil, fmt.Errorf("%q is not a version", r.version)
	}
	if st, err := os.Stat(r.dist); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("the dist folder %s is not there (is the share mounted?)", r.dist)
	}
	if cur, ok := r.published(); ok && !selfupdate.Newer(r.version, cur) {
		return nil, fmt.Errorf("%s is published, and %s is not newer", cur, r.version)
	}
	relDir := filepath.Join(r.dist, r.version)
	if _, err := os.Stat(relDir); err == nil {
		return nil, fmt.Errorf("%s exists: a published version is never written over", relDir)
	}
	plats, err := os.ReadDir(r.stage)
	if err != nil {
		return nil, err
	}
	files := map[string]selfupdate.File{}
	for _, p := range plats {
		if !p.IsDir() {
			if err := copyInto(filepath.Join(r.stage, p.Name()), filepath.Join(relDir, p.Name())); err != nil {
				return nil, err
			}
			continue
		}
		bin := r.product
		if strings.HasPrefix(p.Name(), "windows-") {
			bin += ".exe"
		}
		src := filepath.Join(r.stage, p.Name(), bin)
		rel := r.version + "/" + p.Name() + "/" + bin
		size, sum, err := hashFile(src)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", p.Name(), err)
		}
		// The binary, and what travels with it (its notices: the modules a
		// platform links are not the same on every one).
		extra, err := os.ReadDir(filepath.Join(r.stage, p.Name()))
		if err != nil {
			return nil, err
		}
		for _, e := range extra {
			if e.Type().IsRegular() {
				if err := copyInto(filepath.Join(r.stage, p.Name(), e.Name()), filepath.Join(relDir, p.Name(), e.Name())); err != nil {
					return nil, err
				}
			}
		}
		files[p.Name()] = selfupdate.File{Path: rel, Size: size, SHA256: sum}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no binaries in %s", r.stage)
	}
	m := selfupdate.Manifest{Product: r.product, Version: r.version, Commit: r.commit,
		Published: r.now().UTC().Format(time.RFC3339), Files: files}
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	body = append(body, '\n')
	if err := writeInto(filepath.Join(r.dist, selfupdate.SignatureName), selfupdate.Sign(body, r.priv)); err != nil {
		return nil, err
	}
	if err := writeInto(filepath.Join(r.dist, selfupdate.ManifestName), body); err != nil {
		return nil, err
	}
	if err := writeInto(filepath.Join(r.dist, selfupdate.VersionName), []byte(r.version)); err != nil {
		return nil, err
	}
	r.prune()
	return files, nil
}

// published is the version the dist folder's manifest names now.
func (r release) published() (string, bool) {
	b, err := os.ReadFile(filepath.Join(r.dist, selfupdate.ManifestName))
	if err != nil {
		return "", false
	}
	var m selfupdate.Manifest
	if json.Unmarshal(b, &m) != nil || m.Version == "" {
		return "", false
	}
	return m.Version, true
}

// prune removes release folders past the newest keep, never the one just
// published, and nothing that is not named as a version.
func (r release) prune() {
	if r.keep < 1 {
		return
	}
	ents, err := os.ReadDir(r.dist)
	if err != nil {
		return
	}
	var vers []string
	for _, e := range ents {
		if e.IsDir() && versionDir.MatchString(e.Name()) {
			vers = append(vers, e.Name())
		}
	}
	sort.Slice(vers, func(i, j int) bool { return selfupdate.Newer(vers[i], vers[j]) })
	for i, v := range vers {
		if i >= r.keep && v != r.version {
			if err := os.RemoveAll(filepath.Join(r.dist, v)); err != nil {
				fmt.Fprintf(os.Stderr, "publish: could not remove the old release %s: %v\n", v, err)
			}
		}
	}
}

func hashFile(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return n, hex.EncodeToString(h.Sum(nil)), err
}

// copyInto copies src to dst under a temporary name, then renames it.
func copyInto(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".publish-*")
	if err != nil {
		return err
	}
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	os.Chmod(tmp.Name(), st.Mode().Perm()|0o444)
	if err := os.Rename(tmp.Name(), dst); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// writeInto writes b to dst under a temporary name, then renames it.
func writeInto(dst string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".publish-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	os.Chmod(tmp.Name(), 0o644)
	if err := os.Rename(tmp.Name(), dst); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

func sortedKeys(m map[string]selfupdate.File) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
