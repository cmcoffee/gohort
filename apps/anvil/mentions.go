// @file mentions, as in Claude Code: typing "@" in the message box offers
// the project's files, and picking one puts its path in the message, so the
// assistant is pointed at the right file without it being spelled out.
package anvil

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/core/jsonapi"
)

const (
	mentionLimit   = 20
	mentionMaxScan = 20000
	mentionFresh   = 15 * time.Second
)

// mentionFiles caches each folder's file list briefly: the box asks as the
// person types, and a walk per key would be wasted on a large tree.
var mentionFiles = struct {
	sync.Mutex
	byBase map[string]mentionList
}{byBase: map[string]mentionList{}}

type mentionList struct {
	at    time.Time
	files []string
}

// projectFiles is the project's files, as search_code sees them: .git and
// what the project ignores are left out.
func (c *toolCtx) projectFiles() []string {
	mentionFiles.Lock()
	if l, ok := mentionFiles.byBase[c.base]; ok && time.Since(l.at) < mentionFresh {
		mentionFiles.Unlock()
		return l.files
	}
	mentionFiles.Unlock()
	var files []string
	filepath.Walk(c.base, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, relErr := filepath.Rel(c.base, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if info.IsDir() {
			if path != c.base && (info.Name() == ".git" || c.ignores(rel)) {
				return filepath.SkipDir
			}
			return nil
		}
		if len(files) >= mentionMaxScan {
			return filepath.SkipAll
		}
		if info.Mode().IsRegular() && !c.ignores(rel) {
			files = append(files, rel)
		}
		return nil
	})
	mentionFiles.Lock()
	mentionFiles.byBase[c.base] = mentionList{at: time.Now(), files: files}
	mentionFiles.Unlock()
	return files
}

// mentionRank is how well path answers q, lower first; -1 when it does not.
// A name starting with q beats a name containing it, which beats the path
// containing it, which beats q's letters in order anywhere in the path.
func mentionRank(path, q string) int {
	if q == "" {
		return 0
	}
	p := strings.ToLower(path)
	name := p[strings.LastIndex(p, "/")+1:]
	switch {
	case strings.HasPrefix(name, q):
		return 0
	case strings.Contains(name, q):
		return 1
	case strings.Contains(p, q):
		return 2
	}
	i := 0
	for _, r := range p {
		if i < len(q) && r == rune(q[i]) {
			i++
		}
	}
	if i == len(q) {
		return 3
	}
	return -1
}

// matchingFiles is the best of the project's files for q: by rank, then
// the shallower and shorter path.
func (c *toolCtx) matchingFiles(q string) []string {
	q = strings.ToLower(strings.TrimSpace(q))
	type hit struct {
		path string
		rank int
	}
	var hits []hit
	for _, f := range c.projectFiles() {
		if r := mentionRank(f, q); r >= 0 {
			hits = append(hits, hit{f, r})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		if da, db := strings.Count(a.path, "/"), strings.Count(b.path, "/"); da != db {
			return da < db
		}
		if len(a.path) != len(b.path) {
			return len(a.path) < len(b.path)
		}
		return a.path < b.path
	})
	out := []string{}
	for i := 0; i < len(hits) && i < mentionLimit; i++ {
		out = append(out, hits[i].path)
	}
	return out
}

type mentionOption struct {
	Value string `json:"value"`
}

func (T *Anvil) handleMentions(c *jsonapi.Call) (any, error) {
	q := c.R.URL.Query()
	p, ok := T.findProject(c.UDB, c.User, callerIsAdmin(c.R), strings.TrimSpace(q.Get("project_id")))
	if !ok {
		return []mentionOption{}, nil
	}
	base, err := p.baseDir()
	if err != nil {
		return []mentionOption{}, nil
	}
	ctx := &toolCtx{project: p, base: base}
	out := []mentionOption{}
	for _, f := range ctx.matchingFiles(q.Get("q")) {
		out = append(out, mentionOption{Value: f})
	}
	return out, nil
}
