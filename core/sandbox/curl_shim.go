package sandbox

// curl and wget, as a sandboxed command has them.
//
// A sandboxed shell has no network of its own: its namespace is cut, so a real
// curl fails to resolve the host, and an agent learns one wasted round later
// to reach for fetch_url, which it does every time, because curl is what it
// types first. These stand-ins ARE fetch_url for the flags people actually
// type. The request goes through the same hook a script's fetch_url uses, so
// every control that applies there applies here: Private mode, the audit, the
// hook's destination checks, and a credential only when one is granted.
// Nothing in them dials anything.
//
// When the run DOES have a network (a command that declared raw_network, or a
// deployment that leaves shell networking open), or there is no hook to call
// (a response pipe), the real binary runs instead with its arguments
// untouched, so nothing that worked before changes. The run says which it is
// through GOHORT_NETWORK, set where the network decision is made
// (buildSandboxedShellCmd). core writes it into the shim bin dir with the
// rest of the shims (sandboxToolShims in core/sandbox_hook.go).
//
// A flag the stand-in does not translate is refused by name, with the list of
// what it does: a wrong guess at an unfamiliar flag would send a different
// request than the one asked for, which is worse than none.

// CurlShim is the shared logic behind the curl and wget wrappers in the
// sandbox bin dir, written there as _gohort_curl.py. No backticks: it is a Go
// raw string.
const CurlShim = `#!/usr/bin/env python3
# curl / wget stand-ins for a sandbox with no network of its own: the request
# goes through gohort's fetch_url hook. See core/sandbox/curl_shim.go.
import base64
import os
import sys

try:
    from urllib.parse import quote, urlsplit
except Exception:
    quote = None
    urlsplit = None

SUPPORTED = {
    "curl": "-X -H -d --data-raw --data-binary --data-urlencode --json -G -u -A -e -b -o -O -i -I -L -s -S -f --fail-with-body -w '%{http_code}' -m -k --compressed -v, and --credential NAME for a gohort credential this run was granted",
    "wget": "-q -nv -O FILE (- for stdout) --header --post-data --body-data --method -U -S -T --no-check-certificate, and --credential NAME for a gohort credential this run was granted",
}


def _real(op):
    here = os.path.realpath(os.path.dirname(os.path.realpath(__file__)))
    for d in os.environ.get("PATH", "").split(os.pathsep):
        if not d or os.path.realpath(d) == here:
            continue
        p = os.path.join(d, op)
        if os.path.isfile(p) and os.access(p, os.X_OK):
            return p
    return None


def _passthrough(op, argv):
    real = _real(op)
    if real is None:
        sys.stderr.write("%s: command not found\n" % op)
        return 127
    os.execv(real, [op] + list(argv))


def _unsupported(op, what):
    sys.stderr.write(
        "%s: %s is not available here. This sandbox has no direct network, so %s is gohort's "
        "stand-in: it sends the request through fetch_url, under the same controls. It understands %s. "
        "For anything else use the fetch_url command (fetch_url URL --method M --header 'K: V' --body DATA) "
        "or the fetch_url tool.\n" % (op, what, op, SUPPORTED[op]))
    return 2


def _read_data(v, keep_newlines):
    if not v.startswith("@"):
        return v
    name = v[1:]
    data = sys.stdin.read() if name == "-" else open(name).read()
    if not keep_newlines:
        data = data.replace("\r", "").replace("\n", "")
    return data


def _urlencode(v):
    if quote is None:
        return v
    if v.startswith("@"):
        return quote(_read_data(v, True), safe="")
    if "=" in v:
        name, val = v.split("=", 1)
        return name + "=" + quote(val, safe="")
    return quote(v, safe="")


def _has(headers, name):
    return any(k.lower() == name.lower() for k in headers)


def _header(headers, h):
    if ":" not in h:
        return
    k, v = h.split(":", 1)
    headers[k.strip()] = v.strip()


def _out_path(path):
    # save_to writes straight to a file in the workspace, which keeps binary
    # content whole. A path outside it (a fresh /tmp) is written here from the
    # text body instead.
    full = os.path.abspath(path)
    inside = full.startswith(os.getcwd().rstrip(os.sep) + os.sep)
    return full, inside


def _send(op, url, method, headers, body, credential, timeout, save_to):
    import gohort
    kw = {"method": method, "headers": headers, "body": body, "timeout": timeout}
    if save_to:
        kw["save_to"] = save_to
    if credential:
        return gohort.fetch_via(credential, url, **kw)
    return gohort.fetch_url(url, **kw)


def _fetch(op, url, method, headers, body, credential, timeout, out):
    # Returns (response dict, error message). out is a local path or None.
    save_to = None
    if out:
        full, inside = _out_path(out)
        if inside:
            save_to = full
    try:
        r = _send(op, url, method, headers, body, credential, timeout, save_to)
    except Exception as e:
        if save_to is None:
            return None, str(e)
        try:
            r = _send(op, url, method, headers, body, credential, timeout, None)
            save_to = None
        except Exception as e2:
            return None, str(e2)
    if not isinstance(r, dict):
        r = {"status": 200, "headers": {}, "body": str(r)}
    if out and save_to is None:
        with open(out, "w") as f:
            f.write(r.get("body") or "")
    return r, None


def _status_block(r):
    lines = ["HTTP/1.1 %s" % r.get("status")]
    hdrs = r.get("headers") or {}
    for k in sorted(hdrs):
        v = hdrs[k]
        if isinstance(v, list):
            v = ", ".join(str(x) for x in v)
        lines.append("%s: %s" % (k, v))
    return "\r\n".join(lines) + "\r\n\r\n"


def _write_out(fmt, r, url):
    hdrs = r.get("headers") or {}
    ctype = ""
    for k in hdrs:
        if k.lower() == "content-type":
            ctype = hdrs[k] if not isinstance(hdrs[k], list) else ", ".join(hdrs[k])
    body = r.get("body") or ""
    size = r.get("bytes") if r.get("bytes") is not None else len(body.encode("utf-8"))
    vals = {"http_code": str(r.get("status")), "response_code": str(r.get("status")),
            "content_type": ctype, "size_download": str(size), "url_effective": url}
    out, i = [], 0
    while i < len(fmt):
        if fmt.startswith("%{", i) and "}" in fmt[i:]:
            j = fmt.index("}", i)
            out.append(vals.get(fmt[i + 2:j], ""))
            i = j + 1
        elif fmt.startswith("\\n", i):
            out.append("\n"); i += 2
        elif fmt.startswith("\\t", i):
            out.append("\t"); i += 2
        else:
            out.append(fmt[i]); i += 1
    return "".join(out)


_CURL_VALUE = {
    "-X": "method", "--request": "method",
    "-H": "header", "--header": "header",
    "-d": "data", "--data": "data", "--data-ascii": "data",
    "--data-raw": "data_raw", "--data-binary": "data_binary",
    "--data-urlencode": "data_urlencode", "--json": "json",
    "-u": "user", "--user": "user",
    "-A": "agent", "--user-agent": "agent",
    "-e": "referer", "--referer": "referer",
    "-b": "cookie", "--cookie": "cookie",
    "-o": "output", "--output": "output",
    "-w": "write_out", "--write-out": "write_out",
    "-m": "max_time", "--max-time": "max_time",
    "--connect-timeout": "skip", "--retry": "skip", "--retry-delay": "skip", "--retry-max-time": "skip",
    "--url": "url", "--credential": "credential", "--cred": "credential",
}
_CURL_FLAG = {
    "-s": "silent", "--silent": "silent", "-S": "show_error", "--show-error": "show_error",
    "-L": "skip", "--location": "skip", "-f": "fail", "--fail": "fail",
    "--fail-with-body": "fail_with_body", "-i": "include", "--include": "include",
    "-I": "head", "--head": "head", "-O": "remote_name", "--remote-name": "remote_name",
    "-G": "get", "--get": "get", "-k": "skip", "--insecure": "skip", "--compressed": "skip",
    "-v": "skip", "--verbose": "skip", "-#": "skip", "--progress-bar": "skip",
    "-N": "skip", "--no-buffer": "skip", "--globoff": "skip", "-g": "skip",
}


def _curl(argv):
    o = {"headers": {}, "data": [], "json": [], "urls": []}
    i, n = 0, len(argv)

    def take(name, val):
        if name == "header":
            _header(o["headers"], val)
        elif name in ("data", "data_raw", "data_binary", "data_urlencode"):
            if name == "data":
                val = _read_data(val, False)
            elif name == "data_binary":
                val = _read_data(val, True)
            elif name == "data_urlencode":
                val = _urlencode(val)
            o["data"].append(val)
        elif name == "json":
            o["json"].append(_read_data(val, True))
        elif name == "url":
            o["urls"].append(val)
        elif name != "skip":
            o[name] = val

    while i < n:
        a = argv[i]
        if a == "--":
            o["urls"].extend(argv[i + 1:])
            break
        if a in _CURL_VALUE:
            if i + 1 >= n:
                return _unsupported("curl", a + " with no value")
            take(_CURL_VALUE[a], argv[i + 1]); i += 2; continue
        if a in _CURL_FLAG:
            o[_CURL_FLAG[a]] = True; i += 1; continue
        if a.startswith("--"):
            return _unsupported("curl", a)
        if a.startswith("-") and len(a) > 1:
            # A cluster of short flags, "-sSL" or "-sXPOST": a flag that takes
            # a value takes the rest of the cluster, or the next argument.
            k = 1
            while k < len(a):
                f = "-" + a[k]
                if f in _CURL_FLAG:
                    o[_CURL_FLAG[f]] = True; k += 1; continue
                if f in _CURL_VALUE:
                    rest = a[k + 1:]
                    if rest:
                        take(_CURL_VALUE[f], rest)
                    elif i + 1 < n:
                        i += 1
                        take(_CURL_VALUE[f], argv[i])
                    else:
                        return _unsupported("curl", f + " with no value")
                    break
                return _unsupported("curl", f)
            i += 1
            continue
        o["urls"].append(a)
        i += 1

    if len(o["urls"]) != 1:
        if not o["urls"]:
            sys.stderr.write("curl: no URL specified\n")
            return 2
        return _unsupported("curl", "more than one URL in one call")
    url = o["urls"][0]
    if "://" not in url:
        url = "http://" + url
    headers = o["headers"]
    method = o.get("method")
    body = ""
    if o["json"]:
        body = "".join(o["json"])
        if not _has(headers, "Content-Type"):
            headers["Content-Type"] = "application/json"
        if not _has(headers, "Accept"):
            headers["Accept"] = "application/json"
    elif o["data"]:
        body = "&".join(o["data"])
        if o.get("get"):
            url += ("&" if "?" in url else "?") + body
            body = ""
        elif not _has(headers, "Content-Type"):
            headers["Content-Type"] = "application/x-www-form-urlencoded"
    if not method:
        if o.get("head"):
            method = "HEAD"
        elif body:
            method = "POST"
        else:
            method = "GET"
    if o.get("user"):
        headers["Authorization"] = "Basic " + base64.b64encode(o["user"].encode()).decode()
    if o.get("agent"):
        headers["User-Agent"] = o["agent"]
    if o.get("referer"):
        headers["Referer"] = o["referer"]
    if o.get("cookie"):
        if "=" not in o["cookie"]:
            return _unsupported("curl", "-b with a cookie file")
        headers["Cookie"] = o["cookie"]
    out = o.get("output")
    if o.get("remote_name") and not out:
        path = urlsplit(url).path if urlsplit else url
        out = os.path.basename(path.rstrip("/"))
        if not out:
            sys.stderr.write("curl: Remote file name has no length\n")
            return 23
    if out == "-":
        out = None
    try:
        timeout = int(float(o.get("max_time") or 30))
    except ValueError:
        timeout = 30

    r, err = _fetch("curl", url, method, headers, body, o.get("credential"), timeout, out)
    quiet = o.get("silent") and not o.get("show_error")
    if err is not None:
        if not quiet:
            sys.stderr.write("curl: (7) %s\n" % err)
        return 7
    status = r.get("status") or 0
    failed = isinstance(status, int) and status >= 400 and (o.get("fail") or o.get("fail_with_body"))
    if o.get("include") or o.get("head"):
        sys.stdout.write(_status_block(r))
    if not out and not o.get("head") and not (failed and not o.get("fail_with_body")):
        sys.stdout.write(r.get("body") or "")
    if o.get("write_out"):
        sys.stdout.write(_write_out(o["write_out"], r, url))
    if failed:
        if not quiet:
            sys.stderr.write("curl: (22) The requested URL returned error: %s\n" % status)
        return 22
    return 0


_WGET_VALUE = {
    "-O": "output", "--output-document": "output",
    "--header": "header", "--post-data": "post", "--body-data": "body",
    "--method": "method", "-U": "agent", "--user-agent": "agent",
    "-T": "timeout", "--timeout": "timeout",
    "--credential": "credential", "--cred": "credential",
}
_WGET_FLAG = {
    "-q": "quiet", "--quiet": "quiet", "-nv": "quiet", "--no-verbose": "quiet",
    "-S": "server_response", "--server-response": "server_response",
    "--no-check-certificate": "skip",
}


def _wget(argv):
    o = {"headers": {}, "urls": []}
    i, n = 0, len(argv)
    while i < n:
        a = argv[i]
        if "=" in a and a.startswith("--") and a.split("=", 1)[0] in _WGET_VALUE:
            a, v = a.split("=", 1)
            argv = argv[:i] + [a, v] + argv[i + 1:]
            n = len(argv)
        if a in _WGET_VALUE:
            if i + 1 >= n:
                return _unsupported("wget", a + " with no value")
            name, val = _WGET_VALUE[a], argv[i + 1]
            if name == "header":
                _header(o["headers"], val)
            else:
                o[name] = val
            i += 2
            continue
        if a in _WGET_FLAG:
            o[_WGET_FLAG[a]] = True; i += 1; continue
        if a.startswith("--"):
            return _unsupported("wget", a)
        if a.startswith("-") and len(a) > 1:
            k = 1
            while k < len(a):
                f = "-" + a[k]
                if f in _WGET_FLAG:
                    o[_WGET_FLAG[f]] = True; k += 1; continue
                if f in _WGET_VALUE:
                    rest = a[k + 1:]
                    if rest:
                        val = rest
                    elif i + 1 < n:
                        i += 1
                        val = argv[i]
                    else:
                        return _unsupported("wget", f + " with no value")
                    if _WGET_VALUE[f] == "header":
                        _header(o["headers"], val)
                    else:
                        o[_WGET_VALUE[f]] = val
                    break
                return _unsupported("wget", f)
            i += 1
            continue
        o["urls"].append(a)
        i += 1

    if len(o["urls"]) != 1:
        if not o["urls"]:
            sys.stderr.write("wget: missing URL\n")
            return 1
        return _unsupported("wget", "more than one URL in one call")
    url = o["urls"][0]
    if "://" not in url:
        url = "http://" + url
    headers = o["headers"]
    body = o.get("post") or o.get("body") or ""
    method = (o.get("method") or ("POST" if o.get("post") else "GET")).upper()
    if o.get("post") and not _has(headers, "Content-Type"):
        headers["Content-Type"] = "application/x-www-form-urlencoded"
    if o.get("agent"):
        headers["User-Agent"] = o["agent"]
    out = o.get("output")
    if out is None:
        path = urlsplit(url).path if urlsplit else url
        out = os.path.basename(path.rstrip("/")) or "index.html"
    if out == "-":
        out = None
    try:
        timeout = int(float(o.get("timeout") or 30))
    except ValueError:
        timeout = 30

    r, err = _fetch("wget", url, method, headers, body, o.get("credential"), timeout, out)
    if err is not None:
        sys.stderr.write("wget: %s\n" % err)
        return 4
    status = r.get("status") or 0
    if o.get("server_response"):
        sys.stderr.write(_status_block(r))
    if isinstance(status, int) and status >= 400:
        sys.stderr.write("wget: server returned error: HTTP %s\n" % status)
        return 8
    if out is None:
        sys.stdout.write(r.get("body") or "")
    elif not o.get("quiet"):
        size = r.get("bytes") if r.get("bytes") is not None else len((r.get("body") or "").encode("utf-8"))
        sys.stderr.write("'%s' saved [%s]\n" % (out, size))
    return 0


def main(op, argv):
    # The real binary when this run has a network, or nothing to send through.
    if os.environ.get("GOHORT_NETWORK") == "open" or not os.environ.get("GOHORT_HOOK_PATH"):
        return _passthrough(op, argv)
    if op == "wget":
        return _wget(list(argv))
    return _curl(list(argv))


if __name__ == "__main__":
    raise SystemExit(main(os.path.basename(sys.argv[0]), sys.argv[1:]))
`
