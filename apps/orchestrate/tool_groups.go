package orchestrate

// Tool groups: several of an agent's load-before-use tools shown as one line.
//
// The "Your custom tools" list is one line per tool, and a person who wraps
// an API one endpoint at a time ends up with a dozen lines for one service
// (gitlab_list_issues, gitlab_get_file, ...), each a separate load_tool
// round-trip. Tools that share an API, or a job, are listed together, and
// load_tool takes the group's name to load all of them at once.
//
// Presentation only. Nothing about what a tool may reach or how it is called
// changes: each member is still its own tool, loaded and dispatched by its
// own name, and the full-schema catalog is untouched.
//
// The key is never asked of the owner:
//   - a named credential: the tools on one API;
//   - no credential of its own (no_auth, a public API): the host it calls,
//     since every public API shares no_auth and grouping on that would merge
//     unrelated services;
//   - no API at all (a shell script, a pipeline): its category, the owner's
//     own label when set, else one the worker filed it under once
//     (autoToolCategory).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/oddjob/core"
)

// toolGroupMin is how many tools a group needs before it collapses: two
// lines are no harder to read than one.
const toolGroupMin = 3

// toolGroupPrefix marks a group's name in the list and in load_tool, so a
// group can never be mistaken for a tool of the same name.
const toolGroupPrefix = "group:"

// toolGroup is one collapsed line: the name load_tool takes, what the group
// is, and its tools in order.
type toolGroup struct {
	Name    string // "group:gitlab"
	Label   string // "gitlab", "api.example.com", "Music"
	Kind    string // "api", "host" or "category"
	Members []string
}

// toolGroupKey is the key a tool groups under, and its label, or "" when it
// groups with nothing. category is the tool's own label, else its auto one.
func toolGroupKey(lt *TempTool, category string) (key, label, kind string) {
	if lt == nil {
		return "", "", ""
	}
	cred := strings.TrimSpace(lt.Credential)
	if cred == "" {
		for _, c := range lt.HookCapabilities {
			if i := strings.IndexByte(c, ':'); i >= 0 && strings.EqualFold(strings.TrimSpace(c[:i]), "fetch_via") {
				cred = strings.TrimSpace(c[i+1:])
				break
			}
		}
	}
	if cred != "" && !strings.EqualFold(cred, "no_auth") {
		return "api:" + strings.ToLower(cred), cred, "api"
	}
	if host := toolHost(lt); host != "" {
		return "host:" + host, host, "host"
	}
	if c := strings.TrimSpace(category); c != "" {
		return "cat:" + strings.ToLower(c), c, "category"
	}
	return "", "", ""
}

// toolHost is the host an api or toolbox tool calls, or "" for one that calls
// none, or calls through a relative URL (resolved against a credential's base,
// which a no_auth tool does not have).
func toolHost(lt *TempTool) string {
	var urls []string
	switch lt.Mode {
	case TempToolModeAPI:
		urls = append(urls, lt.CommandTemplate)
	case TempToolModeToolbox:
		for _, a := range lt.Actions {
			urls = append(urls, a.URLTemplate)
		}
	}
	for _, raw := range urls {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err == nil && u.Host != "" && !strings.ContainsAny(u.Host, "{}") {
			return strings.ToLower(u.Host)
		}
	}
	return ""
}

// apiless says a tool calls no API, so only a category can group it.
func apiless(lt *TempTool) bool {
	k, _, _ := toolGroupKey(lt, "")
	return k == ""
}

// groupTools collapses the lazy tools into groups of toolGroupMin or more by
// keyOf. Groups are named from their label, made unique in key order so the
// same tools always get the same names (the list is part of the prompt).
func groupTools(lazy []AgentToolDef, keyOf func(name string) (key, label, kind string)) map[string]toolGroup {
	type pending struct {
		label, kind string
		members     []string
	}
	byKey := map[string]*pending{}
	var keys []string
	for _, td := range lazy {
		k, label, kind := keyOf(td.Tool.Name)
		if k == "" {
			continue
		}
		p := byKey[k]
		if p == nil {
			p = &pending{label: label, kind: kind}
			byKey[k] = p
			keys = append(keys, k)
		}
		p.members = append(p.members, td.Tool.Name)
	}
	sort.Strings(keys)
	out := map[string]toolGroup{}
	for _, k := range keys {
		p := byKey[k]
		if len(p.members) < toolGroupMin {
			continue
		}
		name := toolGroupPrefix + groupSlug(p.label)
		for n := 2; ; n++ {
			if _, taken := out[name]; !taken {
				break
			}
			name = fmt.Sprintf("%s%s-%d", toolGroupPrefix, groupSlug(p.label), n)
		}
		out[name] = toolGroup{Name: name, Label: p.label, Kind: p.kind, Members: p.members}
	}
	return out
}

