package orchestrate

// Structural reading of an html section's inline JavaScript.
//
// `node --check` answers one question — does this parse — and a document that
// lost half its functions parses perfectly: calling a name nothing defines is
// valid JavaScript right up until it runs. A canvas game makes that gap total,
// because nothing runs until the player clicks, so the browser check on save
// loads a dead page and reports it clean. That is how a rewrite silently wiped
// a working game and every gate said fine.
//
// So: read the source structurally. Mask the literals, then two things fall
// out of the same scan — which names a script DEFINES versus which it only
// CALLS (the dangling-call check), and where a named function starts and ends
// (replace_function, so an author can swap one function without reproducing
// the document byte-for-byte).
//
// Neither is a parser and neither pretends to be. The dangling-call check is
// used as a DIFF — names dangling after an edit minus names dangling before —
// so anything this scanner is wrong about was already wrong a moment ago and
// cancels out. Only a name the edit newly broke can accuse.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// jsMaskLiterals returns a copy of src with the same LENGTH and the same line
// breaks, in which the contents of comments, string literals, template
// literals and regex literals have been replaced by spaces. Offsets in the
// masked copy address the same bytes in the original, so a match found here
// can be sliced out of there.
//
// Masking first is what makes the rest of this file simple: a brace inside a
// string, a `//` inside a URL, and the word `function` inside a comment stop
// existing before anything looks for them.
func jsMaskLiterals(src string) string {
	out := []byte(src)
	n := len(out)
	blank := func(i int) {
		if out[i] != '\n' {
			out[i] = ' '
		}
	}
	// prev is the last significant CODE byte seen, which is how a regex
	// literal is told from a division: `/` after a value divides, `/` after an
	// operator or an opening bracket starts a pattern.
	prev := byte(0)
	prevWord := ""
	for i := 0; i < n; {
		c := out[i]
		switch {
		case c == '/' && i+1 < n && out[i+1] == '/':
			for i < n && out[i] != '\n' {
				blank(i)
				i++
			}
		case c == '/' && i+1 < n && out[i+1] == '*':
			for i < n && !(out[i] == '*' && i+1 < n && out[i+1] == '/') {
				blank(i)
				i++
			}
			for j := 0; j < 2 && i < n; j++ {
				blank(i)
				i++
			}
		case c == '"' || c == '\'':
			quote := c
			i++ // leave the opening quote in place
			for i < n && out[i] != quote && out[i] != '\n' {
				if out[i] == '\\' {
					blank(i)
					i++
					if i < n {
						blank(i)
						i++
					}
					continue
				}
				blank(i)
				i++
			}
			if i < n && out[i] == quote {
				i++
			}
			prev, prevWord = quote, ""
		case c == '`':
			// Template literals are masked whole, interpolations included. Code
			// inside `${…}` is vanishingly rare in an app's game loop and the
			// only cost of ignoring it is a definition this scan doesn't see —
			// which the diff then cancels out anyway.
			i++
			for i < n && out[i] != '`' {
				if out[i] == '\\' {
					blank(i)
					i++
					if i < n {
						blank(i)
						i++
					}
					continue
				}
				blank(i)
				i++
			}
			if i < n {
				i++
			}
			prev, prevWord = '`', ""
		case c == '/' && jsRegexCanStart(prev, prevWord):
			i++
			inClass := false
			for i < n && out[i] != '\n' {
				if out[i] == '\\' {
					blank(i)
					i++
					if i < n {
						blank(i)
						i++
					}
					continue
				}
				if out[i] == '[' {
					inClass = true
				} else if out[i] == ']' {
					inClass = false
				} else if out[i] == '/' && !inClass {
					break
				}
				blank(i)
				i++
			}
			if i < n && out[i] == '/' {
				i++
			}
			prev, prevWord = '/', ""
		default:
			if !isJSSpace(c) {
				prev = c
				if isJSIdentByte(c) {
					j := i
					for j < n && isJSIdentByte(out[j]) {
						j++
					}
					prevWord = string(out[i:j])
					prev = out[j-1]
					i = j
					continue
				}
				prevWord = ""
			}
			i++
		}
	}
	return string(out)
}

