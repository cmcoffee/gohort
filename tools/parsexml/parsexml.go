// parse_xml — a pure, always-on utility that turns XML into JSON via a
// declarative, namespace-agnostic spec. It exists because small models cannot
// hand-write XML parsing: across the CalDAV work they failed the same
// extraction in Python ElementTree, xmllint+xpath, and grep+sed+awk, every time
// tripping on WebDAV namespaces. This is the standalone sibling of an api tool's
// response_extract — use it for XML the agent already HOLDS (a shell tool's
// output, a pasted document) rather than an XML HTTP response.
package parsexml

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func init() { RegisterChatTool(new(ParseXMLTool)) }

// ParseXMLTool wraps core.ExtractXML as an LLM-callable tool.
type ParseXMLTool struct{}

func (t *ParseXMLTool) Name() string { return "parse_xml" }

func (t *ParseXMLTool) Desc() string {
	return "Extract JSON from XML DECLARATIVELY — no hand-written parsing (ElementTree/xpath/regex are unreliable, especially with namespaces). Pass the XML plus a spec. ALL element/attribute matching is by LOCAL name, so namespaces/prefixes are IGNORED: <response>, <D:response>, and <ns0:response> all match \"response\". Use for XML you already hold (a script's output, a pasted doc); for an XML HTTP response, prefer an api tool's response_extract instead. Empty result is [] (not an error)."
}

// Caps: nil — pure transform, no side effects (matches calculate).
func (t *ParseXMLTool) Caps() []Capability { return nil }

func (t *ParseXMLTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"xml": {Type: "string", Description: "The XML content to parse."},
		"spec": {Type: "object", Description: "Extraction spec. Shape: {\"select\":\"<local element name of the repeating record>\", \"where\":{...optional...}, \"fields\":{\"<out_key>\":\"<selector>\"}}. Selectors: \"name\"=text of first descendant <name>; \"a/b\"=child path; \"@attr\"=attribute on the record; \"name/@attr\". where (one of): {\"has\":\"name\"} keep records containing a descendant <name>; {\"missing\":\"name\"}; {\"equals\":{\"field\":\"<sel>\",\"value\":\"x\"}}; {\"contains\":{...}}. Omit select to treat the whole document as one object. Example: {\"select\":\"response\",\"where\":{\"has\":\"calendar\"},\"fields\":{\"path\":\"href\",\"displayname\":\"displayname\"}}."},
	}
}

func (t *ParseXMLTool) Run(args map[string]any) (string, error) {
	xmlStr, _ := args["xml"].(string)
	if strings.TrimSpace(xmlStr) == "" {
		return "", fmt.Errorf("xml is required (the XML content to parse)")
	}
	spec := ParseExtractSpec(args["spec"])
	if spec == nil {
		return "", fmt.Errorf("spec is required — an object with at least a fields map, e.g. {\"select\":\"response\",\"fields\":{\"path\":\"href\"}}")
	}
	out, err := ExtractXML([]byte(xmlStr), *spec)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
