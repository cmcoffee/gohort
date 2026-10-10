package temptool

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	. "github.com/cmcoffee/oddjob/core"
)

// inferCommandTemplate produces a sensible default command_template
// for a script_body when the caller forgot to specify one. Returns
// empty string when no interpreter can be guessed — caller falls
// back to a directive error.
//
// Strategy:
//   - Shebang on line 1 → use it directly (script must be executable
//     at dispatch; the framework writes script files with 0700 so a
//     shebang line resolves naturally).
//   - Recognized extension → known interpreter:
//     .py → python3, .sh/.bash → bash, .jq → jq -f, .js → node,
//     .rb → ruby, .pl → perl.
//   - Otherwise empty (no inference).
//
// Params are NOT inlined as positional placeholders. The framework
// already passes every declared param to the script as an environment
// variable (see RunSandboxedShellWithEnv) — adding positional args on
// top forces the script to choose between sys.argv and os.environ,
// and the ordering of those positional args (alphabetical?
// insertion-order?) becomes a footgun the LLM keeps tripping on.
// Standardizing on env-vars-only means: scripts read params with
// `os.environ['name']` (Python) / `$name` (bash); ordering is a
// non-question; insertion + retrieval are both by NAME.
//
// If the caller really wants positional args (third-party scripts
// that take strict argv shapes), they supply an explicit
// command_template — the inference only fires when command_template
// is omitted.
// PrepareScriptBody is the shared implementation of the script_body authoring
// shortcut: infer command_template when the author omitted it, materialize the
// script into the session workspace under a collision-proof canonical name, and
// hand back the fields to stamp onto the TempTool record.
//
// It exists so the two authoring surfaces can't drift. tool_def grew this
// behavior inline; add_tool never had it, and silently DROPPED a script_body it
// was handed — the tool record kept a command_template pointing at a file that
// was never written, so the first dispatch failed with a "script doesn't exist"
// error that blamed a "legacy tool". Any surface that accepts script_body must
// route through here.
//
// cmd is the caller's command_template ("" to infer). Returns the effective
// template plus the LLM-facing and canonical script names. A blank scriptBody
// is a no-op that echoes cmd back, so callers can call it unconditionally.
func PrepareScriptBody(sess *ToolSession, toolName, cmd, scriptBody, scriptName string, params any) (outCmd, outScriptName, outCanonical string, err error) {
	if strings.TrimSpace(scriptBody) == "" {
		return cmd, "", "", nil
	}
	if scriptName == "" {
		scriptName = "script.py"
	}
	if strings.ContainsAny(scriptName, "/\\") {
		return "", "", "", fmt.Errorf("script_name must be a single filename (no path separators)")
	}
	if strings.TrimSpace(cmd) == "" {
		paramOrder, perr := paramNamesInDefinitionOrder(params)
		if perr == nil {
			cmd = inferCommandTemplate(scriptName, scriptBody, paramOrder)
			if cmd != "" {
				Log("[temptool] auto-inferred command_template=%q (script=%s, params=%v): supply command_template explicitly for kwargs / stdin / non-positional shapes",
					cmd, scriptName, paramOrder)
			}
		}
	}
	if strings.TrimSpace(cmd) == "" {
		return "", "", "", fmt.Errorf("command_template is required (or supply script_body with a recognized extension.py/.sh/.bash/.js/.jq/.rb, and the framework will infer it; declared params reach the script as ENVIRONMENT VARIABLES, not positional argv, read them with os.environ['name'])")
	}
	canonical := canonicalScriptName(toolName, scriptName, scriptBody)
	if _, werr := EnsureSessionWorkspace(sess); werr != nil {
		return "", "", "", fmt.Errorf("auto-mint workspace: %w", werr)
	}
	scriptPath := filepath.Join(sess.WorkspaceDir, canonical)
	if mkerr := os.MkdirAll(filepath.Dir(scriptPath), 0700); mkerr != nil {
		return "", "", "", fmt.Errorf("create parent dir for script %q: %w", canonical, mkerr)
	}
	if werr := os.WriteFile(scriptPath, []byte(scriptBody), 0700); werr != nil {
		return "", "", "", fmt.Errorf("write script %q: %w", canonical, werr)
	}
	// The template must actually reference the script, or the tool is born
	// broken — the exact failure this helper exists to prevent.
	if !strings.Contains(cmd, scriptName) && !strings.Contains(cmd, "{workspace_dir}") {
		return "", "", "", fmt.Errorf("script_body was written to %s (canonical %s) but command_template %q doesn't reference it: add {workspace_dir}/%s to the template", scriptPath, canonical, cmd, scriptName)
	}
	return cmd, scriptName, canonical, nil
}

