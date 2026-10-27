package core

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func runHintTool() *GroupedTool {
	g := NewGroupedTool("tool_def", "manage tools")
	g.AddAction("list", &GroupedToolAction{
		Description: "list tools",
		Handler:     func(map[string]any, *ToolSession) (string, error) { return "ok", nil },
	})
	g.AddAction("get", &GroupedToolAction{
		Description: "read one tool",
		Params:      map[string]ToolParam{"name": {Type: "string", Description: "tool name"}},
		Required:    []string{"name"},
		Handler:     func(map[string]any, *ToolSession) (string, error) { return "ok", nil },
	})
	return g
}

// action="help" ignores every other param. A model that writes
// help(name="get_top_stories") is asking about ONE tool and gets the generic
// authoring spec — a SUCCESS answering a different question, so the miss is
// invisible. Observed live: an agent asked twice, got the manual twice,
// concluded a published tool needed building, and ran the verifier three times
// (a real dispatch, three live fetches) before it thought to just call it.
func TestHelpWithParamsFlagsTheMissAndRoutesIt(t *testing.T) {
	g := runHintTool()
	out, err := g.Run(map[string]any{"action": "help", "name": "get_top_stories"})
	if err != nil {
		t.Fatalf("help should still return the spec: %v", err)
	}
	if !strings.Contains(out, "NOTE:") || !strings.Contains(out, "name") {
		t.Errorf("the ignored param should be named up front, got:\n%s", out)
	}
	// Route the caller to the action that actually takes "name".
	if !strings.Contains(out, `action="get"`) {
		t.Errorf("help should point at the action declaring the ignored param, got:\n%s", out)
	}
	// The banner heads the output — a model skims the top of a long dump.
	if idx := strings.Index(out, "NOTE:"); idx != 0 {
		t.Errorf("banner must lead the output, found at offset %d", idx)
	}
	// The spec itself is still there; the caller did ask for help.
	if !strings.Contains(out, "tool_def, usage:") {
		t.Errorf("the usage spec should still follow the banner, got:\n%s", out)
	}
}

// A bare help call is a legitimate request for the manual and must stay clean —
// no banner, no scolding.
func TestBareHelpHasNoBanner(t *testing.T) {
	g := runHintTool()
	out, err := g.Run(map[string]any{"action": "help"})
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	if strings.Contains(out, "NOTE:") {
		t.Errorf("a bare help call should return the spec unadorned, got:\n%s", out)
	}
}

// An agent that inspected a custom tool through tool_def (list → get → test)
// then reached for action="run" got back only the list of valid actions, and
// spent the rest of the turn improvising: inventing routes, then trying to run
// the tool's underlying script by hand with guessed workspace paths. The one
// fact that would have unstuck it — call the tool directly by name, which the
// loop's lazy fallback resolves whether or not it is in the visible catalog —
// was never stated.
func TestUnknownRunActionPointsAtCallingDirectly(t *testing.T) {
	g := runHintTool()
	for _, action := range []string{"run", "execute", "invoke", "call", "use"} {
		_, err := g.Run(map[string]any{"action": action})
		if err == nil {
			t.Fatalf("action %q should be rejected", action)
		}
		if !strings.Contains(err.Error(), "DIRECTLY by its own name") {
			t.Errorf("action %q should point at calling the tool directly, got: %v", action, err)
		}
	}
}

// A plain typo is not a request to execute something — it gets the ordinary
// error without the extra sentence, so the hint keeps its meaning.
func TestUnknownNonRunActionStaysTerse(t *testing.T) {
	g := runHintTool()
	_, err := g.Run(map[string]any{"action": "lsit"})
	if err == nil {
		t.Fatal("a typo action should be rejected")
	}
	if strings.Contains(err.Error(), "DIRECTLY by its own name") {
		t.Errorf("a typo should not get the run hint, got: %v", err)
	}
	if !strings.Contains(err.Error(), "list") {
		t.Errorf("the error should still name the valid actions, got: %v", err)
	}
}

