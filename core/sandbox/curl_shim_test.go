package sandbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGohortModule stands in for the hook-backed gohort helper: it records each
// request to FAKE_LOG and answers from FAKE_STATUS / FAKE_BODY, or raises
// FAKE_RAISE the way the hook raises a refusal.
const fakeGohortModule = `import json, os
def _rec(kind, url, kw, cred=None):
    with open(os.environ["FAKE_LOG"], "a") as f:
        f.write(json.dumps({"kind": kind, "url": url, "cred": cred, "method": kw.get("method"),
            "headers": kw.get("headers"), "body": kw.get("body"), "timeout": kw.get("timeout"),
            "save_to": kw.get("save_to")}) + "\n")
    if os.environ.get("FAKE_RAISE"):
        raise Exception(os.environ["FAKE_RAISE"])
    st = int(os.environ.get("FAKE_STATUS", "200"))
    body = os.environ.get("FAKE_BODY", "")
    if kw.get("save_to"):
        with open(kw["save_to"], "w") as f:
            f.write(body)
        return {"status": st, "headers": {}, "body": "saved", "bytes": len(body)}
    return {"status": st, "headers": {"Content-Type": "application/json"}, "body": body}
def fetch_url(url, **kw):
    return _rec("fetch_url", url, kw)
def fetch_via(cred, url, **kw):
    return _rec("fetch_via", url, kw, cred)
`

