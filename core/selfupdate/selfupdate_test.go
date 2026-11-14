package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testKey is a signing key pair for one test.
func testKey(t *testing.T) (ed25519.PrivateKey, []Key) {
	t.Helper()
	privFile, pubLine, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	priv, err := ParsePrivateKey(privFile)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := ParseKeys(pubLine)
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys %v %v", keys, err)
	}
	return priv, keys
}

func manifestFor(version string, bins map[string][]byte) []byte {
	m := Manifest{Product: "gohort", Version: version, Files: map[string]File{}}
	for p, b := range bins {
		sum := sha256.Sum256(b)
		m.Files[p] = File{Path: version + "/" + p + "/gohort", Size: int64(len(b)), SHA256: hex.EncodeToString(sum[:])}
	}
	out, _ := json.MarshalIndent(m, "", "  ")
	return out
}

func TestASignedReleaseListReadsAndATamperedOneDoesNot(t *testing.T) {
	priv, keys := testKey(t)
	body := manifestFor("0.9.380", map[string][]byte{"linux-amd64": []byte("bin")})
	sig := Sign(body, priv)
	m, err := ReadManifest(body, sig, keys, "gohort")
	if err != nil || m.Version != "0.9.380" {
		t.Fatalf("a good list was refused: %v", err)
	}
	tampered := []byte(strings.Replace(string(body), "0.9.380", "0.9.381", 1))
	if _, err := ReadManifest(tampered, sig, keys, "gohort"); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Errorf("a changed list was read: %v", err)
	}
	_, other := testKey(t)
	if _, err := ReadManifest(body, sig, other, "gohort"); err == nil || !strings.Contains(err.Error(), "does not trust") {
		t.Errorf("a list signed with another key was read: %v", err)
	}
	if _, err := ReadManifest(body, sig, nil, "gohort"); err == nil || !strings.Contains(err.Error(), "no release key") {
		t.Errorf("a list was read with no key to check it: %v", err)
	}
	if _, err := ReadManifest(body, sig, keys, "kitebroker"); err == nil || !strings.Contains(err.Error(), "is for") {
		t.Errorf("another product's list was read: %v", err)
	}
	if _, err := ReadManifest(body, []byte("garbage"), keys, "gohort"); err == nil {
		t.Error("a list with no real signature was read")
	}
	// Two keys trusted: the one that signed is found by its name.
	if _, err := ReadManifest(body, sig, append(other, keys...), "gohort"); err != nil {
		t.Errorf("a list signed by the second trusted key was refused: %v", err)
	}
}

func TestAReleaseListThatNamesABadFileIsRefused(t *testing.T) {
	priv, keys := testKey(t)
	for name, f := range map[string]File{
		"climbs out":   {Path: "../etc/gohort", Size: 3, SHA256: strings.Repeat("a", 64)},
		"absolute":     {Path: "/etc/gohort", Size: 3, SHA256: strings.Repeat("a", 64)},
		"another site": {Path: "https://elsewhere.test/gohort", Size: 3, SHA256: strings.Repeat("a", 64)},
		"no size":      {Path: "0.9.380/linux-amd64/gohort", Size: 0, SHA256: strings.Repeat("a", 64)},
		"no hash":      {Path: "0.9.380/linux-amd64/gohort", Size: 3, SHA256: "abc"},
	} {
		b, _ := json.Marshal(Manifest{Product: "gohort", Version: "0.9.380", Files: map[string]File{"linux-amd64": f}})
		if _, err := ReadManifest(b, Sign(b, priv), keys, "gohort"); err == nil {
			t.Errorf("%s: read", name)
		}
	}
	b, _ := json.Marshal(Manifest{Product: "gohort"})
	if _, err := ReadManifest(b, Sign(b, priv), keys, "gohort"); err == nil {
		t.Error("a list with no version was read")
	}
}

