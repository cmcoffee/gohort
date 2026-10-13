package orchestrate

// Every tool is filed into a category on its own (core/toolcat): the owner's
// own label always wins, and a tool without one is filed by the worker among
// the categories already in use. This is the filer: registered as
// toolcat.Filer at start-up, kicked when tools are saved or listed, and run
// once for everyone at start-up so the tools that predate it get filed too.

import (
	"context"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/toolcat"
)

// filingUsers is the owners whose tools are being filed now: one pass each.
var filingUsers = struct {
	sync.Mutex
	m map[string]bool
}{m: map[string]bool{}}

// fileUserTools files user's own tools that have no category yet, among the
// categories their tools are already in.
func (T *OrchestrateApp) fileUserTools(user string) {
	user = strings.TrimSpace(user)
	if T == nil || user == "" || RootDB == nil {
		return
	}
	filingUsers.Lock()
	if filingUsers.m[user] {
		filingUsers.Unlock()
		return
	}
	filingUsers.m[user] = true
	filingUsers.Unlock()
	defer func() {
		filingUsers.Lock()
		delete(filingUsers.m, user)
		filingUsers.Unlock()
	}()
	var all, unfiled []TempTool
	for _, p := range LoadPersistentTempTools(RootDB, user) {
		all = append(all, p.Tool)
		if c, _ := toolcat.Of(user, p.Tool); c == "" {
			unfiled = append(unfiled, p.Tool)
		}
	}
	if len(unfiled) == 0 {
		return
	}
	existing := toolcat.InUse(user, all)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	filed := 0
	for i := range unfiled {
		if ctx.Err() != nil {
			break
		}
		lt := unfiled[i]
		cat, err := fileToolCategory(ctx, T, &lt, existing, toolcat.Rejected(user, lt))
		if err != nil {
			Debug("[orchestrate.tools] could not file %q in a category: %v", lt.Name, err)
			continue
		}
		toolcat.SetAuto(user, lt, cat)
		filed++
		if !containsFold(existing, cat) {
			existing = append(existing, cat)
		}
	}
	Log("[orchestrate.tools] filed %d of %d uncategorized tool(s) for %s into categories (their own labels always win)", filed, len(unfiled), user)
}

// startToolFiling registers the filer and files everybody's uncategorized
// tools once, in the background.
func (T *OrchestrateApp) startToolFiling() {
	toolcat.Filer = T.fileUserTools
	go func() {
		defer func() {
			if r := recover(); r != nil {
				Log("[orchestrate.tools] category filing pass panicked: %v", r)
			}
		}()
		for _, u := range deploymentUsers() {
			T.fileUserTools(u)
		}
	}()
}

// toolCategoryOf is t's category as its owner sees it: their own label, else
// the one it was filed under, else "".
func toolCategoryOf(owner string, t TempTool) string {
	c, _ := toolcat.Of(owner, t)
	return c
}
