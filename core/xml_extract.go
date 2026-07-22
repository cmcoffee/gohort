package core

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// ExtractSpec is a declarative, namespace-AGNOSTIC recipe for pulling structured
// JSON out of an XML document. It exists because small models cannot reliably
// hand-write XML parsing: across the CalDAV work they failed the same extraction
// in Python ElementTree, xmllint+xpath, and grep+sed+awk, every time tripping on
// WebDAV namespaces and repeating-record traversal. This spec lets them declare
// WHAT to pull, never HOW.
//
// ALL element/attribute matching is by LOCAL name — prefix and namespace URI are
// ignored. <response>, <D:response>, and <ns0:response xmlns:ns0="DAV:"> all
// match "response". That single rule is what defeated every xpath attempt.
type ExtractSpec struct {
	// Select is the local name of the repeating record element; one output
	// object is emitted per match, in document order, at any depth. Empty →
	// the whole document is one record and the output is a single object.
	Select string `json:"select,omitempty"`
	// Where, if set, keeps only records that pass the filter.
	Where *ExtractWhere `json:"where,omitempty"`
	// Fields maps output key → selector (see resolveSelector). Required.
	Fields map[string]string `json:"fields"`
}

// ExtractWhere is a single-clause record filter. Exactly one field is honored,
// checked in the order has, missing, equals, contains.
type ExtractWhere struct {
	Has      string        `json:"has,omitempty"`      // record has a descendant with this local name
	Missing  string        `json:"missing,omitempty"`  // record has NO such descendant
	Equals   *ExtractMatch `json:"equals,omitempty"`   // selector's text == value
	Contains *ExtractMatch `json:"contains,omitempty"` // selector's text contains value
}

// ExtractMatch is the {field, value} pair for equals/contains where-clauses.
type ExtractMatch struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

// maxXMLExtractBytes bounds the input so a runaway response can't blow up memory
// while building the node tree. Matches the api dispatch response cap ballpark.
const maxXMLExtractBytes = 8 << 20 // 8MB

// ParseExtractSpec normalizes an LLM-supplied response_extract object (a loose
// map) into a *ExtractSpec, or nil when absent/empty. Round-trips through JSON
// so nested where/fields shapes are handled uniformly.
func ParseExtractSpec(v any) *ExtractSpec {
	if v == nil {
		return nil
	}
	if s, ok := v.(*ExtractSpec); ok {
		return s
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var spec ExtractSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil
	}
	if len(spec.Fields) == 0 {
		return nil
	}
	return &spec
}

type xmlNode struct {
	local    string
	attrs    map[string]string
	children []*xmlNode
	chardata string // direct char data on this element only
}

// ExtractXML applies spec to xml and returns JSON — an array of objects when
// spec.Select is set (possibly empty []), or a single object when it isn't.
// Namespaces are ignored (local-name matching). A parse error names the problem;
// no partial output is emitted.
func ExtractXML(data []byte, spec ExtractSpec) ([]byte, error) {
	if len(spec.Fields) == 0 {
		return nil, fmt.Errorf("extract spec needs at least one field")
	}
	if len(data) > maxXMLExtractBytes {
		data = data[:maxXMLExtractBytes]
	}
	root, err := parseXMLTree(data)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return nil, fmt.Errorf("no XML element found in the document")
	}

	if strings.TrimSpace(spec.Select) == "" {
		// Whole document is one record → single object.
		obj := extractFields(root, spec.Fields)
		return json.Marshal(obj)
	}

	records := make([]*xmlNode, 0, 8)
	collectByLocal(root, strings.TrimSpace(spec.Select), &records)
	out := make([]map[string]string, 0, len(records))
	for _, rec := range records {
		if !whereMatch(rec, spec.Where) {
			continue
		}
		out = append(out, extractFields(rec, spec.Fields))
	}
	return json.Marshal(out)
}

