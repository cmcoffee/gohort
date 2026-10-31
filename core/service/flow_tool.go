package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// flowTool is flow_<name>: a saved flow as a tool an agent can be given,
// with the flow's inputs as its parameters. A flow deleted since it was
// registered leaves every catalog (its schema is nil) and says so if called.
type flowTool struct{ flow string }

func (t *flowTool) Name() string { return "flow_" + t.flow }

func (t *flowTool) Category() string { return "Flows" }

func (t *flowTool) Desc() string {
	f, ok := liveFlow(t.flow)
	if !ok {
		return "A flow that has been removed."
	}
	return flowDesc(f)
}

func flowDesc(f Flow) string {
	return strings.TrimSpace(f.Description) + " Runs the same steps every time: " + f.Line() + "."
}

func (t *flowTool) Params() map[string]ToolParam {
	f, ok := liveFlow(t.flow)
	if !ok {
		return map[string]ToolParam{}
	}
	return flowParams(f)
}

func (t *flowTool) SchemaWithSession(*ToolSession) (string, map[string]ToolParam) {
	f, ok := liveFlow(t.flow)
	if !ok {
		return "", nil
	}
	return flowDesc(f), flowParams(f)
}

func flowParams(f Flow) map[string]ToolParam {
	out := map[string]ToolParam{}
	for _, in := range f.Inputs {
		p := ToolParam{Type: "string", Description: strings.TrimSpace(in.Description)}
		switch in.Type {
		case TypeNumber:
			p.Type = "number"
		case TypeJSON:
			p.Description = strings.TrimSpace(p.Description + " As JSON.")
		case TypeFile:
			p.Description = strings.TrimSpace(p.Description + " The path of a file in your workspace.")
		}
		if in.Required {
			p.Description = strings.TrimSpace(p.Description + " Required.")
		}
		out[in.Name] = p
	}
	return out
}

// Caps: a flow reaches what its connectors reach. A step on the network
// goes online; a local program runs.
func (t *flowTool) Caps() []Capability {
	f, _ := liveFlow(t.flow)
	network, execute := false, false
	for _, st := range f.Steps {
		c, ok := GetConnector(RootDB, st.Connector)
		if !ok {
			continue
		}
		if s, err := Parse(c.Spec); err == nil && s.Provider.Transport == "command" && !s.Provider.Network {
			execute = true
		} else {
			network = true
		}
	}
	var caps []Capability
	if network || !execute {
		caps = append(caps, CapNetwork)
	}
	if execute {
		caps = append(caps, CapExecute)
	}
	return caps
}

func (t *flowTool) Run(args map[string]any) (string, error) {
	return t.RunWithSession(args, nil)
}

func (t *flowTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	f, ok := liveFlow(t.flow)
	if !ok {
		return "", fmt.Errorf("the flow %s has been removed", t.flow)
	}
	in := map[string]any{}
	for _, fi := range f.Inputs {
		v, given := args[fi.Name]
		if !given || v == nil {
			continue
		}
		if fi.Type == TypeFile {
			rel := strings.TrimSpace(fmt.Sprint(v))
			if rel == "" {
				continue
			}
			if sess == nil || strings.TrimSpace(sess.WorkspaceDir) == "" {
				return "", fmt.Errorf("%s takes a file from your workspace, and there is no workspace", fi.Name)
			}
			abs, err := ResolveWorkspacePath(sess.WorkspaceDir, rel)
			if err != nil {
				return "", fmt.Errorf("%s: %w", fi.Name, err)
			}
			v = abs
		}
		in[fi.Name] = v
	}
	ctx := context.Background()
	if sess != nil {
		ctx = sess.Context()
	}
	res, _, err := f.Run(ctx, sess, in)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(res))
	for n := range res {
		names = append(names, n)
	}
	sort.Strings(names)
	var lines []string
	for _, n := range names {
		v := res[n]
		if v.File == nil {
			lines = append(lines, n+": "+clip(v.Text, 4000))
			continue
		}
		rel, err := keepInWorkspace(sess, f.Name+"-"+n, v.File)
		if err != nil {
			return "", err
		}
		lines = append(lines, fmt.Sprintf("%s: saved as %s in your workspace (%s, %d KB)", n, rel, v.File.Type, v.File.Size>>10))
	}
	return strings.Join(lines, "\n"), nil
}

// keepInWorkspace moves a file a flow made into the session's workspace,
// under a name not yet taken, and returns that name.
func keepInWorkspace(sess *ToolSession, base string, f *File) (string, error) {
	defer os.Remove(f.Path)
	if sess == nil || strings.TrimSpace(sess.WorkspaceDir) == "" {
		return "", fmt.Errorf("the flow made a file, but there is no workspace to save it in")
	}
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return "", err
	}
	ext := filepath.Ext(f.Path)
	base = strings.Trim(unsafeToolChars.ReplaceAllString(strings.ToLower(base), "-"), "-_")
	rel := base + ext
	for i := 2; ; i++ {
		abs, err := ResolveWorkspacePath(sess.WorkspaceDir, rel)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(abs); os.IsNotExist(err) {
			if err := os.WriteFile(abs, data, 0o644); err != nil {
				return "", err
			}
			break
		}
		rel = fmt.Sprintf("%s-%d%s", base, i, ext)
	}
	sess.AddStagedFiles([]string{rel})
	return rel, nil
}
