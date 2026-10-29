package sandbox

// git, as a sandboxed command has it.
//
// A sandboxed shell has no network of its own, so a real git clone fails to
// resolve the host. Running the clone on the server instead would put a
// repository someone else wrote through the server's git, outside the
// sandbox, where a clone-time git bug is the server's problem. So git still
// runs here, in the sandbox, and only its HTTP leaves: for the commands that
// fetch (clone, fetch, pull, ls-remote, submodule, remote), the stand-in
// makes sure the run's loopback relay is up (relay.go) and runs the real git
// with its remote addresses rewritten to it (url.<relay>.insteadOf, given
// with -c for this run only). gohort reads each request on its side
// (relayGit in core/sandbox_hook.go), sends it under the rules every request
// from the sandbox meets, adds a credential covering the host there, and
// streams the reply back.
//
// What follows from that shape:
//   - the token never enters the sandbox, and the clone's .git/config keeps
//     the plain URL it was given;
//   - git@host:path and ssh://git@host/path are fetched over https from the
//     same host, the way most hosts serve both;
//   - a push is refused by name, here and again by gohort;
//   - every other git command runs the real binary untouched;
//   - what the relay had to say (a refusal, a sign-in to add, a cut) is asked
//     for after git, which reports a failed request only by its status, and
//     printed.
//
// When the run has a network of its own (GOHORT_NETWORK=open), or no relay,
// the real git runs as typed. core writes this into the shim bin dir as
// _gohort_git.py (sandboxToolShims in core/sandbox_hook.go). No backticks: it
// is a Go raw string. Python 3.6 or later.

// GitShim is the git stand-in.
const GitShim = `#!/usr/bin/env python3
# git stand-in for a sandbox with no network of its own: git runs here, its
# HTTP goes through gohort's relay. See core/sandbox/git_shim.go.
import json
import os
import socket
import subprocess
import sys

# The commands that reach a remote. Everything else runs the real git as is.
FETCHING = {"clone", "fetch", "pull", "ls-remote", "submodule", "remote", "fetch-pack"}
PUSHING = {"push", "send-pack"}
# Global options that take the next argument as their value.
VALUED = {"-C", "-c", "--git-dir", "--work-tree", "--namespace", "--config-env", "--super-prefix"}

HERE = os.path.dirname(os.path.realpath(__file__))


def _real():
    here = os.path.realpath(HERE)
    for d in os.environ.get("PATH", "").split(os.pathsep):
        if not d or os.path.realpath(d) == here:
            continue
        p = os.path.join(d, "git")
        if os.path.isfile(p) and os.access(p, os.X_OK):
            return p
    return None


def subcommand(argv):
    """The git command argv runs, past the global options; "" for none."""
    i = 0
    while i < len(argv):
        a = argv[i]
        if a in VALUED:
            i += 2
            continue
        if a.startswith("-"):
            i += 1
            continue
        return a
    return ""


def relay_notes():
    """What the relay had to say during this run, read once."""
    try:
        s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        s.settimeout(10)
        s.connect(os.environ["GOHORT_HOOK_PATH"])
        s.sendall(b'{"method":"relay_notes","params":{}}\n')
        buf = b""
        while b"\n" not in buf:
            chunk = s.recv(65536)
            if not chunk:
                break
            buf += chunk
        s.close()
        resp = json.loads(buf.split(b"\n", 1)[0].decode("utf-8"))
        return (resp.get("result") or {}).get("notes") or []
    except Exception:
        return []


def main(argv):
    real = _real()
    if real is None:
        sys.stderr.write("git: command not found\n")
        return 127
    port = os.environ.get("GOHORT_RELAY_PORT", "")
    token = os.environ.get("GOHORT_RELAY_TOKEN", "")
    if os.environ.get("GOHORT_NETWORK") == "open" or not os.environ.get("GOHORT_HOOK_PATH") or not port or not token:
        os.execv(real, ["git"] + list(argv))
    cmd = subcommand(argv)
    if cmd in PUSHING:
        sys.stderr.write("git %s: not available here. This sandbox has no direct network; gohort relays git's fetches "
                         "(clone, fetch, pull, ls-remote), not pushes, which change something outside it.\n" % cmd)
        return 1
    if cmd not in FETCHING:
        os.execv(real, ["git"] + list(argv))
    if subprocess.call([sys.executable, os.path.join(HERE, "_gohort_relay")]) != 0:
        sys.stderr.write("git (through gohort): the run's relay could not start, so this cannot reach a remote\n")
        return 1
    base = "http://127.0.0.1:%s/%s/" % (port, token)
    cfg = [
        "-c", "url.%shttps/.insteadOf=https://" % base,
        "-c", "url.%shttp/.insteadOf=http://" % base,
        "-c", "url.%sssh/.insteadOf=git@" % base,
        "-c", "url.%sssh/.insteadOf=ssh://git@" % base,
        # Auth is gohort's to add, on its side: nothing here asks for one.
        "-c", "credential.helper=",
    ]
    env = dict(os.environ)
    env["GIT_TERMINAL_PROMPT"] = "0"
    # The relay is on loopback: never through a proxy the run was given.
    env["NO_PROXY"] = env["no_proxy"] = "127.0.0.1,localhost"
    rc = subprocess.call([real] + cfg + list(argv), env=env)
    for note in relay_notes():
        sys.stderr.write("git (through gohort): %s\n" % note)
    return rc
`