func TestKeysAndKeyFiles(t *testing.T) {
	_, line, _ := GenerateKey()
	_, line2, _ := GenerateKey()
	keys, err := ParseKeys("# comment\n" + line + "\n" + strings.TrimSpace(strings.Split(line2, "#")[0]) + ", \n")
	if err != nil || len(keys) != 2 {
		t.Fatalf("two keys read as %d: %v", len(keys), err)
	}
	if _, err := ParseKeys("not-a-key"); err == nil {
		t.Error("garbage read as a key")
	}
	if keys, err := ParseKeys(builtinKeys); err != nil {
		t.Errorf("the built-in key file does not read: %v (%d keys)", err, len(keys))
	}
	priv, _, _ := GenerateKey()
	f := strings.Fields(priv)
	if _, err := ParsePrivateKey(f[0] + " 0000000000000000 " + f[2]); err == nil {
		t.Error("a private key under another key's name was read")
	}
	if _, err := ParsePrivateKey(line); err == nil {
		t.Error("a public key was read as a private one")
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"0.9.380", "0.9.377", true},
		{"0.9.380", "0.9.38", true},
		{"0.10.0", "0.9.999", true},
		{"0.9.377", "0.9.377", false},
		{"0.9.376", "0.9.377", false},
		{"26.11.13-1449", "26.11.9-0900", true},
		{"1.0", "1.0.0", false},
		{"1.0.1", "1.0", true},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

// dist is a dist site serving one release.
type dist struct {
	srv      *httptest.Server
	manifest []byte
	sig      []byte
	bins     map[string][]byte // by path
	hits     atomic.Int32
}

func newDist(t *testing.T, priv ed25519.PrivateKey, version string, bin []byte) *dist {
	t.Helper()
	d := &dist{bins: map[string][]byte{}}
	d.manifest = manifestFor(version, map[string][]byte{Platform(): bin})
	d.sig = Sign(d.manifest, priv)
	d.bins[version+"/"+Platform()+"/gohort"] = bin
	d.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.hits.Add(1)
		p := strings.TrimPrefix(r.URL.Path, "/gohort/")
		switch p {
		case ManifestName:
			w.Write(d.manifest)
		case SignatureName:
			w.Write(d.sig)
		default:
			b, ok := d.bins[p]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Write(b)
		}
	}))
	t.Cleanup(d.srv.Close)
	return d
}

func (d *dist) source(keys []Key) Source {
	return Source{URL: d.srv.URL + "/gohort", Keys: keys, Product: "gohort", UserAgent: "gohort/test"}
}

func TestADownloadThatDoesNotMatchTheListIsRemoved(t *testing.T) {
	priv, keys := testKey(t)
	d := newDist(t, priv, "0.9.380", []byte("the real binary"))
	src := d.source(keys)
	m, err := src.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path, err := src.Download(context.Background(), m, Platform(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "the real binary" {
		t.Fatalf("downloaded %q", b)
	}
	os.Remove(path)

	// The site swaps the binary; the signed list still names the old one.
	d.bins["0.9.380/"+Platform()+"/gohort"] = []byte("the real binarY")
	if _, err := src.Download(context.Background(), m, Platform(), dir, nil); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Errorf("a swapped binary was taken: %v", err)
	}
	d.bins["0.9.380/"+Platform()+"/gohort"] = []byte("short")
	if _, err := src.Download(context.Background(), m, Platform(), dir, nil); err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Errorf("a short binary was taken: %v", err)
	}
	d.bins["0.9.380/"+Platform()+"/gohort"] = []byte("the real binary, and more after it")
	if _, err := src.Download(context.Background(), m, Platform(), dir, nil); err == nil {
		t.Error("a longer binary was taken")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 0 {
		t.Errorf("failed downloads left %v", left)
	}
	if _, err := src.Download(context.Background(), m, "plan9-mips", dir, nil); err == nil {
		t.Error("a platform the release lacks was downloaded")
	}
}

func TestAnUnsignedSiteOffersNothing(t *testing.T) {
	priv, keys := testKey(t)
	d := newDist(t, priv, "0.9.380", []byte("bin"))
	d.sig = []byte("")
	if _, err := d.source(keys).Latest(context.Background()); err == nil {
		t.Error("a release with no signature was offered")
	}
	if _, err := (Source{Keys: keys, Product: "gohort"}).Latest(context.Background()); err == nil {
		t.Error("a check with no address answered")
	}
}

// script is a stand-in binary that says a version, the way gohort --version does.
func script(t *testing.T, version string) []byte {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stand-in binaries are shell scripts")
	}
	return []byte("#!/bin/sh\necho '  v" + version + "'\n")
}

func TestSwapKeepsThePreviousAndRollbackGoesBackAndForth(t *testing.T) {
	dir := t.TempDir()
	in := Install{Exe: filepath.Join(dir, "gohort")}
	os.WriteFile(in.Exe, []byte("old"), 0o755)
	if _, ok := in.Previous(); ok {
		t.Fatal("a previous version before any update")
	}
	next := filepath.Join(dir, "next")
	os.WriteFile(next, []byte("new"), 0o755)
	if err := in.Swap(next, "0.9.377"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(in.Exe); string(b) != "new" {
		t.Fatalf("running binary is %q", b)
	}
	if v, ok := in.Previous(); !ok || v != "0.9.377" {
		t.Fatalf("previous %q %v", v, ok)
	}
	was, err := in.Rollback("0.9.380")
	if err != nil || was != "0.9.377" {
		t.Fatalf("rollback %q %v", was, err)
	}
	if b, _ := os.ReadFile(in.Exe); string(b) != "old" {
		t.Fatalf("after rollback the binary is %q", b)
	}
	if v, _ := in.Previous(); v != "0.9.380" {
		t.Errorf("after rollback the previous is %q, want the one rolled back from", v)
	}
	if in.Writable() != "" {
		t.Errorf("a temp dir reads as not writable: %s", in.Writable())
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".update-*")); len(left) != 0 {
		t.Errorf("the writable check left %v", left)
	}
}