// A bare call is the shape a dropped-argument call arrives in. Answering it
// with the usage spec returns a long, successful-looking result that contains
// no data — the model cannot tell it from an answer, and re-calls. One observed
// turn burned 79 seconds that way across three tools.
func TestBareGroupedCallIsAnErrorNotHelp(t *testing.T) {
	g := runHintTool()
	out, err := g.Run(map[string]any{})
	if err == nil {
		t.Fatalf("a bare call must ERROR, not answer with help — got %d chars of result", len(out))
	}
	msg := err.Error()
	// Actionable on its own: name the actions, say plainly nothing happened.
	for _, want := range []string{"nothing was done", "list", "get", "help"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should mention %q: %s", want, msg)
		}
	}
	// And name the dropped-argument case, which is the actual cause when a
	// model that DID construct arguments lands here.
	if !strings.Contains(msg, "did not arrive") {
		t.Errorf("error should raise the dropped-argument possibility: %s", msg)
	}
}

// Explicit probing still works — discovery was never the problem.
func TestExplicitHelpStillReturnsTheSpec(t *testing.T) {
	g := runHintTool()
	out, err := g.Run(map[string]any{"action": "help"})
	if err != nil {
		t.Fatalf("action=help must still work: %v", err)
	}
	if !strings.Contains(out, "list") {
		t.Errorf("help should list the actions: %s", out)
	}
}

// newTestPostTool builds a grouped tool mirroring moltbook's "post" action
// so we exercise the exact shape that sent the Builder agent into a
// re-send loop (submolt_name typed as submolta_name).
func newTestPostTool() *GroupedTool {
	gt := NewGroupedTool("moltbook", "test")
	gt.AddAction("post", &GroupedToolAction{
		Description: "Create a new post.",
		Params: map[string]ToolParam{
			"submolt_name": {Type: "string", Description: "target submolt"},
			"title":        {Type: "string", Description: "post title"},
			"content":      {Type: "string", Description: "post body"},
			"client_id":    {Type: "string", Description: "idempotency key"},
			"type":         {Type: "string", Description: "post type"},
		},
		Required: []string{"submolt_name", "title", "client_id"},
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			return "ok", nil
		},
	})
	return gt
}

func TestGroupedTool_TypoOnRequiredParamIsNamed(t *testing.T) {
	gt := newTestPostTool()
	_, err := gt.Run(map[string]any{
		"action":        "post",
		"submolta_name": "general", // the typo that caused the loop
		"title":         "hi",
		"client_id":     "abc",
	})
	if err == nil {
		t.Fatal("expected error for missing required submolt_name")
	}
	msg := err.Error()
	if !strings.Contains(msg, `you supplied "submolta_name"`) {
		t.Errorf("error should name the wrong key; got: %s", msg)
	}
	if !strings.Contains(msg, `did you mean "submolt_name"`) {
		t.Errorf("error should suggest the intended param; got: %s", msg)
	}
}

func TestGroupedTool_PlainMissingHasNoTypoHint(t *testing.T) {
	gt := newTestPostTool()
	// submolt_name simply omitted (no near-miss key supplied) — the
	// message must NOT invent a "did you mean".
	_, err := gt.Run(map[string]any{
		"action":    "post",
		"title":     "hi",
		"client_id": "abc",
	})
	if err == nil {
		t.Fatal("expected error for missing required submolt_name")
	}
	if strings.Contains(err.Error(), "did you mean") {
		t.Errorf("no near-miss key was supplied; should not suggest one; got: %s", err.Error())
	}
}