// groupSlug is a label as a group's name: lower case, runs of anything but
// letters, digits and dots as one "-".
func groupSlug(label string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(label) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// groupIndexLine is a group's entry in the load-before-use list: its name,
// what it is, and its tools by name (their leads are dropped: that is what
// the line saves, and each comes back with its schema on load).
func groupIndexLine(g toolGroup) string {
	what := map[string]string{"api": "tools on the " + g.Label + " API", "host": "tools calling " + g.Label, "category": g.Label + " tools"}[g.Kind]
	names := make([]string, len(g.Members))
	for i, m := range g.Members {
		names[i] = "`" + m + "`"
	}
	return fmt.Sprintf("- `%s` %d %s: %s\n", g.Name, len(g.Members), what, strings.Join(names, ", "))
}

// toolGroupsFor groups the turn's lazy tools, filing each API-less tool with
// no category of its own under the auto category the worker gave it, and
// asking the worker, in the background, for any it has not filed yet: those
// group from a later turn.
func (t *chatTurn) toolGroupsFor(sess *ToolSession, lazy []AgentToolDef) map[string]toolGroup {
	tools := map[string]*TempTool{}
	var unfiled []*TempTool
	for _, td := range lazy {
		lt := sess.LookupTempTool(td.Tool.Name)
		if lt == nil {
			continue
		}
		tools[td.Tool.Name] = lt
		if apiless(lt) && strings.TrimSpace(lt.Category) == "" {
			if _, ok := autoToolCategory(t.udb, lt); !ok {
				unfiled = append(unfiled, lt)
			}
		}
	}
	if len(unfiled) > 0 && t.app != nil {
		known := make([]*TempTool, 0, len(tools))
		for _, td := range lazy {
			if lt := tools[td.Tool.Name]; lt != nil {
				known = append(known, lt)
			}
		}
		t.fileToolsLater(known, unfiled)
	}
	return groupTools(lazy, func(name string) (string, string, string) {
		lt := tools[name]
		if lt == nil {
			return "", "", ""
		}
		cat := strings.TrimSpace(lt.Category)
		if cat == "" {
			cat, _ = autoToolCategory(t.udb, lt)
		}
		return toolGroupKey(lt, cat)
	})
}

// autoCategoryTable holds the categories the worker filed API-less tools
// under, keyed by tool name and a hash of what it does: a tool whose
// description changes is filed again. Kept apart from the tool record, which
// a write would re-version (and, for a published tool, unpublish).
const autoCategoryTable = "tool_auto_category"

func autoCategoryKey(lt *TempTool) string {
	sum := sha256.Sum256([]byte(lt.Mode + "\x00" + lt.Description))
	return lt.Name + "\x00" + hex.EncodeToString(sum[:8])
}

// autoToolCategory is the category the worker filed lt under, if it has.
func autoToolCategory(udb Database, lt *TempTool) (string, bool) {
	if udb == nil || lt == nil {
		return "", false
	}
	var c string
	if udb.Get(autoCategoryTable, autoCategoryKey(lt), &c) && strings.TrimSpace(c) != "" {
		return c, true
	}
	return "", false
}

// filing is the tools being filed now, so two turns do not ask twice.
var filing = struct {
	sync.Mutex
	m map[string]bool
}{m: map[string]bool{}}

// toolCategoryAsk is the worker's model for filing a tool; a test replaces it.
var toolCategoryAsk = func(ctx context.Context, app *OrchestrateApp, msgs []Message) (string, error) {
	resp, err := app.WorkerChat(ctx, msgs, WithJSONMode(), WorkerJudgeThink(), WithMaxTokens(600))
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

// fileToolsLater asks the worker, off the turn, to file each tool in a
// category, choosing among those already in use where one fits.
func (t *chatTurn) fileToolsLater(known, unfiled []*TempTool) {
	var mine []*TempTool
	filing.Lock()
	for _, lt := range unfiled {
		k := t.user + "\x00" + autoCategoryKey(lt)
		if !filing.m[k] {
			filing.m[k] = true
			// A copy of what filing reads: the session's record can change
			// under a running turn while this works off it.
			mine = append(mine, &TempTool{Name: lt.Name, Mode: lt.Mode, Description: lt.Description})
		}
	}
	filing.Unlock()
	if len(mine) == 0 {
		return
	}
	existing := t.categoriesInUse(known)
	app, udb, user := t.app, t.udb, t.user
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.ctx), 2*time.Minute)
	go func() {
		defer cancel()
		defer func() {
			filing.Lock()
			for _, lt := range mine {
				delete(filing.m, user+"\x00"+autoCategoryKey(lt))
			}
			filing.Unlock()
		}()
		for _, lt := range mine {
			cat, err := fileToolCategory(ctx, app, lt, existing)
			if err != nil {
				Debug("[orchestrate.tools] could not file %q in a category: %v", lt.Name, err)
				continue
			}
			udb.Set(autoCategoryTable, autoCategoryKey(lt), cat)
			Log("[orchestrate.tools] filed %q under %q (auto category; the owner's own label always wins)", lt.Name, cat)
			if !containsFold(existing, cat) {
				existing = append(existing, cat)
			}
		}
	}()
}

