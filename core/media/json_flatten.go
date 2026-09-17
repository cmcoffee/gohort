package media

// A JSON document is decomposed for retrieval rather than ingested raw.
//
// Raw JSON embeds and chunks badly: there are no headings for the chunker to
// cut at, so the size cap cuts through objects, and the vector of a
// brace-heavy blob is close to noise. Flattened, each top-level key (or each
// record of a top-level array) is a section the chunker lands one chunk on,
// and every nested field is one "path: value" line, so keyword search hits
// field names and values directly and a long string value still reads as the
// prose it is. Deterministic — keys are sorted, numbers are kept as written —
// so re-ingesting the same document replaces it with the same chunks.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// LooksLikeJSON reports whether data is a JSON object or array — enough to
// route pasted text through JSONToMarkdown without an extension to go on.
func LooksLikeJSON(data []byte) bool {
	t := bytes.TrimSpace(data)
	if len(t) == 0 || (t[0] != '{' && t[0] != '[') {
		return false
	}
	return json.Valid(t)
}

// jsonProseChars is the length past which a string value is rendered as a
// paragraph under its path rather than on the path's line.
const jsonProseChars = 160

// jsonLabelKeys are tried, in order, to title a record of a top-level array.
var jsonLabelKeys = []string{"name", "title", "id", "key", "slug", "label"}

// JSONToMarkdown renders a JSON document as the markdown the report chunker
// expects: a "## " section per top-level key, or per record of a top-level
// array (titled by its name/title/id when it has one, else by position), with
// nested fields as dotted "path: value" lines. A top-level scalar renders as
// itself. Invalid JSON is an error.
func JSONToMarkdown(data []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", fmt.Errorf("not valid JSON: %w", err)
	}
	var b strings.Builder
	switch t := v.(type) {
	case map[string]any:
		// Paths are absolute under a key's section, so a chunk cut from the
		// middle of a large section still names the full field it holds.
		for _, k := range sortedKeys(t) {
			fmt.Fprintf(&b, "## %s\n\n", k)
			renderJSON(&b, k, t[k])
			b.WriteString("\n")
		}
	case []any:
		for i, el := range t {
			fmt.Fprintf(&b, "## %s\n\n", jsonRecordTitle(el, i))
			renderJSON(&b, "", el)
			b.WriteString("\n")
		}
	default:
		b.WriteString(jsonScalar(v))
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String()) + "\n", nil
}

// renderJSON writes v under prefix: scalars as one line, objects by
// recursing with a dotted path, arrays of scalars as a comma list, arrays of
// objects as indexed paths.
func renderJSON(b *strings.Builder, prefix string, v any) {
	switch t := v.(type) {
	case map[string]any:
		for _, k := range sortedKeys(t) {
			renderJSON(b, joinPath(prefix, k), t[k])
		}
	case []any:
		if len(t) == 0 {
			fmt.Fprintf(b, "%s: []\n", pathOrValue(prefix))
			return
		}
		if allScalars(t) {
			parts := make([]string, 0, len(t))
			for _, el := range t {
				parts = append(parts, jsonScalar(el))
			}
			fmt.Fprintf(b, "%s: %s\n", pathOrValue(prefix), strings.Join(parts, ", "))
			return
		}
		for i, el := range t {
			renderJSON(b, fmt.Sprintf("%s[%d]", prefix, i), el)
		}
	case string:
		if len(t) > jsonProseChars || strings.Contains(t, "\n") {
			fmt.Fprintf(b, "%s:\n\n%s\n\n", pathOrValue(prefix), strings.TrimSpace(t))
			return
		}
		fmt.Fprintf(b, "%s: %s\n", pathOrValue(prefix), t)
	default:
		fmt.Fprintf(b, "%s: %s\n", pathOrValue(prefix), jsonScalar(v))
	}
}

// jsonRecordTitle names a record of a top-level array by the first label key
// it carries as a string or number, else by position.
func jsonRecordTitle(el any, i int) string {
	if m, ok := el.(map[string]any); ok {
		for _, k := range jsonLabelKeys {
			switch lv := m[k].(type) {
			case string:
				if s := strings.TrimSpace(lv); s != "" {
					return s
				}
			case json.Number:
				return k + " " + lv.String()
			}
		}
	}
	return fmt.Sprintf("Item %d", i+1)
}

func jsonScalar(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		if t {
			return "true"
		}
		return "false"
	}
	return fmt.Sprint(v)
}

func allScalars(a []any) bool {
	for _, el := range a {
		switch el.(type) {
		case map[string]any, []any:
			return false
		}
	}
	return true
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func joinPath(prefix, k string) string {
	if prefix == "" {
		return k
	}
	return prefix + "." + k
}

// pathOrValue is the label for a line: the field path, or "value" for a
// scalar record of a top-level array, which has no path of its own.
func pathOrValue(prefix string) string {
	if prefix == "" {
		return "value"
	}
	return prefix
}