func TestGroupedTool_UnrelatedExtraKeyNotFlagged(t *testing.T) {
	// A genuinely unrelated stray key (not a typo of any param) shouldn't
	// produce a spurious suggestion — only the real missing-param message.
	def := &GroupedToolAction{
		Params: map[string]ToolParam{
			"submolt_name": {Type: "string"},
			"title":        {Type: "string"},
		},
	}
	if got := def.nearestParamName("xyzzy"); got != "" {
		t.Errorf("unrelated key matched %q; want no suggestion", got)
	}
	// Transposition-style typo should still resolve.
	if got := def.nearestParamName("titel"); got != "title" {
		t.Errorf("titel should suggest title; got %q", got)
	}
}

// TestGroupedTool_AllMissingParamsReportedAtOnce pins the batch validation:
// every missing required param lands in ONE error, with the typo hint, so
// the model converges in a single retry instead of discovering its mistakes
// serially (observed live: a reply_to_comment missing post_id was "fixed"
// into one missing content, and the model abandoned the action after two
// rounds of one-at-a-time errors).
func TestGroupedTool_AllMissingParamsReportedAtOnce(t *testing.T) {
	gt := newTestPostTool()
	_, err := gt.Run(map[string]any{
		"action":        "post",
		"submolta_name": "general", // typo — the real param is missing
		"title":         "",        // present but empty
	})
	if err == nil {
		t.Fatal("expected error for missing/empty required params")
	}
	msg := err.Error()
	for _, want := range []string{`"submolt_name"`, `"client_id"`, `non-empty "title"`, `"submolta_name"`, "did you mean"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("batch error should mention %s; got: %s", want, msg)
		}
	}
	// And the fixed call goes straight through.
	if out, err := gt.Run(map[string]any{
		"action": "post", "submolt_name": "general", "title": "hi", "client_id": "abc",
	}); err != nil || out != "ok" {
		t.Fatalf("complete call should succeed; out=%q err=%v", out, err)
	}
}

// TestSerialFireWiring guards the serial-fire plumbing the agent loop relies
// on: a GroupedTool marked serial-fire must report SerialFirePerBatch (and NOT
// SingleFirePerBatch), and ChatToolToAgentToolDef must propagate that onto the
// AgentToolDef. rebuildToolMaps keys off exactly these two flags — a serial
// tool that leaked into the single-fire set would have its excess batched
// calls dropped instead of run in order, reintroducing the tool_def
// delete-then-create footgun this replaced.
func TestSerialFireWiring(t *testing.T) {
	serial := NewGroupedTool("t_serial", "serial authoring tool")
	serial.AddAction("noop", &GroupedToolAction{
		Description: "noop",
		Handler:     func(args map[string]any, sess *ToolSession) (string, error) { return "ok", nil },
	})
	serial.SetSerialFirePerBatch(true)

	if !serial.SerialFirePerBatch() {
		t.Fatal("SerialFirePerBatch() should be true after SetSerialFirePerBatch(true)")
	}
	if serial.SingleFirePerBatch() {
		t.Fatal("a serial-fire tool must NOT also report single-fire")
	}

	def := ChatToolToAgentToolDefWithSession(serial, nil)
	if !def.SerialFirePerBatch {
		t.Error("AgentToolDef.SerialFirePerBatch not propagated from the tool")
	}
	if def.SingleFirePerBatch {
		t.Error("AgentToolDef.SingleFirePerBatch should be false for a serial-fire tool")
	}

	// A plain single-fire tool must still register as single-fire only.
	single := NewGroupedTool("t_single", "single-fire tool")
	single.AddAction("noop", &GroupedToolAction{
		Description: "noop",
		Handler:     func(args map[string]any, sess *ToolSession) (string, error) { return "ok", nil },
	})
	single.SetSingleFirePerBatch(true)
	sdef := ChatToolToAgentToolDefWithSession(single, nil)
	if !sdef.SingleFirePerBatch || sdef.SerialFirePerBatch {
		t.Errorf("single-fire tool mis-wired: single=%v serial=%v", sdef.SingleFirePerBatch, sdef.SerialFirePerBatch)
	}
}