func inferCommandTemplate(scriptName, scriptBody string, paramOrder []string) string {
	if scriptName == "" {
		return ""
	}
	interpreter := ""
	if shebang := firstShebang(scriptBody); shebang != "" {
		// Use the file directly; the kernel resolves the shebang.
		interpreter = ""
	} else {
		ext := strings.ToLower(filepath.Ext(scriptName))
		switch ext {
		case ".py":
			interpreter = "python3"
		case ".sh", ".bash":
			interpreter = "bash"
		case ".jq":
			interpreter = "jq -f"
		case ".js":
			interpreter = "node"
		case ".rb":
			interpreter = "ruby"
		case ".pl":
			interpreter = "perl"
		default:
			return "" // unknown — caller must supply command_template
		}
	}
	// paramOrder is intentionally unused — see the doc comment above.
	_ = paramOrder
	var b strings.Builder
	if interpreter != "" {
		b.WriteString(interpreter)
		b.WriteByte(' ')
	}
	b.WriteString("{workspace_dir}/")
	b.WriteString(scriptName)
	return b.String()
}

// detectForbiddenNetworkPatterns returns a human-readable description
// of any sandbox-incompatible network-library use found in script_body.
// Empty string means clean.
//
// The patterns we refuse are the ones that DEFINITELY cannot work
// inside the bwrap sandbox (--unshare-net). False positives here have
// real cost — refusing a legitimate use breaks authoring — so we keep
// the list focused on the calls that are network-doing (not just
// `import socket` since AF_UNIX sockets work, not just `import urllib`
// since urllib.parse is useful for URL encoding).
//
// Patterns:
//
//	import requests / from requests import …      → requests library
//	import urllib2                                → Py2 legacy net
//	urllib.request.urlopen / urllib.urlopen       → urllib network
//	from urllib.request import …                  → urllib network
//	from urllib import urlopen                    → urllib network
//	import http.client / from http.client …       → http.client
//	socket.create_connection / socket.connect(    → raw socket dialing
//	curl <url> / wget <url>                       → shell HTTP
//	subprocess.* with "curl" or "wget"            → wrapped shell HTTP
func detectForbiddenNetworkPatterns(script string) string {
	patterns := []struct {
		needle string
		label  string
	}{
		// Python network libs.
		{"import requests", "import requests"},
		{"from requests import", "from requests import …"},
		{"import urllib2", "import urllib2"},
		{"urllib.request", "urllib.request"},
		{"from urllib.request import", "from urllib.request import …"},
		{"from urllib import urlopen", "from urllib import urlopen"},
		{"urllib.urlopen", "urllib.urlopen"},
		{"import http.client", "import http.client"},
		{"from http.client import", "from http.client import …"},
		{"socket.create_connection", "socket.create_connection"},
		{"socket.connect(", "socket.connect()"},
		// Shell HTTP. Match with leading space / line-start so a
		// substring inside a longer word doesn't false-positive.
		{"\ncurl ", "curl"},
		{" curl ", "curl"},
		{"\nwget ", "wget"},
		{" wget ", "wget"},
		// Catch curl/wget wrapped in subprocess too.
		{"subprocess.run([\"curl", "subprocess.run([\"curl …\"])"},
		{"subprocess.run(['curl", "subprocess.run(['curl …'])"},
		{"subprocess.run([\"wget", "subprocess.run([\"wget …\"])"},
		{"subprocess.run(['wget", "subprocess.run(['wget …'])"},
		{"os.system(\"curl", "os.system(\"curl …\")"},
		{"os.system('curl", "os.system('curl …')"},
		{"os.system(\"wget", "os.system(\"wget …\")"},
		{"os.system('wget", "os.system('wget …')"},
	}
	for _, p := range patterns {
		if strings.Contains(script, p.needle) {
			return p.label
		}
	}
	return ""
}

// ungrantedCalls represents one or more credential-bearing calls
// found in script_body that aren't covered by HookCapabilities.
// calls and suggest are pre-formatted for the directive error.
type ungrantedCalls struct {
	calls   string
	suggest string
}

