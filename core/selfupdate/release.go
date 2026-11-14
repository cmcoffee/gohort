// Package selfupdate installs a newer release of the running program from a
// dist site, the way kitebroker's updater does, with two things it does not
// have: every release list is signed, and nothing is replaced until the new
// binary has been downloaded whole, checked and run once
// (docs/design/software-update.md).
//
// A dist folder holds version.txt, manifest.json, manifest.json.sig, and one
// folder per release with a binary per platform. The manifest is what is
// signed: the product, the version, and each binary's size and SHA-256.
//
// It speaks no core type, so it can move to snugforge for kitebroker, and
// costs core nothing against its export ceiling.
package selfupdate

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"unicode"
)

// Manifest is one release: what it is, and each platform's binary.
type Manifest struct {
	Product   string          `json:"product"`
	Version   string          `json:"version"`
	Commit    string          `json:"commit,omitempty"`
	Published string          `json:"published,omitempty"`
	Files     map[string]File `json:"files"`
}

// File is one platform's binary, at Path under the dist folder.
type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// The dist folder's fixed names.
const (
	ManifestName  = "manifest.json"
	SignatureName = "manifest.json.sig"
	VersionName   = "version.txt"
)

// maxBinary is the largest binary a manifest may name.
const maxBinary = 1 << 30

// Platform is this build's platform as the manifest names it: linux-amd64.
func Platform() string { return runtime.GOOS + "-" + runtime.GOARCH }

// For is this platform's binary, or why there is none.
func (m Manifest) For(platform string) (File, error) {
	f, ok := m.Files[platform]
	if !ok {
		return File{}, fmt.Errorf("release %s has no build for %s", m.Version, platform)
	}
	return f, nil
}

// check refuses a manifest that does not say what a release needs to.
func (m Manifest) check(product string) error {
	switch {
	case m.Product != product:
		return fmt.Errorf("the release list is for %q, not %q", m.Product, product)
	case strings.TrimSpace(m.Version) == "":
		return errors.New("the release list names no version")
	}
	for p, f := range m.Files {
		if !safePath(f.Path) {
			return fmt.Errorf("the release list's %s path %q is not a plain path inside the dist folder", p, f.Path)
		}
		if f.Size <= 0 || f.Size > maxBinary {
			return fmt.Errorf("the release list's %s size %d is not a binary's", p, f.Size)
		}
		if b, err := hex.DecodeString(f.SHA256); err != nil || len(b) != sha256.Size {
			return fmt.Errorf("the release list's %s hash is not a SHA-256", p)
		}
	}
	return nil
}

// safePath is a relative path that stays inside the dist folder.
func safePath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || strings.Contains(p, "://") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// Newer reports whether version a is newer than b. Versions compare by their
// numbers in order (0.9.380 after 0.9.38, 26.11.13-1449 after 26.11.9-0900);
// anything else in them is a separator.
func Newer(a, b string) bool {
	x, y := versionParts(a), versionParts(b)
	for i := 0; i < len(x) || i < len(y); i++ {
		var p, q int
		if i < len(x) {
			p = x[i]
		}
		if i < len(y) {
			q = y[i]
		}
		if p != q {
			return p > q
		}
	}
	return false
}

func versionParts(v string) []int {
	var out []int
	for _, f := range strings.FieldsFunc(v, func(r rune) bool { return !unicode.IsDigit(r) }) {
		n, err := strconv.Atoi(f)
		if err != nil {
			n = 0
		}
		out = append(out, n)
	}
	return out
}

// Key is a public key a release list may be signed with.
type Key struct {
	ID  string
	Pub ed25519.PublicKey
}

// KeyID names a public key: the first 8 bytes of its SHA-256, in hex. The
// signature file says which key made it, so a server trusting two keys knows
// which to check.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}

//go:embed release_keys.txt
var builtinKeys string

// BuiltinKeys are the keys built into this program.
func BuiltinKeys() []Key {
	keys, _ := ParseKeys(builtinKeys)
	return keys
}

// ParseKeys reads public keys, base64, one or more to a line or separated by
// commas; a # starts a comment.
func ParseKeys(text string) ([]Key, error) {
	var out []Key
	for _, line := range strings.Split(text, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		for _, f := range strings.FieldsFunc(line, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
			b, err := base64.StdEncoding.DecodeString(f)
			if err != nil || len(b) != ed25519.PublicKeySize {
				return nil, fmt.Errorf("%q is not a release public key", clip(f))
			}
			pub := ed25519.PublicKey(b)
			out = append(out, Key{ID: KeyID(pub), Pub: pub})
		}
	}
	return out, nil
}

// privateKeyTag starts a private key file, so a public key or anything else
// is not taken for one.
const privateKeyTag = "gohort-release-private-key"

// GenerateKey makes a signing key pair: the private key file's text, and the
// public key as a line for release_keys.txt.
func GenerateKey() (privateFile, publicLine string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	id := KeyID(pub)
	privateFile = fmt.Sprintf("%s %s %s\n", privateKeyTag, id, base64.StdEncoding.EncodeToString(priv.Seed()))
	publicLine = fmt.Sprintf("%s # key %s\n", base64.StdEncoding.EncodeToString(pub), id)
	return privateFile, publicLine, nil
}

// ParsePrivateKey reads a private key file GenerateKey wrote.
func ParsePrivateKey(text string) (ed25519.PrivateKey, error) {
	f := strings.Fields(text)
	if len(f) != 3 || f[0] != privateKeyTag {
		return nil, errors.New("not a release private key file")
	}
	seed, err := base64.StdEncoding.DecodeString(f[2])
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("the release private key file is damaged")
	}
	priv := ed25519.NewKeyFromSeed(seed)
	if KeyID(priv.Public().(ed25519.PublicKey)) != f[1] {
		return nil, errors.New("the release private key file is damaged: its key does not match its name")
	}
	return priv, nil
}

// Sign signs a manifest's exact bytes: the signature file's text.
func Sign(manifest []byte, priv ed25519.PrivateKey) []byte {
	id := KeyID(priv.Public().(ed25519.PublicKey))
	return []byte(id + " " + base64.StdEncoding.EncodeToString(ed25519.Sign(priv, manifest)) + "\n")
}

// Verify checks a signature file against a manifest's exact bytes and the
// trusted keys. It is checked before anything in the manifest is read.
func Verify(manifest, sig []byte, keys []Key) error {
	if len(keys) == 0 {
		return errors.New("no release key is built in or set, so no release can be checked")
	}
	f := strings.Fields(string(sig))
	if len(f) != 2 {
		return errors.New("the release list's signature is not one")
	}
	raw, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil || len(raw) != ed25519.SignatureSize {
		return errors.New("the release list's signature is not one")
	}
	for _, k := range keys {
		if k.ID != f[0] {
			continue
		}
		if ed25519.Verify(k.Pub, manifest, raw) {
			return nil
		}
		return errors.New("the release list's signature does not match it: not offering it")
	}
	return fmt.Errorf("the release list is signed with key %s, which this install does not trust", clip(f[0]))
}

// ReadManifest checks the signature, then reads the manifest and refuses one
// for another product or one that does not say what a release needs to.
func ReadManifest(manifest, sig []byte, keys []Key, product string) (Manifest, error) {
	if err := Verify(manifest, sig, keys); err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		return Manifest{}, fmt.Errorf("the release list is signed but does not read: %v", err)
	}
	if err := m.check(product); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func clip(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}