// A call that names no action but whose params can only mean one runs that
// action. An agent sent workspace a command with no action six times in one
// turn; "command" belongs to run alone, so each refusal cost a round for
// nothing. A shape that fits more than one action, or none, is still refused.
func TestAnUnambiguousCallWithNoActionRunsTheOnlyActionItFits(t *testing.T) {
	ran := ""
	act := func(name string, params ...string) *GroupedToolAction {
		p := map[string]ToolParam{}
		for _, k := range params {
			p[k] = ToolParam{Type: "string"}
		}
		return &GroupedToolAction{Params: p, Handler: func(map[string]any, *ToolSession) (string, error) {
			ran = name
			return name + " done", nil
		}}
	}
	g := NewGroupedTool("workspace", "files")
	g.AddAction("run", act("run", "command", "cwd_root"))
	g.AddAction("cat", act("cat", "path"))
	g.AddAction("write", act("write", "path", "content"))

	out, err := g.Run(map[string]any{"command": "ls"})
	if err != nil || ran != "run" {
		t.Fatalf("command alone means run: ran %q, err %v", ran, err)
	}
	if !strings.Contains(out, `action="run" was inferred`) || !strings.Contains(out, "run done") {
		t.Errorf("the result should say the action was inferred and carry its output: %q", out)
	}
	ran = ""
	if _, err := g.Run(map[string]any{"path": "a", "content": "b"}); err != nil || ran != "write" {
		t.Errorf("path+content fits write alone: ran %q, err %v", ran, err)
	}
	for _, args := range []map[string]any{
		{"path": "a"},                  // cat and write both take path, neither alone
		{"command": "ls", "path": "a"}, // no one action takes both
		{"script": "ls"},               // no action takes it
	} {
		ran = ""
		if _, err := g.Run(args); err == nil || ran != "" {
			t.Errorf("%v is ambiguous and must be refused, ran %q", args, ran)
		} else if !strings.Contains(err.Error(), "nothing was done") {
			t.Errorf("the refusal should still say nothing was done: %v", err)
		}
	}
}

// Only a tool whose definition the user wrote is tracked for "never worked".
// A built-in authoring tool's failures are the caller's arguments, and telling
// the model its definition is broken stops it fixing them: tool_def got
// "STOP RETRYING THIS ... broken tool DEFINITION" on the model's own
// validation errors, in a fresh sandbox where nothing had succeeded yet.
func TestOnlyTrackedToolsReportTheirOutcomes(t *testing.T) {
	var seen []string
	prev := RecordToolOutcome
	RecordToolOutcome = func(_ *ToolSession, tool, action string, err error) string {
		seen = append(seen, tool+"."+action)
		return " BROKEN"
	}
	t.Cleanup(func() { RecordToolOutcome = prev })
	sess := &ToolSession{}

	builtin := runHintTool()
	if _, err := builtin.RunWithSession(map[string]any{"action": "get"}, sess); err == nil || strings.Contains(err.Error(), "BROKEN") {
		t.Fatalf("a built-in tool's validation error = %v", err)
	}
	if len(seen) != 0 {
		t.Fatalf("a built-in tool was tracked: %v", seen)
	}

	toolbox := runHintTool()
	toolbox.SetTrackOutcomes(true)
	if _, err := toolbox.RunWithSession(map[string]any{"action": "get"}, sess); err == nil || !strings.HasSuffix(err.Error(), "BROKEN") {
		t.Fatalf("a tracked tool's error lost the advice: %v", err)
	}
	if len(seen) != 1 || seen[0] != "tool_def.get" {
		t.Fatalf("tracked = %v", seen)
	}
}

