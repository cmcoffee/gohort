// Turning an API specification into something retrieval can actually find.
//
// An OpenAPI document ingested as raw JSON retrieves badly, and the reason is
// structural rather than cosmetic. A question is asked in the shape "how do I
// list merge requests" and the answer lives in one operation object — but a
// chunker splitting a JSON blob by size cuts through the middle of objects,
// separates a summary from the path it describes, and buries both in braces. A
// search then matches a fragment that cannot answer anything.
//
// So the spec is decomposed into one markdown section per OPERATION, which is
// the unit a question is actually about. The report chunker splits at "## "
// headings (SplitReportIntoChunks), so one section becomes one chunk with no
// further plumbing.
//
// SELF-CONTAINMENT IS THE WHOLE POINT. A chunk is retrieved alone, with no
// neighbours and no document around it, so every section repeats the method,
// the path and the spec's title. "Returns a list of users" is worthless without
// "GET /users" in the same breath, and that is exactly what naive chunking
// loses.
package media

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// httpMethods are the keys of a path item that denote operations. Anything else
// under a path (parameters, servers, $ref, summary) describes the path rather
// than being callable.
var httpMethods = []string{"get", "post", "put", "patch", "delete", "head", "options", "trace"}

// LooksLikeOpenAPI reports whether the bytes are a JSON OpenAPI/Swagger spec.
//
// Detection is on the marker key rather than the filename: specs arrive as
// openapi.json, swagger.json, api-docs, or a download with no extension at all,
// and the document itself is the only reliable witness.
func LooksLikeOpenAPI(data []byte) bool {
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return false
	}
	if _, ok := doc["openapi"]; ok {
		return true
	}
	if _, ok := doc["swagger"]; ok {
		return true
	}
	return false
}

// OpenAPIToMarkdown renders a spec as one section per operation.
func OpenAPIToMarkdown(data []byte) (string, error) {
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return "", fmt.Errorf("not valid JSON: %w", err)
	}
	title, version := specTitle(doc)

	var b strings.Builder
	fmt.Fprintf(&b, "# %s", title)
	if version != "" {
		fmt.Fprintf(&b, " (%s)", version)
	}
	b.WriteString("\n\n")
	if d := str(doc["info"], "description"); d != "" {
		b.WriteString(d + "\n\n")
	}
	if servers := serverList(doc); len(servers) > 0 {
		b.WriteString("Base URL(s): " + strings.Join(servers, ", ") + "\n\n")
	}

	paths, _ := doc["paths"].(map[string]any)
	if len(paths) == 0 {
		return "", fmt.Errorf("the spec declares no paths")
	}
	// Sorted so re-ingesting the same spec produces the same chunks in the same
	// order — otherwise every upload looks like a wholly new document.
	routes := make([]string, 0, len(paths))
	for p := range paths {
		routes = append(routes, p)
	}
	sort.Strings(routes)

	count := 0
	for _, route := range routes {
		item, ok := paths[route].(map[string]any)
		if !ok {
			continue
		}
		shared := paramList(item["parameters"])
		for _, method := range httpMethods {
			op, ok := item[method].(map[string]any)
			if !ok {
				continue
			}
			b.WriteString(renderOperation(title, strings.ToUpper(method), route, op, shared))
			count++
		}
	}
	if count == 0 {
		return "", fmt.Errorf("the spec declares paths but no operations")
	}
	return b.String(), nil
}

// renderOperation writes one self-contained section.
func renderOperation(specTitle, method, route string, op map[string]any, sharedParams []string) string {
	var b strings.Builder
	summary := str(op, "summary")
	// The heading carries method and path, because it becomes the chunk's
	// label and is often all a ranked result shows.
	fmt.Fprintf(&b, "## %s %s", method, route)
	if summary != "" {
		fmt.Fprintf(&b, " — %s", summary)
	}
	b.WriteString("\n\n")
	// Repeated inside the body too: the heading may be stripped or truncated by
	// whatever renders the hit, and a chunk that cannot say which endpoint it
	// describes is unusable.
	fmt.Fprintf(&b, "Endpoint: `%s %s` in %s.\n", method, route, specTitle)

	if id := str(op, "operationId"); id != "" {
		fmt.Fprintf(&b, "Operation id: `%s`.\n", id)
	}
	if tags := strList(op["tags"]); len(tags) > 0 {
		fmt.Fprintf(&b, "Tags: %s.\n", strings.Join(tags, ", "))
	}
	if d := str(op, "description"); d != "" && d != summary {
		b.WriteString("\n" + d + "\n")
	}
	if dep, _ := op["deprecated"].(bool); dep {
		b.WriteString("\n**Deprecated.**\n")
	}

	params := append(append([]string{}, sharedParams...), paramList(op["parameters"])...)
	if len(params) > 0 {
		b.WriteString("\nParameters:\n")
		for _, p := range params {
			b.WriteString("- " + p + "\n")
		}
	}
	if body := requestBody(op["requestBody"]); body != "" {
		b.WriteString("\nRequest body: " + body + "\n")
	}
	if rs := responseList(op["responses"]); len(rs) > 0 {
		b.WriteString("\nResponses:\n")
		for _, r := range rs {
			b.WriteString("- " + r + "\n")
		}
	}
	b.WriteString("\n")
	return b.String()
}