// findUngrantedCredentialCalls scans script_body for oddjob.secret(...)
// and oddjob.fetch_via(...) calls, extracts the credential name from
// the first string-literal arg, and returns any names that aren't
// covered by the existing HookCapabilities. Used to produce a
// directive error at authoring time instead of letting the tool fail
// at dispatch with a confusing "HookError: secret %q not granted".
//
// Best-effort parse — only catches the common shape with a string
// literal as the first arg (`oddjob.secret("openweather")`,
// `oddjob.fetch_via("github", url)`). Dynamic args (variable, f-string,
// dict lookup) slip through silently — they'll surface at dispatch
// where the hook denies the unknown credential.
//
// Returns the zero value when nothing's missing (calls == "" means
// no error to raise).
func findUngrantedCredentialCalls(script string, granted []string) ungrantedCalls {
	grantedSet := map[string]bool{}
	for _, c := range granted {
		grantedSet[c] = true
	}
	var missingCalls []string
	var suggestParts []string
	scan := func(prefix, capKind string) {
		idx := 0
		for {
			pos := strings.Index(script[idx:], prefix)
			if pos < 0 {
				return
			}
			start := idx + pos + len(prefix)
			// Skip whitespace.
			for start < len(script) && (script[start] == ' ' || script[start] == '\t') {
				start++
			}
			if start >= len(script) {
				return
			}
			quote := script[start]
			if quote != '"' && quote != '\'' {
				// Not a string literal — can't extract the name.
				idx = idx + pos + len(prefix)
				continue
			}
			end := strings.IndexByte(script[start+1:], quote)
			if end < 0 {
				return
			}
			name := script[start+1 : start+1+end]
			needed := capKind + ":" + name
			if !grantedSet[needed] {
				missingCalls = append(missingCalls, fmt.Sprintf("oddjob.%s(%q)", capKind, name))
				suggestParts = append(suggestParts, fmt.Sprintf("%q", needed))
				grantedSet[needed] = true // dedupe across multiple call sites
			}
			idx = idx + pos + len(prefix)
		}
	}
	scan("oddjob.secret(", "secret")
	scan("oddjob.fetch_via(", "fetch_via")
	if len(missingCalls) == 0 {
		return ungrantedCalls{}
	}
	return ungrantedCalls{
		calls:   strings.Join(missingCalls, ", "),
		suggest: strings.Join(suggestParts, ", "),
	}
}

// firstShebang returns the shebang line of scriptBody (without the
// leading "#!") when the body starts with one. Empty otherwise.
func firstShebang(scriptBody string) string {
	if !strings.HasPrefix(scriptBody, "#!") {
		return ""
	}
	end := strings.IndexByte(scriptBody, '\n')
	if end < 0 {
		end = len(scriptBody)
	}
	return strings.TrimSpace(scriptBody[2:end])
}

// paramNamesInDefinitionOrder extracts param names from the raw
// `params` argument value while preserving the order the LLM
// specified. parseParamsArg returns a map[string]ToolParam which
// loses order; for inferring positional command_template we want the
// order the LLM listed them in. Uses encoding/json's Decoder Token
// stream to walk an object's keys in insertion order.
//
// Falls back to alphabetical when the input isn't a parseable JSON
// object (the typed-map path takes over via parseParamsArg downstream,
// so this is a best-effort ordering for the auto-inference shortcut).
func paramNamesInDefinitionOrder(v any) ([]string, error) {
	if v == nil {
		return nil, fmt.Errorf("params is nil")
	}
	var raw []byte
	switch s := v.(type) {
	case string:
		raw = []byte(s)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("params is not a JSON object")
	}
	var keys []string
	depth := 0
	for dec.More() || depth > 0 {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch d := t.(type) {
		case json.Delim:
			switch d {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		case string:
			if depth == 0 {
				keys = append(keys, d)
			}
		}
	}
	return keys, nil
}

var (
	oddjobFromImport = regexp.MustCompile(`(?m)^[ \t]*from[ \t]+oddjob[ \t]+import[ \t]+(\([^)]*\)|[^\n#]+)`)
	oddjobAttrCall   = regexp.MustCompile(`(?:^|[^\w.])oddjob\.([A-Za-z_]\w*)\s*\(`)
	credentialToolRe = regexp.MustCompile(`^(?:fetch_url|call)_(\w+)$`)
)

// unknownOddjobName reports a name a script takes from the oddjob module that
// the module does not export, with what to write instead; "" when every name is
// real. Observed: a script imported fetch_url_gemini_api, which is the name of
// the credential's catalog tool, not a function. It passed authoring and died
// on its first run with an ImportError, and the "fix" that followed dropped the
// import on the belief that the runtime injects the name. Nothing does.
func unknownOddjobName(script string) string {
	known := SandboxReachableNames()
	var names []string
	for _, m := range oddjobFromImport.FindAllStringSubmatch(script, -1) {
		list := strings.Trim(strings.TrimSpace(m[1]), "()")
		for _, item := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == '\n' }) {
			if f := strings.Fields(item); len(f) > 0 && f[0] != "*" && f[0] != "\\" {
				names = append(names, f[0])
			}
		}
	}
	for _, m := range oddjobAttrCall.FindAllStringSubmatch(script, -1) {
		names = append(names, m[1])
	}
	for _, n := range names {
		if known[n] {
			continue
		}
		if c := credentialToolRe.FindStringSubmatch(n); c != nil {
			return fmt.Sprintf("script_body takes %q from the oddjob module, but that is the name of a tool an agent calls, not a function a script can import: nothing provides it, and the script fails on its first run. From a script, the simplest route is `from oddjob import fetch_url` and `fetch_url(url, method=\"POST\", body=..., headers={...})` on a URL at the credential's own host: the call goes through %q automatically, with no grant to declare. To name the credential explicitly (the only way for a Secured credential), use `from oddjob import fetch_via` and `fetch_via(%q, url, ...)` and add \"fetch_via:%s\" to hook_capabilities. Both return {status, status_line, body}, and either way the key is attached server-side and the script never sees it", n, c[1], c[1], c[1])
		}
		exports := make([]string, 0, len(known))
		for k := range known {
			if !strings.HasPrefix(k, "_") {
				exports = append(exports, k)
			}
		}
		sort.Strings(exports)
		return fmt.Sprintf("script_body takes %q from the oddjob module, which has no such name: it exports only %s. Nothing else is injected into a script", n, strings.Join(exports, ", "))
	}
	return ""
}