// TestGroupedTool_NoLookAlikeForShortKey pins the scoring fix: "cases"
// shares two bigrams with "hook_capabilities" ("ca", "es"), which the old
// supplied-key-only overlap took for a near miss. Scored against both
// lengths it is nowhere close, while real typos, including a stray digit
// and a transposition, still resolve.
func TestGroupedTool_NoLookAlikeForShortKey(t *testing.T) {
	def := &GroupedToolAction{Params: map[string]ToolParam{
		"description":       {Type: "string"},
		"hook_capabilities": {Type: "array"},
		"from_currency":     {Type: "string"},
		"title":             {Type: "string"},
	}}
	if got := def.nearestParamName("cases"); got != "" {
		t.Errorf("cases matched %q; want no suggestion", got)
	}
	for key, want := range map[string]string{
		"from1_currency": "from_currency",
		"titel":          "title",
		"descripton":     "description",
	} {
		if got := def.NearestParamName(key); got != want {
			t.Errorf("%s: got %q, want %q", key, got, want)
		}
	}
}

// TestGroupedTool_KeyOfSiblingActionNamed: a key that is a real param of
// another action is named as that action's, not passed through a look-alike
// guess. Observed: create called with cases (a param of test) was told
// "did you mean hook_capabilities?".
func TestGroupedTool_KeyOfSiblingActionNamed(t *testing.T) {
	gt := NewGroupedTool("tool_def", "manage tools")
	ok := func(map[string]any, *ToolSession) (string, error) { return "ok", nil }
	gt.AddAction("create", &GroupedToolAction{
		Params:   map[string]ToolParam{"description": {Type: "string"}, "hook_capabilities": {Type: "array"}},
		Required: []string{"description"},
		Handler:  ok,
	})
	gt.AddAction("test", &GroupedToolAction{
		Params:  map[string]ToolParam{"cases": {Type: "array"}},
		Handler: ok,
	})
	_, err := gt.Run(map[string]any{"action": "create", "cases": []any{}})
	if err == nil {
		t.Fatal("expected a missing-param error")
	}
	msg := err.Error()
	if !strings.Contains(msg, `"cases", which belongs to action="test"`) {
		t.Errorf("want the sibling action named; got: %s", msg)
	}
	if strings.Contains(msg, "hook_capabilities") {
		t.Errorf("must not guess a look-alike for a sibling's param; got: %s", msg)
	}
}

// A confirmation the framework prints is marked so the fence leaves it alone;
// an action that returns what it read is not, and neither is an error.
func TestGroupedToolFrameworkOutputIsMarked(t *testing.T) {
	gt := NewGroupedTool("files_like", "test")
	gt.AddAction("write", &GroupedToolAction{
		FrameworkOutput: true,
		Handler:         func(map[string]any, *ToolSession) (string, error) { return "wrote 3 bytes to a.txt", nil },
	})
	gt.AddAction("cat", &GroupedToolAction{
		Handler: func(map[string]any, *ToolSession) (string, error) { return "whatever the file said", nil },
	})
	gt.AddAction("fail", &GroupedToolAction{
		FrameworkOutput: true,
		Handler:         func(map[string]any, *ToolSession) (string, error) { return "", errors.New("no such file") },
	})
	sess := &ToolSession{}
	out, _ := gt.RunWithSession(map[string]any{"action": "write"}, sess)
	if body, marked := TakeFrameworkResultMark(out); !marked || body != "wrote 3 bytes to a.txt" {
		t.Errorf("write: marked=%v body=%q, want marked confirmation", marked, body)
	}
	out, _ = gt.RunWithSession(map[string]any{"action": "cat"}, sess)
	if _, marked := TakeFrameworkResultMark(out); marked {
		t.Error("cat returns what it read and must stay fenced")
	}
	out, err := gt.RunWithSession(map[string]any{"action": "fail"}, sess)
	if _, marked := TakeFrameworkResultMark(out); marked || err == nil {
		t.Errorf("an error came back marked=%v err=%v", marked, err)
	}
}