// jsRegexCanStart reports whether a `/` at this point begins a regex literal
// rather than a division, judged by what came before it.
func jsRegexCanStart(prev byte, prevWord string) bool {
	switch prevWord {
	case "return", "typeof", "case", "in", "of", "new", "delete", "void", "throw", "do", "else", "yield", "await":
		return true
	}
	if prevWord != "" {
		return false // an identifier or a number — this divides
	}
	switch prev {
	case 0, '(', ',', '=', ':', '[', '!', '&', '|', '?', '{', '}', ';', '+', '-', '*', '%', '^', '~', '<', '>':
		return true
	}
	return false
}

func isJSSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func isJSIdentByte(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

var (
	// A name is DEFINED by any of these. `.` is excluded ahead of the name so
	// `obj.function` style property text can't register.
	jsFuncDeclRE = regexp.MustCompile(`(?:^|[^\w$.])(?:async\s+)?function\s*\*?\s*([A-Za-z_$][\w$]*)\s*\(`)
	jsClassDefRE = regexp.MustCompile(`(?:^|[^\w$.])class\s+([A-Za-z_$][\w$]*)`)
	jsBindingRE  = regexp.MustCompile(`(?:^|[^\w$.])(?:var|let|const)\s+([A-Za-z_$][\w$]*)`)
	// Method shorthand — `{ draw(ctx) {`, `, tick() {`, `get width() {` — which
	// otherwise reads as a call site and gets reported as undefined. This also
	// matches `if (x) {` and friends, harmlessly: keywords are filtered out of
	// the called set before anything is compared.
	jsMethodDefRE = regexp.MustCompile(`(?m)(?:^|[,{;]|\bget\b|\bset\b|\bstatic\b|\basync\b)\s*([A-Za-z_$][\w$]*)\s*\([^()]*\)\s*\{`)
	// Every `name(` that is not a property access (`.name(`) and not optional
	// chaining (`?.name(`).
	jsCallRE = regexp.MustCompile(`(?:^|[^\w$.])([A-Za-z_$][\w$]*)\s*\(`)
	// Any occurrence of an identifier at all, used to decide whether a called
	// name is ALSO referenced somewhere as a plain value.
	jsIdentRE = regexp.MustCompile(`[A-Za-z_$][\w$]*`)
)

// jsKeywords are the words the call regex will happily match (`if (`, `for (`)
// and which are never a function call.
var jsKeywords = map[string]bool{
	"if": true, "for": true, "while": true, "switch": true, "catch": true, "return": true,
	"function": true, "typeof": true, "new": true, "delete": true, "void": true, "in": true,
	"of": true, "do": true, "else": true, "case": true, "throw": true, "with": true,
	"await": true, "yield": true, "instanceof": true, "class": true, "super": true, "this": true,
	"var": true, "let": true, "const": true, "try": true, "finally": true, "break": true, "continue": true,
}

// jsGlobals are the host and language names an app can call without defining.
// Being generous here is deliberate: a name wrongly listed costs a missed
// warning, while a name wrongly OMITTED accuses an author of breaking working
// code. The diff cancels out most of what this list misses anyway.
var jsGlobals = map[string]bool{
	"alert": true, "confirm": true, "prompt": true, "fetch": true, "atob": true, "btoa": true,
	"parseInt": true, "parseFloat": true, "isNaN": true, "isFinite": true, "encodeURIComponent": true,
	"decodeURIComponent": true, "encodeURI": true, "decodeURI": true, "eval": true,
	"setTimeout": true, "setInterval": true, "clearTimeout": true, "clearInterval": true,
	"requestAnimationFrame": true, "cancelAnimationFrame": true, "queueMicrotask": true,
	"structuredClone": true, "getComputedStyle": true, "matchMedia": true, "scrollTo": true,
	"Array": true, "Object": true, "String": true, "Number": true, "Boolean": true, "Symbol": true,
	"Math": true, "JSON": true, "Date": true, "RegExp": true, "Error": true, "TypeError": true,
	"RangeError": true, "Promise": true, "Map": true, "Set": true, "WeakMap": true, "WeakSet": true,
	"Proxy": true, "Reflect": true, "BigInt": true, "Intl": true, "Function": true,
	"Image": true, "Audio": true, "Option": true, "Blob": true, "File": true, "FileReader": true,
	"FormData": true, "Headers": true, "Request": true, "Response": true, "URL": true,
	"URLSearchParams": true, "AbortController": true, "Event": true, "CustomEvent": true,
	"MutationObserver": true, "IntersectionObserver": true, "ResizeObserver": true,
	"AudioContext": true, "webkitAudioContext": true, "OffscreenCanvas": true, "Path2D": true,
	"ImageData": true, "DOMParser": true, "XMLHttpRequest": true, "WebSocket": true, "Worker": true,
	"TextEncoder": true, "TextDecoder": true, "Uint8Array": true, "Uint8ClampedArray": true,
	"Int8Array": true, "Uint16Array": true, "Int16Array": true, "Uint32Array": true,
	"Int32Array": true, "Float32Array": true, "Float64Array": true, "ArrayBuffer": true, "DataView": true,
	"window": true, "document": true, "console": true, "navigator": true, "location": true,
	"localStorage": true, "sessionStorage": true, "history": true, "screen": true, "performance": true,
	"crypto": true, "top": true, "parent": true, "self": true, "globalThis": true,
	// gohort's own page runtime, available to any html section.
	"uiOpenModal": true, "uiRegisterClientAction": true, "uiRegisterBlockRenderer": true,
	"uiRegisterMarkdownExtension": true, "fetchJSON": true,
}

// jsDanglingCalls returns the names an html blob's inline scripts CALL but
// never define — the shape a half-finished rewrite leaves behind, and the one
// thing a JavaScript parser will never object to.
//
// Conservative by construction. A name is only reported when EVERY occurrence
// of it is a call (`name(`): a function passed as a value, stored in an
// object, or named in a parameter list appears bare somewhere, and that is
// enough to consider it accounted for. What survives that filter is a name the
// document mentions exclusively as something to call, and defines nowhere.
func jsDanglingCalls(html string) []string {
	defined := map[string]bool{}
	called := map[string]bool{}
	bareRef := map[string]bool{}

	for _, block := range jsScriptBodies(html) {
		masked := jsMaskLiterals(block)
		for _, re := range []*regexp.Regexp{jsFuncDeclRE, jsClassDefRE, jsBindingRE, jsMethodDefRE} {
			for _, m := range re.FindAllStringSubmatch(masked, -1) {
				defined[m[1]] = true
			}
		}
		callAt := map[int]bool{}
		for _, loc := range jsCallRE.FindAllStringSubmatchIndex(masked, -1) {
			name := masked[loc[2]:loc[3]]
			if jsKeywords[name] {
				continue
			}
			called[name] = true
			callAt[loc[2]] = true
		}
		// Any identifier occurrence that is NOT one of those call sites counts
		// as the name being referenced in its own right.
		for _, loc := range jsIdentRE.FindAllStringIndex(masked, -1) {
			if callAt[loc[0]] {
				continue
			}
			if loc[0] > 0 && (masked[loc[0]-1] == '.' || masked[loc[0]-1] == '$') {
				continue // a property, not the free name
			}
			bareRef[masked[loc[0]:loc[1]]] = true
		}
	}

	var out []string
	for name := range called {
		if defined[name] || bareRef[name] || jsGlobals[name] || jsKeywords[name] {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// jsNewDanglingCalls reports the names an edit BROKE: dangling after, not
// dangling before. Framing the check as a diff is what makes it safe to act
// on — every imprecision in the scanner is present on both sides and cancels,
// so an author is only ever stopped by damage this specific edit did.
func jsNewDanglingCalls(before, after string) []string {
	prior := map[string]bool{}
	for _, n := range jsDanglingCalls(before) {
		prior[n] = true
	}
	var out []string
	for _, n := range jsDanglingCalls(after) {
		if !prior[n] {
			out = append(out, n)
		}
	}
	return out
}

// jsDefinedFunctions lists the function names an html blob defines, in source
// order, for error messages that would otherwise leave an author guessing.
func jsDefinedFunctions(html string) []string {
	var out []string
	seen := map[string]bool{}
	for _, block := range jsScriptBodies(html) {
		masked := jsMaskLiterals(block)
		for _, m := range jsFuncDeclRE.FindAllStringSubmatch(masked, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				out = append(out, m[1])
			}
		}
		for _, m := range jsBindingRE.FindAllStringSubmatchIndex(masked, -1) {
			name := masked[m[2]:m[3]]
			if seen[name] || !jsBindsFunction(masked, m[3]) {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// jsBindsFunction reports whether the binding whose name ends at idx is being
// assigned a function or an arrow, as opposed to an ordinary value.
func jsBindsFunction(masked string, idx int) bool {
	rest := strings.TrimLeft(masked[idx:], " \t\r\n")
	if !strings.HasPrefix(rest, "=") || strings.HasPrefix(rest, "=>") {
		return false
	}
	rest = strings.TrimLeft(rest[1:], " \t\r\n")
	rest = strings.TrimPrefix(rest, "async")
	rest = strings.TrimLeft(rest, " \t\r\n")
	if strings.HasPrefix(rest, "function") {
		return true
	}
	// An arrow: `(a, b) => …` or `a => …`.
	if strings.HasPrefix(rest, "(") {
		if end := jsMatchPair(rest, 0, '(', ')'); end > 0 {
			return strings.HasPrefix(strings.TrimLeft(rest[end+1:], " \t\r\n"), "=>")
		}
		return false
	}
	if m := jsIdentRE.FindStringIndex(rest); m != nil && m[0] == 0 {
		return strings.HasPrefix(strings.TrimLeft(rest[m[1]:], " \t\r\n"), "=>")
	}
	return false
}

// jsScriptBodies pulls the inline JavaScript out of an html blob, skipping
// external and non-JavaScript blocks. Shares scriptBlockRE with the syntax
// check so both see exactly the same code.
func jsScriptBodies(html string) []string {
	var out []string
	for _, m := range scriptBlockRE.FindAllStringSubmatch(html, -1) {
		attrs, body := m[1], m[2]
		if strings.TrimSpace(body) == "" || strings.Contains(strings.ToLower(attrs), " src=") {
			continue
		}
		if t := scriptTypeRE.FindStringSubmatch(attrs); t != nil {
			switch strings.ToLower(t[1]) {
			case "", "text/javascript", "application/javascript", "module":
			default:
				continue
			}
		}
		out = append(out, body)
	}
	// A bare fragment of JavaScript — a replacement being checked before it is
	// spliced in, or a section whose script tag the regex couldn't see — is
	// still JavaScript. Only when it doesn't open as markup: html with no
	// script at all must scan as nothing, not as code.
	if len(out) == 0 {
		trimmed := strings.TrimSpace(html)
		if trimmed != "" && !strings.HasPrefix(trimmed, "<") && !strings.Contains(strings.ToLower(trimmed), "<script") {
			return []string{html}
		}
	}
	return out
}

// jsMatchPair returns the index of the delimiter closing the one at i, or -1.
// The input must already be masked, so every delimiter it sees is code.
func jsMatchPair(masked string, i int, open, close byte) int {
	if i >= len(masked) || masked[i] != open {
		return -1
	}
	depth := 0
	for ; i < len(masked); i++ {
		switch masked[i] {
		case open:
			depth++
		case close:
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return -1
}

// jsFunctionSpan locates the whole definition of a named function in src and
// returns the byte range covering it — declaration keyword through closing
// brace, plus a trailing semicolon when the form has one.
//
// Recognizes the three shapes an app actually uses: `function name(…) {…}`,
// `const name = function (…) {…}` / `const name = (…) => {…}`, and a bare
// `name = function (…) {…}` assignment. Ambiguity is refused, never guessed:
// two definitions of the same name means an edit cannot say which one it
// changed, which is the same rule patch_html's uniqueness check enforces.
func jsFunctionSpan(src, name string) (start, end int, err error) {
	masked := jsMaskLiterals(src)
	quoted := regexp.QuoteMeta(name)
	// Each form's capture group ends where the SIGNATURE begins, so
	// jsFunctionBodyEnd picks up from one consistent place regardless of which
	// shape matched.
	forms := []*regexp.Regexp{
		regexp.MustCompile(`(?:^|[^\w$.])((?:async\s+)?function\s*\*?\s*` + quoted + `)\s*\(`),
		regexp.MustCompile(`(?:^|[^\w$.])((?:var|let|const)\s+` + quoted + `\s*=)\s*(?:async\s+)?(?:function\b|\(|[A-Za-z_$][\w$]*\s*=>)`),
		regexp.MustCompile(`(?:^|[^\w$.])(` + quoted + `\s*=)\s*(?:async\s+)?(?:function\b|\([^)]*\)\s*=>|[A-Za-z_$][\w$]*\s*=>)`),
	}
	type hit struct{ start, bodyFrom int }
	var hits []hit
	for _, re := range forms {
		for _, loc := range re.FindAllStringSubmatchIndex(masked, -1) {
			hits = append(hits, hit{start: loc[2], bodyFrom: loc[3]})
		}
		if len(hits) > 0 {
			break // the most specific form that matched wins
		}
	}
	switch {
	case len(hits) == 0:
		defined := jsDefinedFunctions(src)
		if len(defined) == 0 {
			return 0, 0, fmt.Errorf("no function named %q in this html section, and no named functions were found at all — read the current html with app_def(action=\"get\") before editing", name)
		}
		return 0, 0, fmt.Errorf("no function named %q in this html section. It defines: %s", name, strings.Join(defined, ", "))
	case len(hits) > 1:
		return 0, 0, fmt.Errorf("%q is defined %d times in this html section — a replacement has to identify ONE of them, so use patch_html with enough surrounding text to be unique", name, len(hits))
	}

	h := hits[0]
	end, err = jsFunctionBodyEnd(masked, h.bodyFrom)
	if err != nil {
		return 0, 0, fmt.Errorf("found %q but could not find where it ends (%v) — use patch_html for this one", name, err)
	}
	// Swallow a trailing semicolon so replacing an expression form doesn't
	// leave a stray `;` behind.
	if end < len(masked) && masked[end] == ';' {
		end++
	}
	return h.start, end, nil
}

// jsFunctionBodyEnd walks from just past a function's name to the byte after
// its closing brace. from points at the `(` of a declaration's parameter list
// or at the `=` of a binding.
func jsFunctionBodyEnd(masked string, from int) (int, error) {
	i := from
	skipSpace := func() {
		for i < len(masked) && isJSSpace(masked[i]) {
			i++
		}
	}
	if i < len(masked) && masked[i] == '=' && (i+1 >= len(masked) || masked[i+1] != '>') {
		i++ // step over the binding's `=`
	}
	skipSpace()
	if strings.HasPrefix(masked[i:], "async") {
		i += len("async")
		skipSpace()
	}
	if strings.HasPrefix(masked[i:], "function") {
		i += len("function")
		skipSpace()
		for i < len(masked) && (masked[i] == '*' || isJSIdentByte(masked[i])) {
			i++ // an expression form may still carry a name
		}
		skipSpace()
	}
	// Parameters: either a parenthesized list or a single bare identifier.
	if i < len(masked) && masked[i] == '(' {
		close := jsMatchPair(masked, i, '(', ')')
		if close < 0 {
			return 0, fmt.Errorf("unbalanced parameter list")
		}
		i = close + 1
	} else {
		for i < len(masked) && isJSIdentByte(masked[i]) {
			i++
		}
	}
	skipSpace()
	if strings.HasPrefix(masked[i:], "=>") {
		i += 2
		skipSpace()
	}
	if i < len(masked) && masked[i] == '{' {
		close := jsMatchPair(masked, i, '{', '}')
		if close < 0 {
			return 0, fmt.Errorf("unbalanced braces")
		}
		return close + 1, nil
	}
	// A concise arrow body: run to the end of the statement at depth zero.
	depth := 0
	for ; i < len(masked); i++ {
		switch masked[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth--; depth < 0 {
				return i, nil
			}
		case ';':
			if depth == 0 {
				return i, nil
			}
		case '\n':
			if depth == 0 {
				return i, nil
			}
		}
	}
	return len(masked), nil
}
