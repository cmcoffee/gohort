package media

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// acceptAttr matches a file input's accept list in either HTML attribute form
// or JS property form, in single or double quotes.
var acceptAttr = regexp.MustCompile(`accept\s*=\s*['"]([^'"]*)['"]`)

// requiredDocumentExtensions are the extensions ExtractDocumentText handles
// that a document picker must therefore offer.
//
// Kept as a list rather than parsed out of DocumentAcceptAttr so that widening
// the constant does not silently widen the test along with it — adding a format
// should be a deliberate two-line change, not something the test agrees to
// retroactively.
var requiredDocumentExtensions = []string{
	".pdf", ".docx", ".txt", ".md", ".log", ".csv", ".json", ".yaml", ".yml", ".html",
}

// TestDocumentPickersCoverTheExtractor sweeps the source tree for file pickers
// and requires every DOCUMENT picker to offer everything the extractor accepts.
//
// A source sweep rather than a unit test because the defect is duplication:
// this list has been written out by hand in three separate files and drifted in
// two of them, each time surfacing as "the upload won't let me pick a .json"
// while the server side handled .json fine. Nothing inside any one of those
// files can detect that it has fallen behind — only a test that looks at all of
// them at once can.
//
// A picker is judged to be a DOCUMENT picker if it offers .pdf. That is the
// marker because no other picker in the tree wants PDFs: image pickers, audio
// pickers and the evidence-bundle picker (.tar.gz, .log) all name their own
// formats and are correctly left alone.
func TestDocumentPickersCoverTheExtractor(t *testing.T) {
	root := repoRoot(t)
	// Directories holding UI source. core/ui is included because a generic
	// upload primitive there would be subject to the same rule.
	roots := []string{"apps", "core/ui", "private"}

	checked := 0
	for _, dir := range roots {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); err != nil {
			continue // private/ is a symlink that need not be present
		}
		err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil //nolint // an unreadable path is not this test's business
			}
			switch filepath.Ext(path) {
			case ".go", ".html", ".js":
			default:
				return nil
			}
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			for _, m := range acceptAttr.FindAllStringSubmatch(string(data), -1) {
				list := m[1]
				if !strings.Contains(list, ".pdf") {
					continue // not a document picker
				}
				checked++
				rel, _ := filepath.Rel(root, path)
				for _, ext := range requiredDocumentExtensions {
					if !strings.Contains(list, ext+",") && !strings.HasSuffix(list, ext) {
						t.Errorf("%s: a document picker does not offer %s, but "+
							"ExtractDocumentText accepts it — the file looks unsupported to the user "+
							"and there is no way for them to tell which end refused it.\n  accept = %s\n"+
							"  fix: use media.DocumentAcceptAttr", rel, ext, list)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", dir, err)
		}
	}
	// A sweep that finds nothing passes vacuously and would keep passing after
	// someone renamed the attribute or moved the assets.
	if checked == 0 {
		t.Fatal("found no document pickers to check — the sweep is no longer looking where they live")
	}
}

// TestDocumentAcceptAttrCoversTheExtractor holds the constant itself to the
// same rule the pickers are held to.
func TestDocumentAcceptAttrCoversTheExtractor(t *testing.T) {
	for _, ext := range requiredDocumentExtensions {
		if !strings.Contains(DocumentAcceptAttr, ext+",") && !strings.HasSuffix(DocumentAcceptAttr, ext) {
			t.Errorf("DocumentAcceptAttr omits %s", ext)
		}
	}
}

// repoRoot walks up from the test's directory until it finds go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not locate the repository root")
	return ""
}
