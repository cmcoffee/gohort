// Version-control folders are never read.
//
// A checkout's .git/config holds the URL it was cloned from, and a clone made
// with a token keeps the token in that URL; .git/logs repeats it, and an
// .hg/hgrc or a .svn folder can hold the same. A tree of files that happens
// to be a checkout (a repository, a file store pointed at a working copy, an
// uploaded bundle with one inside) would put that credential within reach of
// every search and read over it. So every reader of such a tree leaves these
// folders out: the repo ingest, a file store's listing and reads, and a
// bundle's ingest.
package bundle

// vcsDirs are the folders version control keeps its metadata in.
var vcsDirs = map[string]bool{".git": true, ".hg": true, ".svn": true}

// VCSDir reports whether a folder name is version-control metadata, never to
// be read.
func VCSDir(name string) bool { return vcsDirs[name] }
