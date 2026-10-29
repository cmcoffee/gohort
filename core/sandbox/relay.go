package sandbox

// The run's loopback relay: how a client that dials for itself reaches out of
// a sandbox with no network, through gohort.
//
// A sandboxed command can reach nothing but the hook's unix socket. git, and
// any HTTP client that honours HTTPS_PROXY (pip, npm, go, Python's requests
// and urllib), can only speak TCP. So one small forwarder runs inside the
// sandbox on 127.0.0.1:<port>, and pipes each connection, byte for byte, to
// the hook's relay method. It understands nothing: the HTTP is read on
// gohort's side (handleRelay in core/sandbox_hook.go), which decides what
// may go and sends it under the rules every request from the sandbox meets.
//
// The port and the token come from the hook, per run (GOHORT_RELAY_PORT,
// GOHORT_RELAY_TOKEN). On Linux the run has a network namespace of its own,
// so its loopback is private. On macOS (Seatbelt) there is no such namespace:
// the loopback is the host's, so the port is one free on the host, the
// Seatbelt profile opens that port alone, and every request must carry the
// token, or any process on the machine could use the relay while the run
// lasts.
//
// It starts on demand: the git stand-in starts it before a fetch, and a run
// with the proxy grant starts it before its command (proxyEnv). It returns
// once it is listening, serving from a child it leaves behind, which exits
// when the hook's socket goes (the hook lives exactly as long as the run);
// on Linux the run's process namespace ends it too. A second start finds it
// listening and returns.

// RelayPort is the env var naming the run's relay port.
const RelayPort = "GOHORT_RELAY_PORT"

// RelayShim is the forwarder, written to the sandbox bin dir as
// _gohort_relay. No backticks: it is a Go raw string. Python 3.6 or later.
const RelayShim = `#!/usr/bin/env python3
# The run's loopback relay: a byte pipe from 127.0.0.1:$GOHORT_RELAY_PORT to
# gohort's hook. See core/sandbox/relay.go.
import os
import socket
import sys
import threading
import time

PREAMBLE = b'{"method":"relay","params":{}}\n'


def listening(port):
    try:
        socket.create_connection(("127.0.0.1", port), 1).close()
        return True
    except Exception:
        return False


def pipe(src, dst):
    try:
        while True:
            data = src.recv(65536)
            if not data:
                break
            dst.sendall(data)
    except Exception:
        pass
    try:
        dst.shutdown(socket.SHUT_WR)
    except Exception:
        pass


def serve(client, hook):
    try:
        up = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        up.connect(hook)
        up.sendall(PREAMBLE)
    except Exception:
        client.close()
        return
    t = threading.Thread(target=pipe, args=(client, up), daemon=True)
    t.start()
    pipe(up, client)
    t.join()
    client.close()
    up.close()


def watch(hook):
    # The hook lives as long as the run: when its socket goes, so does this.
    while os.path.exists(hook):
        time.sleep(1)
    os._exit(0)


def main():
    hook = os.environ.get("GOHORT_HOOK_PATH", "")
    try:
        port = int(os.environ.get("` + RelayPort + `", "0"))
    except ValueError:
        port = 0
    if not hook or not port:
        return 1
    if listening(port):
        return 0
    srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    try:
        srv.bind(("127.0.0.1", port))
        srv.listen(64)
    except OSError:
        # Another start won the race: it is this run's relay all the same.
        return 0 if listening(port) else 1
    if os.fork() > 0:
        return 0  # bound and listening; the child serves
    os.setsid()
    devnull = os.open(os.devnull, os.O_RDWR)
    for fd in (0, 1, 2):
        os.dup2(devnull, fd)
    threading.Thread(target=watch, args=(hook,), daemon=True).start()
    while True:
        try:
            client, _ = srv.accept()
        except Exception:
            continue
        threading.Thread(target=serve, args=(client, hook), daemon=True).start()


if __name__ == "__main__":
    sys.exit(main())
`