// The note that names a call's unused keys is only as true as each action's
// Params and Accepts. A handler that reads a key declared in neither would be
// told it did nothing when it did, so every grouped action in the tree is
// read here and each key its handler reads by name must be declared.
//
// Parsed, not run: handlers have side effects. What it can see is a key
// named as a string literal (StringArg(args, "k"), args["k"], or a range over
// a literal list or map whose element indexes args), in the handler and in
// the same-package functions it passes args on to. A forward into another
// package is not followed, so an action that hands args to another tool
// lists that tool's extra keys in Accepts by hand (workspace write's mode).
func TestGroupedActionsDeclareWhatTheyRead(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	dirs := map[string]bool{}
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if n := info.Name(); p != root && (strings.HasPrefix(n, ".") || n == "testdata" || n == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
			if b, err := os.ReadFile(p); err == nil && bytes.Contains(b, []byte("GroupedToolAction{")) {
				dirs[filepath.Dir(p)] = true
			}
		}
		return nil
	})
	if len(dirs) < 3 {
		t.Fatalf("found grouped actions in only %d package(s) under %s: the walk is not reaching the tree", len(dirs), root)
	}
	checked := 0
	for dir := range dirs {
		s := newReadScan(t, dir)
		for _, f := range s.files {
			ast.Inspect(f, func(n ast.Node) bool {
				cl, ok := n.(*ast.CompositeLit)
				if !ok || !isGroupedActionType(cl.Type) {
					return true
				}
				if s.checkAction(t, root, cl) {
					checked++
				}
				return true
			})
		}
	}
	if checked < 40 {
		t.Fatalf("checked only %d grouped actions: the scan is missing most of them", checked)
	}
}

type readScan struct {
	fset    *token.FileSet
	files   []*ast.File
	funcs   map[string]*ast.FuncDecl
	methods map[string][]*ast.FuncDecl
}

func newReadScan(t *testing.T, dir string) *readScan {
	s := &readScan{fset: token.NewFileSet(), funcs: map[string]*ast.FuncDecl{}, methods: map[string][]*ast.FuncDecl{}}
	pkgs, err := parser.ParseDir(s.fset, dir, func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", dir, err)
	}
	for _, p := range pkgs {
		for _, f := range p.Files {
			s.files = append(s.files, f)
			for _, d := range f.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok {
					continue
				}
				if fd.Recv == nil {
					s.funcs[fd.Name.Name] = fd
				} else {
					s.methods[fd.Name.Name] = append(s.methods[fd.Name.Name], fd)
				}
			}
		}
	}
	return s
}

func isGroupedActionType(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name == "GroupedToolAction"
	case *ast.SelectorExpr:
		return x.Sel.Name == "GroupedToolAction"
	}
	return false
}

// checkAction reports each key the action's handler reads that it does not
// declare. False when the action could not be checked: no handler, or a
// Params that is not written out here.
func (s *readScan) checkAction(t *testing.T, root string, cl *ast.CompositeLit) bool {
	declared := map[string]bool{"action": true}
	var params, handler ast.Expr
	for _, e := range cl.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		id, _ := kv.Key.(*ast.Ident)
		if id == nil {
			continue
		}
		switch id.Name {
		case "Params":
			params = kv.Value
		case "Accepts":
			if lit, ok := kv.Value.(*ast.CompositeLit); ok {
				for _, k := range stringLits(lit.Elts) {
					declared[k] = true
				}
			}
		case "Handler":
			handler = kv.Value
		}
	}
	lit, ok := params.(*ast.CompositeLit)
	if !ok || handler == nil {
		return false
	}
	for _, e := range lit.Elts {
		if kv, ok := e.(*ast.KeyValueExpr); ok {
			for _, k := range stringLits([]ast.Expr{kv.Key}) {
				declared[k] = true
			}
		}
	}
	reads := map[string]bool{}
	seen := map[*ast.FuncDecl]bool{}
	s.followHandler(handler, reads, seen)
	var undeclared []string
	for k := range reads {
		if !declared[k] && !strings.HasPrefix(k, "_") {
			undeclared = append(undeclared, strconv.Quote(k))
		}
	}
	if len(undeclared) > 0 {
		sort.Strings(undeclared)
		pos := s.fset.Position(cl.Pos())
		rel, _ := filepath.Rel(root, pos.Filename)
		t.Errorf("%s:%d: the handler reads %s, declared in neither Params nor Accepts. Add each to Params when the model should send it, or to Accepts when only the framework does (or a tool it forwards to honors it): an undeclared key a call sends is reported back as doing nothing",
			rel, pos.Line, strings.Join(undeclared, ", "))
	}
	return true
}