func TestProveRunsTheNewBinaryOnce(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, b, 0o755)
		return p
	}
	in := Install{Exe: filepath.Join(dir, "gohort")}
	if err := in.Prove(context.Background(), write("good", script(t, "0.9.380")), "0.9.380"); err != nil {
		t.Errorf("a good binary failed: %v", err)
	}
	if err := in.Prove(context.Background(), write("wrong", script(t, "0.9.379")), "0.9.380"); err == nil {
		t.Error("a binary saying another version passed")
	}
	if err := in.Prove(context.Background(), write("broken", []byte("#!/bin/sh\nexit 3\n")), "0.9.380"); err == nil {
		t.Error("a binary that fails passed")
	}
	if err := in.Prove(context.Background(), write("junk", []byte("\x7fELF not really")), "0.9.380"); err == nil {
		t.Error("a binary that cannot start passed")
	}
	prev := proveTimeout
	proveTimeout = 200 * time.Millisecond
	t.Cleanup(func() { proveTimeout = prev })
	if err := in.Prove(context.Background(), write("hangs", []byte("#!/bin/sh\nexec sleep 5\n")), "0.9.380"); err == nil {
		t.Error("a binary that never answers passed")
	}
}

// updater is an install of 0.9.377 and a dist site offering version.
func updater(t *testing.T, version string, bin []byte) (*Updater, *dist, chan struct{}) {
	t.Helper()
	priv, keys := testKey(t)
	d := newDist(t, priv, version, bin)
	dir := t.TempDir()
	exe := filepath.Join(dir, "gohort")
	os.WriteFile(exe, script(t, "0.9.377"), 0o755)
	restarted := make(chan struct{}, 1)
	u := &Updater{
		Product: "gohort", Version: "0.9.377", Source: d.source(keys),
		Install: Install{Exe: exe}, RestartDelay: time.Millisecond,
		Restart: func() { restarted <- struct{}{} },
	}
	t.Cleanup(func() { setPendingRelaunch("") })
	return u, d, restarted
}

func TestUpdateInstallsTheNewReleaseAndRestarts(t *testing.T) {
	newBin := script(t, "0.9.380")
	u, _, restarted := updater(t, "0.9.380", newBin)
	ctx := context.Background()
	if st := u.Status(); st.Available || st.Latest != "" || st.Blocked != "" {
		t.Fatalf("before a check: %+v", st)
	}
	msg, err := u.Check(ctx)
	if err != nil || !strings.Contains(msg, "0.9.380 is available") {
		t.Fatalf("check: %q %v", msg, err)
	}
	if st := u.Status(); !st.Available || st.Latest != "0.9.380" || st.Checked.IsZero() {
		t.Fatalf("after a check: %+v", st)
	}
	if _, out := u.Progress(ActionCheck); out != msg {
		t.Errorf("the check's outcome is %q", out)
	}
	msg, err = u.Update(ctx)
	if err != nil || !strings.Contains(msg, "Installed 0.9.380") {
		t.Fatalf("update: %q %v", msg, err)
	}
	if b, _ := os.ReadFile(u.Install.Exe); string(b) != string(newBin) {
		t.Fatal("the new binary is not in place")
	}
	if v, ok := u.Install.Previous(); !ok || v != "0.9.377" {
		t.Errorf("the previous is %q %v", v, ok)
	}
	select {
	case <-restarted:
	case <-time.After(2 * time.Second):
		t.Fatal("no restart after installing")
	}
	pendingMu.Lock()
	pending := pendingRelaunch
	pendingMu.Unlock()
	if CanRelaunch && pending != u.Install.Exe {
		t.Errorf("the relaunch is set to %q", pending)
	}
	st := u.Status()
	if st.Available || st.Installed != "0.9.380" || !st.HasPrevious {
		t.Errorf("after installing: %+v", st)
	}
	if _, err := u.Update(ctx); err == nil || !strings.Contains(err.Error(), "installed already") {
		t.Errorf("a second update before the restart: %v", err)
	}
}

