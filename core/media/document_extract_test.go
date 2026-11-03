package media

import "testing"

// A document format stored as a zip archive is named by its extension, and
// nothing else is: the sniffer's answer stands for any other type or name.
func TestZipDocumentType(t *testing.T) {
	cases := []struct{ sniffed, name, want string }{
		{"application/zip", "comparison.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		{"application/zip", "Budget.XLSX", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
		{"application/zip", "deck.pptx", "application/vnd.openxmlformats-officedocument.presentationml.presentation"},
		{"application/zip", "book.epub", "application/epub+zip"},
		{"application/zip", "photos.zip", "application/zip"},                     // a real archive stays one
		{"application/zip", "page.html", "application/zip"},                      // never a type shown inline
		{"text/plain; charset=utf-8", "notes.docx", "text/plain; charset=utf-8"}, // text named .docx is text
	}
	for _, c := range cases {
		if got := ZipDocumentType(c.sniffed, c.name); got != c.want {
			t.Errorf("ZipDocumentType(%q, %q) = %q, want %q", c.sniffed, c.name, got, c.want)
		}
	}
}