type shimCall struct {
	Kind    string            `json:"kind"`
	URL     string            `json:"url"`
	Cred    string            `json:"cred"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
	Timeout int               `json:"timeout"`
	SaveTo  string            `json:"save_to"`
}

type shimRun struct {
	stdout, stderr string
	code           int
	calls          []shimCall
}

// shimHarness lays out the bin dir as core's EnsureGohortLibDir does (this
// script and the same one-line curl and wget wrappers), a fake gohort module, and a fake "real" curl
// and wget further down PATH that say they ran.
type shimHarness struct {
	t                     *testing.T
	bin, fake, real, work string
}

func newShimHarness(t *testing.T) *shimHarness {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	root := t.TempDir()
	h := &shimHarness{t: t, bin: filepath.Join(root, "bin"), fake: filepath.Join(root, "fake"),
		real: filepath.Join(root, "real"), work: filepath.Join(root, "work")}
	for _, d := range []string{h.bin, h.fake, h.real, h.work} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{"_gohort_curl.py": CurlShim}
	for _, op := range []string{"curl", "wget"} {
		files[op] = "#!/usr/bin/env python3\nimport sys\nfrom _gohort_curl import main\nraise SystemExit(main(\"" + op + "\", sys.argv[1:]))\n"
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(h.bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(h.fake, "gohort.py"), []byte(fakeGohortModule), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"curl", "wget"} {
		script := "#!/bin/sh\necho REAL " + op + " \"$@\"\n"
		if err := os.WriteFile(filepath.Join(h.real, op), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func (h *shimHarness) run(op string, env map[string]string, args ...string) shimRun {
	h.t.Helper()
	log := filepath.Join(h.t.TempDir(), "calls.jsonl")
	cmd := exec.Command("python3", append([]string{filepath.Join(h.bin, op)}, args...)...)
	cmd.Dir = h.work
	cmd.Env = []string{
		"PATH=" + h.bin + ":" + h.real + ":/usr/bin:/bin",
		"PYTHONPATH=" + h.fake,
		"FAKE_LOG=" + log,
		"GOHORT_HOOK_PATH=/run/hook.sock",
		"GOHORT_NETWORK=closed",
	}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	r := shimRun{stdout: out.String(), stderr: errb.String()}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		r.code = ee.ExitCode()
	} else if err != nil {
		h.t.Fatalf("%s %v: %v", op, args, err)
	}
	if b, err := os.ReadFile(log); err == nil {
		for _, ln := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			var c shimCall
			if json.Unmarshal([]byte(ln), &c) == nil {
				r.calls = append(r.calls, c)
			}
		}
	}
	return r
}

// The flags people type become the request fetch_url sends, the reply comes
// back the way curl prints it, and the exit codes are curl's, so a script that
// branches on them branches the same way.
func TestCurlStandInSendsThroughFetchURL(t *testing.T) {
	h := newShimHarness(t)
	body := map[string]string{"FAKE_BODY": `{"name":"r"}`}

	r := h.run("curl", body, "-sS", "https://api.github.com/repos/o/r")
	if r.code != 0 || r.stdout != `{"name":"r"}` || len(r.calls) != 1 {
		t.Fatalf("plain GET: code %d out %q calls %+v err %q", r.code, r.stdout, r.calls, r.stderr)
	}
	if c := r.calls[0]; c.Kind != "fetch_url" || c.Method != "GET" || c.URL != "https://api.github.com/repos/o/r" || c.Timeout != 30 {
		t.Errorf("plain GET sent %+v", c)
	}

	r = h.run("curl", body, "-sXPOST", "-H", "Accept: application/vnd.github+json", "-d", `{"a":1}`, "-m", "5", "https://x.test/api")
	if c := r.calls[0]; c.Method != "POST" || c.Body != `{"a":1}` || c.Headers["Accept"] != "application/vnd.github+json" ||
		c.Headers["Content-Type"] != "application/x-www-form-urlencoded" || c.Timeout != 5 {
		t.Errorf("clustered -sXPOST with -H -d -m sent %+v", c)
	}

	r = h.run("curl", body, "--json", `{"a":1}`, "https://x.test/api")
	if c := r.calls[0]; c.Method != "POST" || c.Headers["Content-Type"] != "application/json" || c.Headers["Accept"] != "application/json" {
		t.Errorf("--json sent %+v", c)
	}

	r = h.run("curl", body, "-G", "-d", "q=a b", "--data-urlencode", "n=a b", "https://x.test/s")
	if c := r.calls[0]; c.Method != "GET" || c.URL != "https://x.test/s?q=a b&n=a%20b" || c.Body != "" {
		t.Errorf("-G with data sent %+v", c)
	}

	r = h.run("curl", body, "-u", "me:pw", "-A", "agent/1", "https://x.test/")
	if c := r.calls[0]; c.Headers["Authorization"] != "Basic bWU6cHc=" || c.Headers["User-Agent"] != "agent/1" {
		t.Errorf("-u -A sent %+v", c)
	}

	r = h.run("curl", body, "--credential", "github", "https://api.github.com/user")
	if c := r.calls[0]; c.Kind != "fetch_via" || c.Cred != "github" {
		t.Errorf("--credential sent %+v", c)
	}

	r = h.run("curl", body, "-i", "https://x.test/")
	if !strings.HasPrefix(r.stdout, "HTTP/1.1 200\r\nContent-Type: application/json\r\n\r\n{") {
		t.Errorf("-i printed %q", r.stdout)
	}
}

// -f is how a script tells a failed request apart, -o and -w how it saves and
// checks one: each behaves as curl's does.
func TestCurlStandInFailsSavesAndWritesOutAsCurlDoes(t *testing.T) {
	h := newShimHarness(t)
	r := h.run("curl", map[string]string{"FAKE_STATUS": "404", "FAKE_BODY": "nope"}, "-fsSL", "https://x.test/missing")
	if r.code != 22 || r.stdout != "" {
		t.Errorf("-f on a 404: code %d out %q, want 22 and nothing printed", r.code, r.stdout)
	}
	r = h.run("curl", map[string]string{"FAKE_STATUS": "404", "FAKE_BODY": "nope"}, "-s", "https://x.test/missing")
	if r.code != 0 || r.stdout != "nope" {
		t.Errorf("without -f a 404 is a success with its body: code %d out %q", r.code, r.stdout)
	}

	r = h.run("curl", map[string]string{"FAKE_BODY": "data"}, "-s", "-o", "out.json", "-w", `%{http_code}\n`, "https://x.test/f")
	if r.stdout != "200\n" {
		t.Errorf("-w printed %q", r.stdout)
	}
	if c := r.calls[0]; c.SaveTo != filepath.Join(h.work, "out.json") {
		t.Errorf("-o inside the working dir should stream through save_to, got %q", c.SaveTo)
	}
	if b, _ := os.ReadFile(filepath.Join(h.work, "out.json")); string(b) != "data" {
		t.Errorf("out.json = %q", b)
	}
	outside := filepath.Join(t.TempDir(), "x.json")
	r = h.run("curl", map[string]string{"FAKE_BODY": "data"}, "-so", outside, "https://x.test/f")
	if c := r.calls[0]; c.SaveTo != "" {
		t.Errorf("-o outside the workspace should not go through save_to, got %q", c.SaveTo)
	}
	if b, _ := os.ReadFile(outside); string(b) != "data" {
		t.Errorf("-o outside the workspace wrote %q", b)
	}
}

// What it cannot translate it refuses by name and says what it can do, and a
// refusal from the hook (Private mode, a destination check) comes through as
// curl's could-not-connect, with the hook's own sentence.
func TestCurlStandInRefusesWhatItCannotTranslate(t *testing.T) {
	h := newShimHarness(t)
	r := h.run("curl", nil, "-F", "file=@x", "https://x.test/up")
	if r.code != 2 || len(r.calls) != 0 || !strings.Contains(r.stderr, "-F is not available here") || !strings.Contains(r.stderr, "fetch_url") {
		t.Errorf("-F: code %d calls %d stderr %q", r.code, len(r.calls), r.stderr)
	}
	r = h.run("curl", nil, "--resolve", "x:443:1.2.3.4", "https://x.test/")
	if r.code != 2 || !strings.Contains(r.stderr, "--resolve is not available here") {
		t.Errorf("--resolve: code %d stderr %q", r.code, r.stderr)
	}
	r = h.run("curl", map[string]string{"FAKE_RAISE": "network blocked: this session is in Private mode"}, "https://x.test/")
	if r.code != 7 || !strings.Contains(r.stderr, "Private mode") {
		t.Errorf("a hook refusal: code %d stderr %q", r.code, r.stderr)
	}
}

// The real binary runs, arguments untouched, when the run has a network or
// there is no hook to send through: nothing that worked before changes.
func TestCurlStandInStepsAsideWhenTheRunHasANetwork(t *testing.T) {
	h := newShimHarness(t)
	for name, env := range map[string]map[string]string{
		"network open": {"GOHORT_NETWORK": "open"},
		"no hook":      {"GOHORT_HOOK_PATH": ""},
	} {
		for _, op := range []string{"curl", "wget"} {
			r := h.run(op, env, "-s", "https://x.test/")
			if strings.TrimSpace(r.stdout) != "REAL "+op+" -s https://x.test/" || len(r.calls) != 0 {
				t.Errorf("%s, %s: out %q calls %d", name, op, r.stdout, len(r.calls))
			}
		}
	}
}

func TestWgetStandInSendsThroughFetchURL(t *testing.T) {
	h := newShimHarness(t)
	body := map[string]string{"FAKE_BODY": "hello"}
	r := h.run("wget", body, "-qO-", "https://x.test/a.txt")
	if r.code != 0 || r.stdout != "hello" {
		t.Errorf("-qO-: code %d out %q err %q", r.code, r.stdout, r.stderr)
	}
	r = h.run("wget", body, "-q", "https://x.test/dir/report.csv")
	if b, _ := os.ReadFile(filepath.Join(h.work, "report.csv")); string(b) != "hello" {
		t.Errorf("default output should be the URL's file name: %q (calls %+v)", b, r.calls)
	}
	r = h.run("wget", body, "--post-data=a=1", "--header", "X-K: v", "-O", "-", "https://x.test/p")
	if c := r.calls[0]; c.Method != "POST" || c.Body != "a=1" || c.Headers["X-K"] != "v" {
		t.Errorf("--post-data sent %+v", c)
	}
	r = h.run("wget", map[string]string{"FAKE_STATUS": "500"}, "-qO-", "https://x.test/")
	if r.code != 8 {
		t.Errorf("a 500 should exit 8 as wget does, got %d", r.code)
	}
	r = h.run("wget", nil, "--mirror", "https://x.test/")
	if r.code != 2 || !strings.Contains(r.stderr, "--mirror is not available here") {
		t.Errorf("--mirror: code %d stderr %q", r.code, r.stderr)
	}
}