func TestUpdateLeavesTheProgramAsItWasWhenAStepFails(t *testing.T) {
	old := script(t, "0.9.377")
	check := func(t *testing.T, u *Updater) {
		t.Helper()
		if b, _ := os.ReadFile(u.Install.Exe); string(b) != string(old) {
			t.Error("the running binary was changed")
		}
		if _, ok := u.Install.Previous(); ok {
			t.Error("a previous version was set aside")
		}
		if left, _ := filepath.Glob(filepath.Join(u.Install.Dir(), ".update-*")); len(left) != 0 {
			t.Errorf("left behind: %v", left)
		}
	}
	t.Run("the binary does not say its version", func(t *testing.T) {
		u, _, _ := updater(t, "0.9.380", script(t, "0.9.379"))
		if _, err := u.Update(context.Background()); err == nil {
			t.Fatal("installed")
		}
		check(t, u)
		if _, out := u.Progress(ActionUpdate); !strings.HasPrefix(out, "Not updated:") {
			t.Errorf("outcome %q", out)
		}
	})
	t.Run("the site swapped the binary", func(t *testing.T) {
		u, d, _ := updater(t, "0.9.380", script(t, "0.9.380"))
		d.bins["0.9.380/"+Platform()+"/gohort"] = []byte("#!/bin/sh\necho v0.9.380; rm -rf /\n")
		if _, err := u.Update(context.Background()); err == nil || !strings.Contains(err.Error(), "bytes") && !strings.Contains(err.Error(), "hash") {
			t.Fatalf("installed: %v", err)
		}
		check(t, u)
	})
	t.Run("nothing newer", func(t *testing.T) {
		u, _, _ := updater(t, "0.9.377", script(t, "0.9.377"))
		if _, err := u.Update(context.Background()); err == nil || !strings.Contains(err.Error(), "newest release") {
			t.Fatalf("installed: %v", err)
		}
		check(t, u)
		if msg, _ := u.Check(context.Background()); !strings.Contains(msg, "Up to date") {
			t.Errorf("check said %q", msg)
		}
		if u.Status().Available {
			t.Error("the same version offered")
		}
	})
	t.Run("an older release", func(t *testing.T) {
		u, _, _ := updater(t, "0.9.300", script(t, "0.9.300"))
		if _, err := u.Update(context.Background()); err == nil {
			t.Fatal("went backwards")
		}
		check(t, u)
	})
	t.Run("a blocked install", func(t *testing.T) {
		u, d, _ := updater(t, "0.9.380", script(t, "0.9.380"))
		u.Blocked = "this build has the private apps"
		before := d.hits.Load()
		if _, err := u.Update(context.Background()); err == nil || !strings.Contains(err.Error(), "private apps") {
			t.Fatalf("installed: %v", err)
		}
		if d.hits.Load() != before {
			t.Error("a blocked install fetched from the site")
		}
		if _, err := u.Rollback(context.Background()); err == nil {
			t.Error("a blocked install rolled back")
		}
		check(t, u)
		if u.Status().Blocked == "" {
			t.Error("the status does not say it is blocked")
		}
	})
}

func TestRollbackRestartsOntoThePrevious(t *testing.T) {
	u, _, restarted := updater(t, "0.9.380", script(t, "0.9.380"))
	if _, err := u.Rollback(context.Background()); err == nil {
		t.Fatal("rolled back with nothing kept")
	}
	if _, err := u.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-restarted
	// The restarted program is 0.9.380, with 0.9.377 kept.
	v := &Updater{Product: "gohort", Version: "0.9.380", Install: u.Install, RestartDelay: time.Millisecond,
		Restart: func() { restarted <- struct{}{} }}
	msg, err := v.Rollback(context.Background())
	if err != nil || !strings.Contains(msg, "Rolled back to 0.9.377") {
		t.Fatalf("rollback %q %v", msg, err)
	}
	<-restarted
	if b, _ := os.ReadFile(u.Install.Exe); !strings.Contains(string(b), "v0.9.377") {
		t.Errorf("after rollback the binary is %q", b)
	}
}

func TestOneActionAtATime(t *testing.T) {
	u := &Updater{}
	if err := u.begin(ActionUpdate); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Check(context.Background()); err != ErrBusy {
		t.Errorf("a check during an update: %v", err)
	}
	u.say("downloading")
	if p, _ := u.Progress(ActionUpdate); !strings.HasPrefix(p, "downloading") {
		t.Errorf("progress %q", p)
	}
	if p, _ := u.Progress(ActionCheck); p != "" {
		t.Errorf("the check row reads as running: %q", p)
	}
	u.end(ActionUpdate, "done")
	if p, out := u.Progress(ActionUpdate); p != "" || out != "done" {
		t.Errorf("after: %q %q", p, out)
	}
}

func TestNoRestartLeavesItToWhoeverRunsIt(t *testing.T) {
	u, _, _ := updater(t, "0.9.380", script(t, "0.9.380"))
	u.Restart = nil
	msg, err := u.Update(context.Background())
	if err != nil || !strings.Contains(msg, "Restart gohort") {
		t.Fatalf("%q %v", msg, err)
	}
	pendingMu.Lock()
	defer pendingMu.Unlock()
	if pendingRelaunch != "" {
		t.Error("a relaunch was set with no restart")
	}
}