// categoriesInUse is every category known carries and every auto one filed,
// sorted: what a new tool is filed among first.
func (t *chatTurn) categoriesInUse(known []*TempTool) []string {
	seen := map[string]bool{}
	var out []string
	add := func(c string) {
		c = strings.TrimSpace(c)
		if c != "" && !seen[strings.ToLower(c)] {
			seen[strings.ToLower(c)] = true
			out = append(out, c)
		}
	}
	for _, lt := range known {
		add(lt.Category)
	}
	if t.udb != nil {
		for _, k := range t.udb.Keys(autoCategoryTable) {
			var c string
			if t.udb.Get(autoCategoryTable, k, &c) {
				add(c)
			}
		}
	}
	sort.Strings(out)
	return out
}

// toolCategorySystem is the worker's instruction for filing one tool.
const toolCategorySystem = "You file one tool into a category for an AI agent's tool list. " +
	"Use one of the categories already in use when it fits the tool's job. Coin a new one only when none does: one to three words in Title Case, naming the job or the service (such as \"Calendar\" or \"Music\"), never the tool's own name. " +
	"Answer with JSON only: {\"category\": \"<the category>\"}."

// fileToolCategory asks the worker for lt's category among existing.
func fileToolCategory(ctx context.Context, app *OrchestrateApp, lt *TempTool, existing []string) (string, error) {
	inUse := "none yet"
	if len(existing) > 0 {
		inUse = strings.Join(existing, ", ")
	}
	out, err := toolCategoryAsk(ctx, app, []Message{
		{Role: "system", Content: toolCategorySystem},
		{Role: "user", Content: "Categories already in use: " + inUse + "\n\nTool: " + lt.Name + "\n" + strings.TrimSpace(lt.Description)},
	})
	if err != nil {
		return "", err
	}
	var v struct {
		Category string `json:"category"`
	}
	if err := json.Unmarshal([]byte(extractJSONObject(out)), &v); err != nil {
		return "", fmt.Errorf("the answer did not parse: %w", err)
	}
	cat := strings.Join(strings.Fields(v.Category), " ")
	if cat == "" || len([]rune(cat)) > 32 || strings.EqualFold(cat, lt.Name) {
		return "", fmt.Errorf("no usable category in %q", clip(out, 80))
	}
	for _, e := range existing {
		if strings.EqualFold(e, cat) {
			return e, nil // the spelling already in use, not a near twin
		}
	}
	return cat, nil
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
