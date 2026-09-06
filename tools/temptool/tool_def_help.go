package temptool

import (
	"encoding/json"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// suppress unused import — json is used by future expansions; keep
// the import handy.
var _ = json.Marshal

// helpText is the full usage guide returned by action="help". Kept
// inline (not loaded from disk) so it ships with the binary and
// can't drift from the action descriptions.
const helpText = `tool_def — runtime tool builder

Use this to define a wrapper around a shell command or an HTTP API
call. Three modes: "shell", "api", and "pipeline". Pick by what you
need to do, not by what's easier to write.

================================================================
SANDBOX FACT SHEET — read this BEFORE authoring shell-mode tools
================================================================

The shell-mode sandbox is restrictive. The most common authoring
failures come from assuming things that aren't true. Memorize this:

PYTHON
  * python3 is available
  * STDLIB ONLY. There is NO pip, NO requests, NO pillow (PIL),
    NO numpy, NO pandas, NO beautifulsoup4, NO lxml, NO opencv.
  * Safe imports: json, re, csv, sqlite3, urllib.parse, hashlib,
    hmac, datetime, collections, itertools, functools, os, sys,
    subprocess, pathlib, base64, html, xml.etree.ElementTree,
    statistics, math, random. (urllib.request is NOT on this list —
    it is a network call and tool_def REFUSES scripts that use it;
    see NETWORK.)
  * Need a third-party package? PIVOT — jq/awk for parsing,
    gohort.fetch_url for HTTP, or api mode usually reaches the
    same outcome.

SHELL
  * Interpreter is sh (POSIX), not bash. No arrays, no [[ ]],
    no <(...). Use plain sh-compatible syntax.
  * Reliably available binaries: jq, awk, sed, grep, head,
    tail, sort, uniq, tr, cut, wc, basename, dirname, date, cat,
    echo, printf, tee, xargs, find.
  * NOT available: bash-only features. curl/wget are NOT usable —
    the sandbox has no network (see NETWORK), and tool_def
    REFUSES scripts that call them.

NETWORK
  * The shell sandbox is NETWORK-ISOLATED (bwrap --unshare-net).
    curl, wget, urllib.request, socket — they ALL FAIL inside a
    shell-mode tool, and tool_def refuses a script_body that uses
    any of them at authoring time.
  * HTTP from a script goes through the gohort bridge instead:
    "from gohort import fetch_url" then fetch_url(url) — granted
    by default, no declaration needed. Authenticated or scoped
    endpoints: hook_capabilities=["fetch_via:<credential>"].
  * api mode is usually the better fit for HTTPS work anyway. It
    handles credentials, allow-listed URLs, audit logs, and rate
    limits — none of which a script gets on its own. Pick api
    mode for any work that just hits an HTTPS endpoint.

FILESYSTEM
  * Writable paths:
      {workspace_dir}  — your tool's bound sandbox. PERSISTS
                         across invocations of THIS tool when
                         StatePath is set; otherwise contents
                         survive while the tool exists but you
                         shouldn't rely on persistence across
                         deletes.
      /tmp             — tmpfs, ephemeral. WIPED every invocation.
                         Fine for scratch files within a single
                         dispatch; do NOT use for state.
  * Read-only paths: /usr, /bin, /sbin, /lib, /etc/{resolv.conf,
    hosts, ssl, alternatives} — bound from the host so binaries
    + DNS + TLS just work.
  * NOT VISIBLE: /home, /root, /var, anywhere outside the binds
    above. Don't reference user home paths or arbitrary system
    paths.
  * For state across invocations: write inside {workspace_dir}
    and declare StatePath on the tool.

THE script_body / script_name PATTERN
  * script_body = the source of a script shipped INTO the sandbox.
  * script_name = the filename it's written as (default "script.py").
  * command_template references {workspace_dir}/<script_name>.
  * One ship at registration; reused on every dispatch. You do not
    re-ship the script per call.
  * MULTI-FILE: helper files your entry script pulls in (a Python
    module it imports, a bash file it sources) are bundled into the
    tool AUTOMATICALLY — write them to the workspace with local(write)
    under the name the script imports (helper.py for "import helper"),
    and they travel with the tool and survive workspace wipes. No
    extra param; just author them beside the entry script.

THE local() TOOL IS A DIFFERENT SANDBOX
  * local() lets you iterate on a script BEFORE wrapping it as a
    tool. Its sandbox is per-user, not per-tool.
  * After local-testing, when you call tool_def, the script_body
    you pass gets shipped into the TOOL's fresh sandbox. They're
    separate environments.

PROBING FOR BINARIES (workspace probe action)
  * Before authoring a tool that depends on a non-POSIX binary
    (convert, ffmpeg, yt-dlp, etc.), call workspace(action="probe")
    to verify it's present:
        workspace(action="probe", name="ffmpeg")
        → "available at /usr/bin/ffmpeg" or "NOT available"
  * No user confirmation required (the probe is scope-limited to
    a "command -v" lookup with a validated identifier — zero
    injection surface). Call it freely during design.
  * If the probe says NOT available, pivot — don't author a tool
    that will fail at dispatch.

EMITTING ATTACHMENTS (images, video, audio)
  * Shell-mode tools CAN attach binary content to the reply by
    writing a marker block to stdout:

        <<<ATTACH:image/png
        <base64 data, can span multiple lines>
        ATTACH_END>>>

  * Supported mimes: image/* (PNG, JPEG, GIF, WEBP), video/*
    (MP4, WEBM, MOV), audio/* (MP3, WAV, M4A, OGG).
  * Multiple markers per stdout = multiple attachments.
  * The dispatcher strips the marker from the LLM-visible output
    and routes the base64 to the session's attachment channel.
  * Use this when the tool PROCESSES binaries (fetch+convert,
    transcode, crop). For plain fetch-and-attach, prefer the
    built-ins: find_image, fetch_image, generate_image,
    download_video. They're more efficient (no base64 round-trip
    through stdout) and don't need authoring.

If you're tempted to author a tool that imports requests, runs
under bash, or writes to /tmp expecting persistence — STOP and
pivot. The shell sandbox will reject those at dispatch time.

================================================================
WHEN TO USE WHICH MODE — decide by the work, not by what's familiar
================================================================

**COMPOSE BEFORE YOU BUILD.** Before authoring anything that touches
the network, check whether an existing framework tool already does
the fetch step:

  web_search       — search the web, returns ranked results
  fetch_url        — GET a URL, returns body
  find_image       — search for an image and save best match to workspace
  fetch_image      — download a specific image URL to workspace
  download_video   — download a video from a supported site to workspace

If one of these covers the fetch, your authoring job is the LOCAL
PROCESSING ON TOP — write that as a shell-mode tool and chain the two
via pipeline_steps. Don't reimplement the fetch. The framework's
versions handle credentials, retries, redirects, content-type sniffing,
size caps, caching, and observability — none of which a curl-in-shell
script gets.

Decision tree:

  Network involved?
    └─ YES — does an existing tool already fetch what you need?
        ├─ YES → pipeline mode: chain that tool + a shell-mode
        │        processor you author for the transformation.
        └─ NO  → api mode (HTTPS endpoint the framework can't
                  already reach).
    └─ NO  — purely local computation? → shell mode.

That's the rule. The two most common mistakes:
  (1) Reaching for shell mode + a Python urllib (or curl) script
      when the task is "call this HTTPS endpoint and pass the
      response back." Use api mode. Invented method names, JSON
      parse bugs, URL-encoding mistakes, even invisible homoglyphs
      in URLs are all eliminated by api mode.
  (2) Re-authoring a fetch when fetch_url or web_search already
      does it. The right shape is pipeline_steps that chains the
      existing fetch tool with your custom processor — you only
      author the part that doesn't already exist.

api mode — for HTTPS endpoints the framework doesn't already reach.
  Use when:
    - The task is to hit an authenticated HTTPS URL with a
      registered credential (Bearer, header, query, basic_auth) —
      pass credential="<name>".
    - The task is an unauthenticated public API (Open-Meteo,
      wttr.in, exchange rates, geocoders, etc.) — pass
      credential="no_auth". Same machinery (allow-list, audit log,
      rate limit) without an auth header.
  Do NOT write a Python urllib or curl-in-shell client around an
  HTTPS endpoint. There is no situation where a hand-rolled HTTP
  client in shell mode is the right answer.

shell mode — for local computation in a sandbox.
  Use when:
    - You need to parse, transform, or aggregate data with a
      script (Python, Bash, jq, awk, sed) — and the data is
      passed in as an arg, NOT fetched by the script itself.
    - You need persistent state across invocations (StatePath).
    - You need a multi-step computation that operates on
      caller-supplied input only.
  Sandbox: bubblewrap, network technically reachable but using it
  for HTTP work is the anti-pattern called out above. Constraints
  documented in the SANDBOX FACT SHEET at the top of this help —
  read that before authoring shell-mode tools.

pipeline mode — for composition (THIS is how "use existing tools").
  Two variants:
    pipeline_steps (DETERMINISTIC): each step is one tool call,
      args templated with {param} (caller args) and $N / $N.field
      (prior step output). No inner LLM. Cheap, fast, predictable.
      The right choice for "fetch X then process X" — pair an
      existing fetch tool with a shell-mode processor you author.
    pipeline_prompt (ADAPTIVE): a sub-agent LLM runs the chain
      with reasoning between steps. Use when the chain needs
      branching ("if the search returns a paper PDF, fetch and
      summarize; if it returns a webpage, scrape and summarize").

Worked example — fetch a JSON endpoint and project just the fields
you want, composing fetch_url + a shell processor:

  Step 1 — author the shell processor (works on caller-supplied data):
    tool_def(action="create", mode="shell",
             name="project_user_summary",
             description="Project name + repo count from a GitHub user JSON.",
             params={"json": {"type": "string", "description": "raw JSON body"}},
             command_template="echo {json} | jq -c '{login, public_repos, followers}'")

  Step 2 — author the pipeline that chains fetch_url + the processor:
    tool_def(action="create", mode="pipeline",
             name="gh_user_summary",
             description="Get a GitHub user's summary by username.",
             params={"user": {"type": "string", "description": "GitHub username"}},
             pipeline_tools=["fetch_url", "project_user_summary"],
             pipeline_steps=[
               {tool: "fetch_url", args: {url: "https://api.github.com/users/{user}"}, name: "page"},
               {tool: "project_user_summary", args: {json: "$page"}}
             ])

What you DIDN'T have to write: the HTTPS fetch, retry handling,
content-type sniffing, error formatting, size caps. fetch_url
already gives you all of that.

When NOT to use pipeline mode:
  - The whole flow is one HTTPS call: just use api mode directly.
  - The processing is so trivial it fits in api mode's
    response_pipe (which is jq / awk on the response body —
    cheaper than a pipeline_steps chain when nothing else is in
    the chain).

================================================================
WRAPPING A SCRIPT — fast path
================================================================

The single-call shortcut (this example uses jq, but Python, Bash,
awk, sed all work the same way — pick the smallest tool for the
job, not a Python script by default):

  tool_def(action=create, mode="shell",
           name="extract_titles",
           description="Extract titles from a JSON list of items.",
           params={"input": {"type": "string", "description": "JSON array on stdin."}},
           script_body="jq -r '.[] | .title'",
           script_name="run.jq",
           command_template="echo {input} | jq -r '.[] | .title'",
           persist=true)

What happens: the sandbox is auto-minted, script_body is written to
{workspace_dir}/script.py (or whatever script_name you set), and
the tool is registered. One call, no setup.

Why script_body beats inlining the script in command_template:
shell-quoting Python (or any non-trivial script) inside a template
is a footgun. Embedded quotes break, line breaks vanish, dollar
signs get expanded. Pass the source verbatim through script_body
and the file system handles it correctly. The template only sees
filenames and {arg} placeholders, which are safe.

================================================================
command_template — placeholders are pre-quoted; DON'T wrap them
================================================================

Every {param} placeholder in command_template is SHELL-QUOTED by
the framework at dispatch time. Wrapping a placeholder in quotes
yourself creates nested quoting and breaks the command.

WRONG (nested quotes — the framework's quote is INSIDE your quote):
  command_template:  curl '{url}' -H "X-Auth: {token}"
  → renders to:     curl ''https://...'' -H "X-Auth: 'abc123'"
  → shell sees doubled and nested quotes, command parses wrong

RIGHT (bare placeholders — let the framework do the quoting):
  command_template:  curl {url} -H X-Auth:\ {token}
  → renders to:     curl 'https://...' -H X-Auth:\ 'abc123'
  → values arrive as separate argv entries, correctly quoted

When script_body does the heavy lifting (typical case), pass the
values as bare placeholders and read them positionally in the script:
  command_template:  python3 {workspace_dir}/run.py {url} {token}
  Then in run.py:    url, token = sys.argv[1], sys.argv[2]

The rule: NEVER put a quote character around a {placeholder}, in
either single or double form. Literal quotes ELSEWHERE in the
template are fine — only the placeholders are auto-quoted.

================================================================
url_template / body_template — placeholders are URL-encoded; DON'T
wrap them either
================================================================

Same rule applies for api-mode and toolbox-mode url_template: the
framework URL-encodes each {placeholder} value at substitution and
splices it into the template. Literal quote characters in the
template (single OR double) survive into the final URL and the
upstream service sees them in the value.

WRONG (literal quotes survive into the URL):
  url_template:  https://api.example.com/search?q='{query}'
  with {query}="Seattle WA":
  → renders to: https://api.example.com/search?q='Seattle%20WA'
  → upstream sees q=%27Seattle%20WA%27 (encoded single quotes
    around the value — usually a 400 / "no results" / wrong match)

RIGHT (bare placeholder):
  url_template:  https://api.example.com/search?q={query}
  with {query}="Seattle WA":
  → renders to: https://api.example.com/search?q=Seattle%20WA

Path segments work the same way:
  url_template:  https://api.example.com/users/{username}/repos
  with {username}="cmcoffee":
  → renders to: https://api.example.com/users/cmcoffee/repos

A path placeholder KEEPS its slashes, so a value that spans
segments substitutes as real separators:
  url_template:  https://api.example.com/dav/{calendar_path}
  with {calendar_path}="/195178399/calendars/home/":
  → renders to: .../dav/195178399/calendars/home/

When the API wants a NESTED PATH as ONE segment, add ":encoded"
("segment" is a synonym) and the whole value is percent-encoded,
slashes included. GitLab's files endpoint is the case this exists
for — it takes the file path as an id:
  url_template:  /projects/{id}/repository/files/{path:encoded}/raw?ref={ref}
  with {path}="src/handlers/webhook_retry.py":
  → renders to: .../files/src%2Fhandlers%2Fwebhook_retry.py/raw?ref=dev

PASS NATURAL VALUES either way. Do NOT pre-encode: "%2F" arrives
as "%252F", because the escaper encodes "%" as it must — a literal
percent in a value is indistinguishable from an encoding you did
by hand. Before this modifier existed there was no third thing to
try: a raw "/" and a hand-written "%2F" both 404 on that endpoint.
A modifier that is not "encoded" / "segment" is REFUSED, at
authoring time and at dispatch, rather than left in the URL as a
literal.

Same rule for body_template — bare {placeholders}, no wrapping
quotes. The framework JSON-encodes string values for you (the
encoder adds its own surrounding quotes), so writing
  body_template:  {"key":"{value}"}
double-quotes the value. Write
  body_template:  {"key":{value}}
instead and let the encoder handle the JSON quoting.

================================================================
AUTHORING A SHELL-MODE TOOL — script_body inline is the path
================================================================

The canonical pattern is ONE call that ships the script content with
the tool record:

  tool_def(action=create, mode="shell",
           name="get_weather_by_city",
           description="Current weather for a US city via wttr.in.",
           script_name="weather.py",
           script_body="""
             import sys
             from gohort import fetch_url
             city = sys.argv[1]; state = sys.argv[2]
             url = f"https://wttr.in/{city},{state}?format=j1"
             print(fetch_url(url)["body"])
           """,
           command_template="python3 {workspace_dir}/weather.py {city} {state}",
           params={
             "city": {"type": "string", "description": "City name"},
             "state": {"type": "string", "description": "Two-letter state"}
           },
           test_args={"city": "Santa Cruz", "state": "CA"})

Why script_body inline:
  - The script content lives ON the tool record. Survives workspace
    wipes (e.g. a new chat session) because the framework redeploys
    it on every dispatch.
  - One call, not three. Workers don't waste rounds on a
    write-then-run-then-wrap dance.
  - test_args runs the freshly-authored tool with concrete inputs
    and folds the result (or error) into your response — if it
    errors, fix it inline and re-call tool_def; if it works,
    you're done.

CRITICAL: command_template must reference the same filename you
passed as script_name. If script_name="weather.py", command_template
must say {workspace_dir}/weather.py — NOT {workspace_dir}/script.py
or any other name. Mismatch → dispatch fails with "no such file."

Iterating-and-testing via local(write) + local(run) BEFORE the
tool_def call is OPTIONAL — useful when you're debugging a non-
trivial algorithm interactively. Once it works, copy the verified
content into script_body and call tool_def ONCE. Do NOT skip
script_body and hope the workspace file survives — it won't, across
sessions or after workspace pruning.

================================================================
NETWORK POLICY — shell sandbox is network-isolated by default
================================================================

Shell-mode tools run in a bwrap sandbox with --unshare-net. That
means: urllib.request, socket.connect, curl, wget — ALL FAIL from
inside the sandbox, and tool_def REFUSES a script_body that uses
any of them at authoring time.

HTTP goes through the gohort bridge instead. The bare hooks —
fetch_url, browse_page, log — are granted BY DEFAULT for any
shell-mode tool with script_body; no declaration needed:

  tool_def(action=create, mode="shell",
           name="get_weather_by_city",
           script_body="""
             from gohort import fetch_url
             import sys, json
             city, state = sys.argv[1], sys.argv[2]
             data = fetch_url(f"https://wttr.in/{city},{state}?format=j1")
             print(data["body"])
           """,
           command_template="python3 {workspace_dir}/weather.py {city} {state}",
           params={...},
           test_args={"city": "Santa Cruz", "state": "CA"})

Why this shape (vs raw network):
  - Every outbound call is logged in gohort's audit trail
  - Secrets stay in the credential store, out of the script's hands
  - Same posture across sessions — no surprises on a fresh workspace

For authenticated endpoints, declare the credential and route the
request THROUGH it (allow-list enforced, auth injected server-side,
the script never sees the secret):

  hook_capabilities=["fetch_via:openweather"]

Then in the script:

  from gohort import fetch_via
  data = fetch_via("openweather",
                   "https://api.openweathermap.org/data/2.5/weather?q=Seattle")
  print(data["body"])

  # fetch_via also takes method, body, and extra request headers —
  # e.g. a CalDAV PROPFIND that needs a Depth header and an XML body:
  #   fetch_via("apple_caldav", url, method="PROPFIND", body=xml,
  #             headers={"Depth": "1", "Content-Type": "application/xml"})
  # The credential's auth header always wins over anything you pass.
  # Returns {status, status_line, body}; status is the NUMERIC code, same
  # as fetch_url, so a plain  if r["status"] != 200:  works unchanged.
  # Do NOT write  int(r["status"].split()[1])  — that was a workaround
  # for an older shape and now raises on an int.

(secret:<name> exists for the rare API that can't be reached that
way — the script gets the decrypted value and injects it itself.
Prefer fetch_via.)

The escape hatch (raw_network=true) is RESERVED for narrow cases:
  - persistent-mode REPLs over non-HTTP (psql, redis-cli, ssh-like)
  - shell tools that NEED raw TCP/UDP and can't use the hook

For ordinary HTTP-shaped work, the default fetch_url hook is the
right answer. raw_network=true should be a deliberate exception
flagged in the description, not a default.

================================================================
state_path — for tools that need to remember
================================================================

The sandbox itself persists across dispatches of the same tool —
your script can write a file in dispatch #1 and read it back in
dispatch #2. That's the default behavior.

state_path is only needed when you want one specific subdir to be
treated as durable state separate from the rest of the sandbox.
Most tools don't need this; leave it unset.

  command_template="python3 {workspace_dir}/run.py --db {workspace_dir}/state/counts.db"
  state_path="state"

================================================================
api mode and response_pipe
================================================================

api-mode tool shape (authenticated — credential registered in admin):

  tool_def(action=create, mode="api",
           name="get_issue",
           description="Get a GitHub issue by number.",
           credential="github_api",
           url_template="https://api.github.com/repos/{owner}/{repo}/issues/{number}",
           method="GET",
           params={
             "owner": {"type": "string", "description": "..."},
             "repo": {"type": "string", "description": "..."},
             "number": {"type": "string", "description": "..."}
           },
           response_pipe="jq -c '{title, state, body, user: .user.login}'")

Public API (no auth) — same shape, credential="none":

  tool_def(action=create, mode="api",
           name="get_weather_forecast",
           description="Forecast for a lat/lon via Open-Meteo.",
           credential="none",
           url_template="https://api.open-meteo.com/v1/forecast?latitude={lat}&longitude={lon}&current=temperature_2m,weather_code&forecast_days={days}",
           method="GET",
           params={
             "lat": {"type": "string", "description": "..."},
             "lon": {"type": "string", "description": "..."},
             "days": {"type": "string", "description": "1-16"}
           },
           response_pipe="jq -c '{current: .current, daily: .daily}'")

response_pipe is optional but powerful. The API response BODY is
piped to your sh -c command on stdin. Whatever lands on stdout is
what reaches your context. Use it to project only the fields you
care about, drop noise, cap list lengths.

  Examples:
    response_pipe="jq -c '[.items[] | {id, name, status}]'"
    response_pipe="jq -c '.[:20]'"
    response_pipe="jq -r '.message'"

Notes:
  - The HTTP status line is stripped before piping and re-prepended
    to your output. You don't need "tail -n +2".
  - The pipe is skipped on non-2xx responses; you'll see the raw
    error in that case.
  - The pipe runs in the same sandbox as shell mode (no network,
    no writable fs, /tmp tmpfs).
  - Available binaries: jq, awk, sed, grep, head, tail, tr, cut.

URL placeholders are URL-encoded at dispatch. Body placeholders are
JSON-encoded. Both are safe against injection.

================================================================
WRITING THE DESCRIPTION — one or two sentences, then stop
================================================================

A tool's description and its param descriptions are re-sent on EVERY
turn the tool sits in a catalog, for the whole life of the tool. You
write them once; every future conversation pays for them. Treat the
length as a budget you are spending on someone else's behalf.

CAPS (enforced — create and update are refused over them):
  tool description          500 characters
  toolbox action            250 characters
  each param description    250 characters

The description answers exactly two questions: WHAT does this do, and
WHEN do I reach for it instead of something else. That is one or two
sentences.

  RIGHT: "Get a GitHub issue by number, including title, state, body
          and author."
  RIGHT: "Search Moltbook posts by keyword. Use get_post for the full
          body of a single hit."

Do NOT put these in the description:
  * worked examples or sample calls (the params already show the shape)
  * a restatement of the params (they are right there, with their own
    descriptions)
  * failure modes and troubleshooting ("if you get a 404, check the
    id") — that belongs in the ERROR the tool returns, where it is
    read only when it actually happens, instead of on every turn
  * setup or authoring history ("built against v2 of the API, uses the
    acme_api credential") — the caller cannot act on it
  * emphasis markup and repetition. Saying it once is saying it.

Param descriptions are one line: what the value is, plus the format
only when it is not obvious from the name and type.

  RIGHT: "Issue number, e.g. 1421."
  RIGHT: "Sort order: newest | oldest | top."
  WRONG: "The number of the issue you want to fetch. You can find this
          in the URL of the issue page, after /issues/. It must be a
          number, not the issue title..."

If a rule genuinely has to reach the caller before they call, it goes
in the ONE param it constrains, not in the tool description.

================================================================
WHAT THE TOOL RETURNS — anchor list items, never omit a field
================================================================

The output shape is part of the tool's contract, same as its params.
These two rules apply in every mode; a tool can pass action="test"
clean and still produce wrong answers in use by breaking them.

ANCHOR EVERY ITEM IN A LIST RESULT.

A tool that returns many similar-shaped items — search hits,
headlines, rows, files, messages — must give each item an id. Without
one the caller can read every field correctly and still attach it to
the wrong item: the attributes survive, the binding to their item
doesn't. It looks like a hallucination and isn't. It's two neighbors
in an undifferentiated wall of text.

WRONG (nothing to point at):
  ### Cracker Barrel CEO steps down after rebrand chaos
  *BBC Business* — Mon, 27 Jul 2026
  Julie Masino will exit after backlash over the logo redesign.

RIGHT (each item carries a handle):
  [id: bbc-cr49z0r54nko] Cracker Barrel CEO steps down
  source: BBC Business | date: 2026-07-27

Prefer a stable opaque id (the upstream's own id, a URL slug) over a
position number: ordinals shift between calls, so [3] names a
different item an hour later. If another tool consumes the selection,
have it take the id as a param rather than a retyped description of
the item. The retyping is where the drift happens.

In api mode this is a response_pipe job:

  response_pipe="jq -c '[.items[] | {id, title, source, published}]'"

STATE ABSENT FIELDS, DON'T DROP THEM.

An omitted key reads as a gap to fill from whatever else is in
context. An explicit null reads as a fact. The caller can't tell
"this tool doesn't report that" from "this item happens to lack it"
unless you say which.

  WRONG:  {"id": "a1", "title": "...", "source": "BBC"}
  RIGHT:  {"id": "a1", "title": "...", "source": "BBC",
           "summary": null, "author": null}

This matters most for the field the caller needs in order to act. If
your tool hands back a headline with no body and the caller's job is
to summarize the body, the missing body gets invented from the
nearest plausible text in context. Emitting "summary": null makes the
caller go fetch it or report that it can't.

Keep the field set identical across items and use a delimiter the
caller can't mistake for content. Ragged records let values slide
across item boundaries.

================================================================
WebDAV / CalDAV — the Depth header is not optional
================================================================

A calendar-query REPORT (or a PROPFIND) applies at the DEPTH the
request asks for. Depth 0 means "the collection resource itself" —
which is never a VEVENT — so the server answers 207 Multi-Status
with an EMPTY multistatus and no error anywhere:

  <?xml version="1.0" encoding="UTF-8"?><multistatus xmlns="DAV:"/>

That is indistinguishable from "the calendar has no events." It is
the single most common reason a CalDAV read tool looks correct,
verifies clean, and returns nothing forever. Set the header:

  tool_def(action=create, mode="api",
           name="list_calendar_events",
           credential="apple_caldav",
           url_template="/{principal}/calendars/{calendar_id}/",
           method="REPORT",
           headers={"Depth": "1"},
           content_type="application/xml",
           body_template="""<?xml version="1.0" encoding="utf-8" ?>
             <c:calendar-query xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
               <d:prop><d:getetag/><c:calendar-data/></d:prop>
               <c:filter>
                 <c:comp-filter name="VCALENDAR">
                   <c:comp-filter name="VEVENT">
                     <c:time-range start="{start}" end="{end}"/>
                   </c:comp-filter>
                 </c:comp-filter>
               </c:filter>
             </c:calendar-query>""",
           response_extract={"select": "response",
                             "where": {"has": "calendar-data"},
                             "fields": {"href": "href", "ical": "calendar-data"}},
           params={...})

Rules that make the difference between working and silently empty:
  * headers={"Depth": "1"} on REPORT and PROPFIND. Always.
  * The filter MUST nest comp-filter VCALENDAR > VEVENT > time-range.
    A time-range at the VCALENDAR level matches nothing — same empty
    207, no error.
  * <c:calendar-data/> is a SELF-CLOSING prop. Putting <d:prop>
    children inside it (<d:summary/> etc.) is not a partial-retrieval
    spec and returns nothing useful — those are iCalendar properties,
    not DAV ones.
  * content_type="application/xml" so the body substitutes RAW.
  * Parse with response_extract, not a hand-written XML pipe.
  * A WRITE is a plain PUT of one .ics to <calendar>/<uid>.ics with
    content_type="text/calendar" — no Depth, no filter. A create tool
    working proves NOTHING about a read tool: they exercise different
    verbs, different headers, and different server-side logic.

If a read returns 2xx with zero records, check in this order:
Depth header, filter nesting, the time range, and only THEN the
calendar path — a path that accepts a PUT is a path that exists.

================================================================
toolbox mode — wrap a whole API surface
================================================================

When the work is "expose several endpoints of one API as tools"
(GitHub: users + repos + issues; Stripe: charges + invoices +
customers; an internal service with 5 read endpoints), use mode=
"toolbox" instead of authoring N separate api-mode tools. A toolbox
surfaces as ONE catalog entry with action="<sub>" dispatch — the
same UX as the framework's built-in grouped tools (tool_def itself
is one). Cleaner for the catalog, one credential shared across
actions, one approval.

Shape:

    tool_def(action="create", mode="toolbox",
             name="github",
             description="Query GitHub: users, repos, issues.",
             credential="github_api",     # shared across all actions
             actions=[
               {name: "get_user",
                description: "Get a user's public profile.",
                url_template: "https://api.github.com/users/{username}",
                method: "GET",
                params: {"username": {"type": "string",
                                      "description": "GitHub username"}},
                response_pipe: "jq -c '{login, name, bio, public_repos, followers}'"},
               {name: "get_repo",
                description: "Get a repository's metadata.",
                url_template: "https://api.github.com/repos/{owner}/{repo}",
                method: "GET",
                params: {"owner": {"type": "string"}, "repo": {"type": "string"}},
                response_pipe: "jq -c '{full_name, description, stars: .stargazers_count, language}'"},
               {name: "list_issues",
                description: "List issues on a repo by state.",
                url_template: "https://api.github.com/repos/{owner}/{repo}/issues?state={state}",
                method: "GET",
                params: {"owner": {"type": "string"}, "repo": {"type": "string"},
                         "state": {"type": "string",
                                   "description": "open | closed | all"}},
                response_pipe: "jq -c '[.[] | {number, title, state, user: .user.login}]'"}
             ])

Called as:

    github(action="get_user", username="octocat")
    github(action="get_repo", owner="cmcoffee", repo="gohort")
    github(action="list_issues", owner="cmcoffee", repo="gohort", state="open")

Each action is structurally a single api-mode endpoint — same URL
template substitution, same method/body_template/response_pipe
semantics. The toolbox is a packaging primitive on top.

Why toolbox over N api-mode tools:
  * One catalog entry (the toolbox name) vs N (gh_get_user,
    gh_get_repo, ...). Much cleaner when the catalog is already
    busy.
  * One credential declared at toolbox level vs repeated per tool.
  * One pending-approval entry vs N — admin reviews "the github
    toolbox" as one unit.
  * Adding a new endpoint = adding one entry to actions[], not
    minting a new tool_def call.

When NOT to use toolbox:
  * The work is one HTTPS call — mode="api" is leaner.
  * The endpoints share NOTHING (different APIs, different
    credentials) — author separate api-mode tools per endpoint.
  * The "actions" would have wildly different params with no
    semantic relation — that's usually a sign the work isn't really
    a wrapper around one API.

================================================================
verify — action="test" (DO THIS BEFORE YOU CALL A TOOL DONE)
================================================================

Authoring a tool and NOT exercising it is how a broken
tool reaches a user: a POST action with no body_template (so a required
field is never sent → live 400 "content must be a string"), a jq
response_pipe with a syntax error, a URL that 404s. action="test"
catches these BEFORE the tool ships.

    tool_def(action="test",
             name="moltbook",
             cases=[
               {action: "feed",     args: {limit: 5, sort: "new"}},
               {action: "get_post", args: {post_id: "<a real id>"}},
               {action: "comment",  args: {post_id: "<real id>", content: "test"}}
             ])

What it does per endpoint:
  * Checks every REQUIRED param is actually sent — referenced in the
    url_template or the body_template. An unreferenced required param
    is the #1 bug (the "must be a string" 400). Fails offline, no
    network needed.
  * Renders the body_template with your sample args and confirms it is
    valid JSON.
  * Compile-checks the response_pipe (a broken jq filter fails here,
    not live).
  * READ endpoints (GET): makes a REAL call, asserts a 2xx, and runs
    the response_pipe against the real body (catches shape mismatches).
  * WRITE endpoints (POST/PUT/PATCH/DELETE): body-validated but NOT
    auto-fired — the report tells you to make one manual call and
    confirm a 2xx yourself (so test never spams the live service).

Pass a cases entry per endpoint with REAL values so reads hit 2xx.
Returns a PASS/FAIL table. Fix every FAIL with action="update" and
re-run until green. Treat a tool as done only when test is clean and
each write endpoint has had one confirmed live call.

SHELL tools go through the same action, with checks that fit a script:

    tool_def(action="test",
             name="create_calendar_entry",
             cases=[{args: {summary: "test", start_time: "...",
                            end_time: "..."}}])

  * Syntax-checks script_body with the real interpreter (python3 -m
    py_compile, bash -n, node --check). An unterminated string or a
    bad indent fails HERE instead of on every future call.
  * Reports how each required param reaches the script: substituted
    into command_template, or ONLY as a lowercase env var (params are
    always exported as env vars — os.environ["summary"], $summary).
  * RUNS the tool with your case args and checks the exit status.
    This is a genuine dispatch — its side effects really happen, so
    pass args you're willing to have executed.

Without a cases entry a shell tool reports UNVERIFIED, not PASS:
running it is the only thing that proves a script works.

================================================================
persist
================================================================

**Your tool_def call is the creation. Stop second-guessing it.**
There is no separate "register with admin" step you need to ask
about. The moment tool_def(action="create", ...) returns success,
your tool is callable in this session AND auto-queued for admin
review in the background. The admin decides whether to keep it
past the session; you don't ask, you author. Saying "want me to
register it now?" after writing a script means you skipped the
tool_def call — go make it.

persist=false (default): the tool exists only for the current
session. Disappears at session end. No approval required.

persist=true: the tool is queued for operator approval. Once
approved it survives across sessions and shows up in your tool
catalog every time. Use this for tools you'll reuse; don't use it
for one-off transformations.

================================================================
cache (optional)
================================================================

cache opts a tool into persistent result memoization — the same
call returns the prior result instead of re-executing. Use for
tools whose output is expensive AND deterministic given the same
args:

  - api tools hitting paid or rate-limited endpoints
  - shell tools that download / convert / process external content
  - anything where re-running on a follow-up turn would waste
    bandwidth, money, or wall-time

Shape (all fields optional inside the cache object):

  key             {param}-template that produces the cache key.
                  Default = hash of all args. Set this when one
                  arg uniquely identifies the result (a URL, a
                  document ID) and other args don't affect output.
  ttl             Duration string: "30d", "12h", "30m", "45s".
                  Empty = no expiry.
  scope           "user" (default; dedup per-user across sessions),
                  "session" (per-conversation), or "global" (shared
                  across all users — only when the result is
                  content-addressable AND privacy-safe).
  invalidate_when Array of post-hit checks. Each entry has the form
                  "kind:expression". Today one kind:
                    file_exists:<path-template>
                  The rendered path must exist on disk or the entry
                  is dropped and the tool re-runs.

Example — api tool with TTL (current-weather lookup, ~10min fresh):

    create(mode="api",
           name="current_weather",
           description="Get current weather for lat/lon.",
           credential="none",
           url_template="https://api.open-meteo.com/v1/forecast?latitude={lat}&longitude={lon}&current_weather=true",
           method="GET",
           params={"lat": {"type": "number", "description": "latitude"},
                   "lon": {"type": "number", "description": "longitude"}},
           cache={"key": "{lat},{lon}", "ttl": "10m", "scope": "user"})

The same (lat, lon) within 10 minutes returns the prior response
without re-hitting Open-Meteo.

Example — shell tool with file_exists invalidation (download once):

    create(mode="shell",
           name="download_url_to_workspace",
           description="Download a URL into the workspace as out.bin.",
           command_template="curl -sSL -o {workspace_dir}/out.bin {url}",
           params={"url": {"type": "string", "description": "source URL"}},
           cache={"key": "{url}",
                  "scope": "user",
                  "invalidate_when": ["file_exists:{workspace_dir}/out.bin"]})

Same URL on a later turn: if the workspace file is still present,
the cached result string is returned instantly and the file is NOT
re-fetched. If the workspace was reaped between runs, file_exists
fails and the tool downloads again.

DO NOT set cache on tools whose output legitimately differs across
calls (status checks, "fetch latest news", anything time-sensitive
beyond your TTL). Cache is for input → output determinism, not for
"make it generally faster."

================================================================
common pitfalls
================================================================

- Wrapping an HTTPS endpoint with a Python+urllib (or curl-in-shell)
  script. This is the most expensive mistake in this system. Use
  api mode. For unauthenticated public APIs pass credential="none".
  Symptoms when you don't: invented method names (.UpperCase()),
  hand-written URL strings with invisible homoglyphs (Cyrillic 'о'
  for Latin 'o'), JSON parsing errors, retry loops blaming your
  own syntax. None of those exist in api mode.

- Trying to fetch a script over api mode and run it. Don't. Pass
  the script source via script_body.

- Embedding a multi-line script inside command_template. Shell
  quoting will fight you. Use script_body — the file system handles
  the source verbatim and the template only sees filenames.

- Wrapping a script you haven't tested. Use the local(write/run)
  iterate loop first; only wrap once it actually works.

- Using api mode for arithmetic or text munging. Use shell mode
  with a small Python or jq command — no credential needed.

- Defining response_pipe that produces empty output. The LLM-
  visible result is what comes off stdout; if your jq filter
  doesn't match, you get nothing. Test the filter against a real
  response first.

- Returning a list of items with no per-item id, or dropping keys
  the item doesn't have. Both invite the caller to bind a field to
  the wrong item or invent one outright. See "WHAT THE TOOL RETURNS"
  above.
`

// pruneRequired drops entries from a required list that no longer name a param.
// Used when an action update replaces params without re-sending required: the
// caller removed a param, so the required entry naming it is dead weight that
// would otherwise block every dispatch.
//
// Conservative about shapes it doesn't recognize — a nil or non-list required,
// or a non-map params, is returned untouched. This runs inside an edit path; a
// helper that discards data it merely failed to parse would be worse than the
// bug it fixes.
func pruneRequired(required, params any) any {
	// The merge base comes from actionToArgs, which emits required as a native
	// []string — normalize so a round-tripped list still prunes.
	if rs, ok := required.([]string); ok {
		conv := make([]any, len(rs))
		for i, s := range rs {
			conv[i] = s
		}
		required = conv
	}
	reqList, ok := required.([]any)
	if !ok || len(reqList) == 0 {
		return required
	}
	paramMap, ok := params.(map[string]any)
	if !ok {
		return required
	}
	kept := make([]any, 0, len(reqList))
	for _, r := range reqList {
		name, ok := r.(string)
		if !ok {
			kept = append(kept, r) // unrecognized entry: leave it alone
			continue
		}
		if _, exists := paramMap[strings.TrimSpace(name)]; exists {
			kept = append(kept, r)
		} else {
			Debug("[temptool] update: dropped required %q — the action no longer declares that param", name)
		}
	}
	return kept
}

// scriptCallsHook reports whether a script body invokes one of the gohort hook
// helpers. Matches the forms that actually appear — a call, a qualified call,
// or membership in an import list (where the name may be first, middle, or
// last, so substring matching on "import <name>" misses two of the three).
// Scoped to syntactic positions rather than any mention, because this check
// FAILS a verification and a comment shouldn't be able to do that.
func scriptCallsHook(body, name string) bool {
	if strings.Contains(body, name+"(") || strings.Contains(body, "gohort."+name) {
		return true
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "from ") && !strings.HasPrefix(line, "import ") {
			continue
		}
		i := strings.Index(line, "import ")
		if i < 0 {
			continue
		}
		for _, part := range strings.Split(line[i+len("import "):], ",") {
			// "x as y" imports under an alias; the import still grants the call.
			if f := strings.Fields(strings.TrimSpace(part)); len(f) > 0 && f[0] == name {
				return true
			}
		}
	}
	return false
}

// hookCapabilityDeclared matches the server's own gate: a bare capability, or
// any qualified form of it ("fetch_via:openweather", "secret:apikey").
func hookCapabilityDeclared(caps []string, want string) bool {
	prefix := want + ":"
	for _, c := range caps {
		c = strings.TrimSpace(c)
		if c == want || strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}
