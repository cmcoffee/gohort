// What a search reads.
//
// The search was written over a bundle's encrypted store, and other stores of
// files want the same search: a repository's, which keeps each file whole. A
// Source is the little a search needs from one: its files, in the order to
// read them, and a file's lines a slice at a time. A bundle's store is one
// Source; a store that keeps files whole is another, each file one slice of
// any height.
package bundle

import "strings"

// Source is a store of files a search can read.
type Source interface {
	// Files is every file, in the order a search reads them. A file's
	// Slices is how many slices it has; Format and the span decide what a
	// time window and records can do with it (FormatText for none).
	Files() []File
	// Slice is slice idx of a file: its lines joined by "\n", "" for none.
	Slice(bf File, idx int) string
	// Height is how many lines a slice holds, the last one of a file
	// excepted: line n of a file is in slice (n-1)/Height. A store that
	// keeps files whole gives each file one slice and a Height no file
	// reaches.
	Height() int
}

// storeSource is a bundle's encrypted store as a Source.
type storeSource struct {
	b     Bundle
	store Store
}

func (s storeSource) Files() []File { return s.b.Index() }
func (s storeSource) Height() int   { return SliceLines }

func (s storeSource) Slice(bf File, idx int) string {
	var body string
	s.store.Get(sliceTable, sliceKey(bf.Key, idx), &body)
	return body
}

// sourceLines is one slice of a file as lines.
func sourceLines(src Source, bf File, idx int) []string {
	body := src.Slice(bf, idx)
	if body == "" {
		return nil
	}
	return strings.Split(body, "\n")
}