func (s *readScan) followHandler(h ast.Expr, reads map[string]bool, seen map[*ast.FuncDecl]bool) {
	switch x := h.(type) {
	case *ast.FuncLit:
		s.walkFunc(x.Type, x.Body, 0, reads, seen)
	case *ast.Ident, *ast.SelectorExpr:
		if fd := s.resolve(x); fd != nil && !seen[fd] {
			seen[fd] = true
			s.walkFunc(fd.Type, fd.Body, 0, reads, seen)
		}
	case *ast.CallExpr:
		// A wrapper around the real handler: connectorMadeHere(connectorCreate).
		for _, a := range x.Args {
			s.followHandler(a, reads, seen)
		}
	}
}

// resolve finds the same-package function a call names. A method is followed
// only when its name is the package's only method by that name: without types
// there is no telling which RunWithSession a call reaches.
func (s *readScan) resolve(e ast.Expr) *ast.FuncDecl {
	switch x := e.(type) {
	case *ast.Ident:
		return s.funcs[x.Name]
	case *ast.SelectorExpr:
		if ms := s.methods[x.Sel.Name]; len(ms) == 1 {
			return ms[0]
		}
	}
	return nil
}

func (s *readScan) walkFunc(ft *ast.FuncType, body *ast.BlockStmt, depth int, reads map[string]bool, seen map[*ast.FuncDecl]bool) {
	if body == nil || ft.Params == nil {
		return
	}
	for _, f := range ft.Params.List {
		if _, ok := f.Type.(*ast.MapType); ok && len(f.Names) > 0 && f.Names[0].Name != "_" {
			s.walkBody(f.Names[0].Name, body, depth, reads, seen)
			return
		}
	}
}

func (s *readScan) walkBody(argName string, body *ast.BlockStmt, depth int, reads map[string]bool, seen map[*ast.FuncDecl]bool) {
	// Loop variables ranging over a written-out list or map of names: a key
	// read through one of them is each of those names.
	bound := map[string][]string{}
	ast.Inspect(body, func(n ast.Node) bool {
		rs, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		lit, ok := rs.X.(*ast.CompositeLit)
		if !ok {
			return true
		}
		switch lit.Type.(type) {
		case *ast.ArrayType:
			if v, ok := rs.Value.(*ast.Ident); ok {
				bound[v.Name] = append(bound[v.Name], stringLits(lit.Elts)...)
			}
		case *ast.MapType:
			if k, ok := rs.Key.(*ast.Ident); ok {
				var keys []ast.Expr
				for _, e := range lit.Elts {
					if kv, ok := e.(*ast.KeyValueExpr); ok {
						keys = append(keys, kv.Key)
					}
				}
				bound[k.Name] = append(bound[k.Name], stringLits(keys)...)
			}
		}
		return true
	})
	isArgs := func(e ast.Expr) bool {
		id, ok := e.(*ast.Ident)
		return ok && id.Name == argName
	}
	key := func(e ast.Expr) {
		for _, k := range stringLits([]ast.Expr{e}) {
			reads[k] = true
		}
		if id, ok := e.(*ast.Ident); ok {
			for _, k := range bound[id.Name] {
				reads[k] = true
			}
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.IndexExpr:
			if isArgs(x.X) {
				key(x.Index)
			}
		case *ast.CallExpr:
			for i, a := range x.Args {
				if !isArgs(a) {
					continue
				}
				if i+1 < len(x.Args) {
					key(x.Args[i+1])
				}
				if depth >= 6 {
					continue
				}
				fd := s.resolve(x.Fun)
				if fd == nil || seen[fd] {
					continue
				}
				seen[fd] = true
				if name := paramNameAt(fd.Type, i); name != "" && name != "_" {
					s.walkBody(name, fd.Body, depth+1, reads, seen)
				}
			}
		}
		return true
	})
}