var defaultAPIRe = regexp.MustCompile(`\bdefault_api\b(?:\.(\w+))?`)

// callsOwnTools reports a command or script that reaches for the author's
// own tools through `default_api`, the name some models give their function
// namespace; "" when it does not. Observed: Builder wrote a shell tool that
// ran print(default_api.bulletins(...)) to let an agent post to a board, then
// `from default_api import bulletins` in Python, and spent the turn on the
// syntax and import errors. No such module exists in a tool, and a tool
// cannot call an agent's tools at all.
func callsOwnTools(src string) string {
	m := defaultAPIRe.FindStringSubmatch(src)
	if m == nil {
		return ""
	}
	what := "your own tools"
	if m[1] != "" {
		what = fmt.Sprintf("your %s tool", m[1])
	}
	return fmt.Sprintf("this calls %s through `default_api`, which does not exist inside a tool: default_api is only how your tools are named to you, and a tool's command or script cannot call any of them. Nothing to build here: if an agent needs what one of your tools gives, grant it to that agent with the tool that manages it (an agent posts to a bulletin board once bulletins allow_poster lets it, which gives it post_bulletin; an agent uses a catalog tool once it is in its allowed tools). A script itself has only the oddjob module (fetch_url, fetch_via, ...) for reaching outside", what)
}

// NormalizeScriptBody undoes a script that arrived escaped a second time: the
// whole program on one line, its line breaks written as the two characters
// backslash and n. On disk that is a one-line file whose first line is the
// shebang, so the kernel reads the entire script as the interpreter line.
// Observed: such a tool re-executed itself until the 5-minute kill, printed
// nothing and never reached its API, and the syntax check had passed it
// because Python reads a file that starts with "#" as a single comment.
//
// Only a body with no real line break at all, three or more escaped ones, and
// the opening of a script (a shebang or a Python import) is touched, and it
// is decoded in one pass, so an escape the script meant to keep (a "\\n"
// inside a string) comes back as the "\n" it was. Reports whether it changed.
func NormalizeScriptBody(body string) (string, bool) {
	if !strings.Contains(body, `\n`) || strings.ContainsAny(body, "\n\r") {
		return body, false
	}
	t := strings.TrimSpace(body)
	if strings.Count(t, `\n`) < 3 || !(strings.HasPrefix(t, "#!") || strings.HasPrefix(t, "import ") || strings.HasPrefix(t, "from ")) {
		return body, false
	}
	var b strings.Builder
	for i := 0; i < len(t); i++ {
		c := t[i]
		if c != '\\' || i+1 == len(t) {
			b.WriteByte(c)
			continue
		}
		switch n := t[i+1]; n {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '\\', '\'', '"':
			b.WriteByte(n)
		default:
			b.WriteByte(c)
			b.WriteByte(n)
		}
		i++
	}
	return b.String() + "\n", true
}
