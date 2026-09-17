package media

import (
	"strings"
	"testing"
)

// A nested object becomes one section per top-level key with dotted paths
// underneath, keys in sorted order, numbers as written, a long string as a
// paragraph, and scalar arrays on one line.
func TestJSONObjectBecomesSections(t *testing.T) {
	src := `{"server": {"host": "db1", "port": 5432, "tls": true, "tags": ["prod", "eu"]},
	         "notes": "` + strings.Repeat("word ", 50) + `",
	         "version": 1.50, "empty": []}`
	md, err := JSONToMarkdown([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## empty\n\nempty: []\n",
		"## notes\n\nnotes:\n\nword word",
		"## server\n\nserver.host: db1\nserver.port: 5432\nserver.tags: prod, eu\nserver.tls: true\n",
		"## version\n\nversion: 1.50\n",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("missing %q in:\n%s", want, md)
		}
	}
	if strings.Index(md, "## empty") > strings.Index(md, "## notes") {
		t.Fatalf("sections must be in sorted key order:\n%s", md)
	}
}

// An array of records becomes one section per record, titled by its name,
// title or id when it has one and by position otherwise; nested arrays of
// objects get indexed paths.
func TestJSONRecordsBecomeSections(t *testing.T) {
	src := `[{"name": "alpha", "cpu": 4, "disks": [{"dev": "sda", "gb": 100}, {"dev": "sdb", "gb": 200}]},
	         {"id": 7, "cpu": 8},
	         {"cpu": 2}]`
	md, err := JSONToMarkdown([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## alpha\n\ncpu: 4\ndisks[0].dev: sda\ndisks[0].gb: 100\ndisks[1].dev: sdb\n",
		"## id 7\n\ncpu: 8\n",
		"## Item 3\n\ncpu: 2\n",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("missing %q in:\n%s", want, md)
		}
	}
}

func TestJSONDetectionAndErrors(t *testing.T) {
	if !LooksLikeJSON([]byte("  {\"a\": 1}")) || !LooksLikeJSON([]byte("[1,2]")) {
		t.Fatal("objects and arrays are JSON")
	}
	if LooksLikeJSON([]byte("# heading\n\n{not json")) || LooksLikeJSON([]byte("42")) {
		t.Fatal("markdown and bare scalars are not routed as JSON")
	}
	if _, err := JSONToMarkdown([]byte("{oops")); err == nil {
		t.Fatal("invalid JSON must be an error")
	}
	if md, _ := JSONToMarkdown([]byte(`"just a string"`)); md != "just a string\n" {
		t.Fatalf("a top-level scalar renders as itself, got %q", md)
	}
	// Uploading a .json file goes through the same flattener.
	if md, err := ExtractDocument(nil, DocumentAttachment{Name: "hosts.json", Data: []byte(`{"a": {"b": 1}}`)}); err != nil || !strings.Contains(md, "## a\n\na.b: 1") {
		t.Fatalf("extract must flatten JSON: %q %v", md, err)
	}
}