// paramList renders parameters as one line each.
func paramList(v any) []string {
	arr, _ := v.([]any)
	var out []string
	for _, raw := range arr {
		p, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := str(p, "name")
		if name == "" {
			// A $ref'd parameter. Named rather than dropped: "there is a
			// parameter here we did not resolve" is useful; silence is not.
			if ref := str(p, "$ref"); ref != "" {
				out = append(out, "(see "+refName(ref)+")")
			}
			continue
		}
		line := "`" + name + "`"
		if in := str(p, "in"); in != "" {
			line += " (" + in
			if req, _ := p["required"].(bool); req {
				line += ", required"
			}
			line += ")"
		} else if req, _ := p["required"].(bool); req {
			line += " (required)"
		}
		if t := schemaSummary(p["schema"]); t != "" {
			line += " " + t
		}
		if d := str(p, "description"); d != "" {
			line += " — " + oneLine(d)
		}
		out = append(out, line)
	}
	return out
}

// requestBody renders the body's content types and shape.
func requestBody(v any) string {
	rb, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	content, _ := rb["content"].(map[string]any)
	types := make([]string, 0, len(content))
	for ct := range content {
		types = append(types, ct)
	}
	sort.Strings(types)
	var parts []string
	for _, ct := range types {
		entry, _ := content[ct].(map[string]any)
		if s := schemaSummary(entry["schema"]); s != "" {
			parts = append(parts, ct+" "+s)
			continue
		}
		parts = append(parts, ct)
	}
	out := strings.Join(parts, ", ")
	if req, _ := rb["required"].(bool); req && out != "" {
		out += " (required)"
	}
	return out
}

// responseList renders status codes with their descriptions, numerically
// ordered so 200 precedes 404 rather than sorting as strings.
func responseList(v any) []string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	codes := make([]string, 0, len(m))
	for c := range m {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	var out []string
	for _, c := range codes {
		line := "`" + c + "`"
		if r, ok := m[c].(map[string]any); ok {
			if d := str(r, "description"); d != "" {
				line += " — " + oneLine(d)
			}
			if content, ok := r["content"].(map[string]any); ok {
				for ct, entry := range content {
					if e, ok := entry.(map[string]any); ok {
						if s := schemaSummary(e["schema"]); s != "" {
							line += " (" + ct + " " + s + ")"
							break
						}
					}
					line += " (" + ct + ")"
					break
				}
			}
		}
		out = append(out, line)
	}
	return out
}

// schemaSummary describes a schema in a few words.
//
// Deliberately shallow: resolving $refs would inline whole object graphs and
// balloon every chunk past the point where retrieval works, which is the
// problem this file exists to solve. The ref NAME is what a reader needs to go
// look it up.
func schemaSummary(v any) string {
	s, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	if ref := str(s, "$ref"); ref != "" {
		return refName(ref)
	}
	t := str(s, "type")
	switch t {
	case "array":
		if items, ok := s["items"].(map[string]any); ok {
			if inner := schemaSummary(items); inner != "" {
				return "array of " + inner
			}
		}
		return "array"
	case "object", "":
		if props, ok := s["properties"].(map[string]any); ok && len(props) > 0 {
			names := make([]string, 0, len(props))
			for n := range props {
				names = append(names, n)
			}
			sort.Strings(names)
			if len(names) > 8 {
				return fmt.Sprintf("object {%s, …%d more}", strings.Join(names[:8], ", "), len(names)-8)
			}
			return "object {" + strings.Join(names, ", ") + "}"
		}
		if t == "" {
			return ""
		}
		return "object"
	}
	if enum := strList(s["enum"]); len(enum) > 0 {
		return t + " (one of: " + strings.Join(enum, ", ") + ")"
	}
	if f := str(s, "format"); f != "" {
		return t + "/" + f
	}
	return t
}

// --- small helpers ---

func specTitle(doc map[string]any) (title, version string) {
	title = str(doc["info"], "title")
	if strings.TrimSpace(title) == "" {
		title = "API"
	}
	return title, str(doc["info"], "version")
}

func serverList(doc map[string]any) []string {
	var out []string
	if arr, ok := doc["servers"].([]any); ok {
		for _, raw := range arr {
			if s, ok := raw.(map[string]any); ok {
				if u := str(s, "url"); u != "" {
					out = append(out, u)
				}
			}
		}
	}
	// Swagger 2.0 said it differently.
	if host := str(doc, "host"); host != "" && len(out) == 0 {
		out = append(out, host+str(doc, "basePath"))
	}
	return out
}

// str reads a string field from a map, tolerating a non-map input so callers
// can walk a spec without asserting at every step.
func str(v any, key ...string) string {
	m, ok := v.(map[string]any)
	if !ok || len(key) == 0 {
		return ""
	}
	s, _ := m[key[0]].(string)
	return strings.TrimSpace(s)
}

func strList(v any) []string {
	arr, _ := v.([]any)
	var out []string
	for _, raw := range arr {
		switch t := raw.(type) {
		case string:
			out = append(out, t)
		default:
			out = append(out, fmt.Sprint(t))
		}
	}
	return out
}

// refName reduces "#/components/schemas/User" to "User".
func refName(ref string) string {
	if i := strings.LastIndexByte(ref, '/'); i >= 0 && i+1 < len(ref) {
		return ref[i+1:]
	}
	return ref
}

// oneLine flattens a description so it cannot break the list item it sits in.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	return s
}