// parseXMLTree builds a local-name-only tree from the document. Lenient
// (dec.Strict = false) so quirky-but-real server XML still parses, and a
// pass-through CharsetReader so a declared non-utf8 charset doesn't hard-fail.
func parseXMLTree(data []byte) (*xmlNode, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	dec.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }
	shadow := &xmlNode{local: "#doc", attrs: map[string]string{}}
	stack := []*xmlNode{shadow}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("invalid XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &xmlNode{local: t.Name.Local, attrs: make(map[string]string, len(t.Attr))}
			for _, a := range t.Attr {
				n.attrs[a.Name.Local] = a.Value
			}
			parent := stack[len(stack)-1]
			parent.children = append(parent.children, n)
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			stack[len(stack)-1].chardata += string(t)
		}
	}
	// First real element under the shadow root is the document root.
	if len(shadow.children) == 0 {
		return nil, nil
	}
	return shadow.children[0], nil
}

// collectByLocal appends every descendant of n (not n itself) whose local name
// matches, in document order.
func collectByLocal(n *xmlNode, local string, out *[]*xmlNode) {
	for _, c := range n.children {
		if c.local == local {
			*out = append(*out, c)
		}
		collectByLocal(c, local, out)
	}
}

// firstDescendant returns the first descendant of n (not n itself) with the
// given local name, in document order, or nil.
func firstDescendant(n *xmlNode, local string) *xmlNode {
	for _, c := range n.children {
		if c.local == local {
			return c
		}
		if got := firstDescendant(c, local); got != nil {
			return got
		}
	}
	return nil
}

// firstChild returns the first direct child of n with the given local name.
func firstChild(n *xmlNode, local string) *xmlNode {
	for _, c := range n.children {
		if c.local == local {
			return c
		}
	}
	return nil
}

// nodeText is the trimmed concatenation of all char data at and below n, so a
// leaf like <displayname>Home</displayname> yields "Home" and a wrapper yields
// its combined descendant text.
func nodeText(n *xmlNode) string {
	var b strings.Builder
	var walk func(*xmlNode)
	walk = func(x *xmlNode) {
		b.WriteString(x.chardata)
		for _, c := range x.children {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(b.String())
}

// resolveSelector evaluates a selector against a record node. Grammar:
//
//	"name"        text of the first descendant <name>
//	"a/b/c"       child-path by local names (first child at each step), text of last
//	"@attr"       attribute attr on the record element
//	"name/@attr"  attribute attr of the first descendant <name>
//
// Returns ("", false) when nothing matches.
func resolveSelector(rec *xmlNode, selector string) (string, bool) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return "", false
	}
	// @attr on the record itself.
	if strings.HasPrefix(selector, "@") {
		v, ok := rec.attrs[selector[1:]]
		return v, ok
	}
	// name/@attr — attribute of a descendant.
	if i := strings.Index(selector, "/@"); i >= 0 {
		name, attr := selector[:i], selector[i+2:]
		d := firstDescendant(rec, name)
		if d == nil {
			return "", false
		}
		v, ok := d.attrs[attr]
		return v, ok
	}
	// Child-path a/b/c.
	if strings.Contains(selector, "/") {
		cur := rec
		for _, step := range strings.Split(selector, "/") {
			if step == "" {
				continue
			}
			cur = firstChild(cur, step)
			if cur == nil {
				return "", false
			}
		}
		return nodeText(cur), true
	}
	// Bare descendant name.
	d := firstDescendant(rec, selector)
	if d == nil {
		return "", false
	}
	return nodeText(d), true
}

// extractFields resolves every field selector against the record. A selector
// that resolves to nothing is omitted from the object (not null).
func extractFields(rec *xmlNode, fields map[string]string) map[string]string {
	obj := make(map[string]string, len(fields))
	for key, sel := range fields {
		if v, ok := resolveSelector(rec, sel); ok {
			obj[key] = v
		}
	}
	return obj
}

// whereMatch reports whether rec passes the filter (nil filter → always true).
func whereMatch(rec *xmlNode, w *ExtractWhere) bool {
	if w == nil {
		return true
	}
	switch {
	case strings.TrimSpace(w.Has) != "":
		return firstDescendant(rec, strings.TrimSpace(w.Has)) != nil
	case strings.TrimSpace(w.Missing) != "":
		return firstDescendant(rec, strings.TrimSpace(w.Missing)) == nil
	case w.Equals != nil:
		v, _ := resolveSelector(rec, w.Equals.Field)
		return v == w.Equals.Value
	case w.Contains != nil:
		v, _ := resolveSelector(rec, w.Contains.Field)
		return strings.Contains(v, w.Contains.Value)
	}
	return true
}