func paramNameAt(ft *ast.FuncType, i int) string {
	idx := 0
	for _, f := range ft.Params.List {
		n := len(f.Names)
		if n == 0 {
			n = 1
		}
		if i < idx+n {
			if len(f.Names) == 0 {
				return ""
			}
			return f.Names[i-idx].Name
		}
		idx += n
	}
	return ""
}

func stringLits(es []ast.Expr) []string {
	var out []string
	for _, e := range es {
		if bl, ok := e.(*ast.BasicLit); ok && bl.Kind == token.STRING {
			if s, err := strconv.Unquote(bl.Value); err == nil {
				out = append(out, s)
			}
		}
	}
	return out
}

// The note itself: a key the action does not take is named, with the action
// that does take it; a declared, accepted or framework key is not; and it
// reaches the model through the tool's AgentToolDef, after the result.
func TestUnusedParamsAreNamedToTheModel(t *testing.T) {
	g := NewGroupedTool("workspace", "files")
	g.AddAction("grep", &GroupedToolAction{
		Description: "search a file",
		Params: map[string]ToolParam{
			"path":    {Type: "string", Description: "file"},
			"pattern": {Type: "string", Description: "regex"},
		},
		Accepts: []string{"legacy"},
		Handler: func(map[string]any, *ToolSession) (string, error) { return "3 matches", nil },
	})
	g.AddAction("read_lines", &GroupedToolAction{
		Description: "page a file",
		Params: map[string]ToolParam{
			"path":   {Type: "string", Description: "file"},
			"offset": {Type: "integer", Description: "first line"},
		},
		Handler: func(map[string]any, *ToolSession) (string, error) { return "lines", nil },
	})

	note := g.unusedArgsNote(map[string]any{"action": "grep", "path": "a.log", "pattern": "x", "offset": 200, "Lines": 100})
	for _, want := range []string{`action="grep" does not take "Lines", "offset"`, `"offset", which belongs to action="read_lines"`, "Its params: path, pattern."} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q:\n%s", want, note)
		}
	}
	for _, args := range []map[string]any{
		{"action": "grep", "path": "a", "pattern": "x"},
		{"action": "grep", "PATH": "a", "pattern": "x", "legacy": true, "_carried": 1},
		{"path": "a", "offset": 1}, // inferred: every key is the inferred action's
		{"action": "nope", "x": 1}, // refused before it runs
		{"action": "help", "x": 1}, // help has its own banner
	} {
		if n := g.unusedArgsNote(args); n != "" {
			t.Errorf("%v: got a note for keys that were used or never ran:\n%s", args, n)
		}
	}

	def := ChatToolToAgentToolDefWithSession(g, nil)
	out, err := def.Handler(context.Background(), map[string]any{"action": "grep", "path": "a.log", "pattern": "x", "offset": 200})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "3 matches") || !strings.Contains(out, "[PARAMS NOT USED]") {
		t.Errorf("the model's result should be the tool's, then the note:\n%s", out)
	}
	if direct, _ := g.RunWithSession(map[string]any{"action": "grep", "path": "a.log", "pattern": "x", "offset": 200}, nil); direct != "3 matches" {
		t.Errorf("a direct caller parses what the tool returns and must get it bare, got %q", direct)
	}
}
