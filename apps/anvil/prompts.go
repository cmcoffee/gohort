// Saved prompts, offered as slash commands as in Claude Code: "/review" or
// "/test" in the message box sends the prompt saved under that name, with
// anything typed after it. Two come with every project; a project adds its
// own, or replaces one of those by saving its own under the same name.
package anvil

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/ui"
)

// SavedPrompt is one command: its name without the slash, and what is sent.
type SavedPrompt struct {
	Name   string `json:"name"`
	Prompt string `json:"prompt"`
}

const (
	maxPrompts     = 40
	maxPromptChars = 4000
)

var promptName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// builtinPrompts come with every project.
var builtinPrompts = []struct {
	SavedPrompt
	desc string
}{
	{SavedPrompt{"review", "Review the changes not yet committed (git diff): what each one does, anything wrong or risky in it, and anything missing, such as a test. Change nothing."},
		"Review the uncommitted changes"},
	{SavedPrompt{"test", "Run the project's check, or its tests if it has no check, and fix what fails. Say what was wrong and what you changed."},
		"Run the tests and fix what fails"},
}

// validatePrompts tidies a project's saved prompts: names lower case without
// a leading slash, blank rows dropped, each name once.
func validatePrompts(in []SavedPrompt) ([]SavedPrompt, error) {
	var out []SavedPrompt
	seen := map[string]bool{}
	for _, sp := range in {
		name := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(sp.Name), "/"))
		prompt := strings.TrimSpace(sp.Prompt)
		if name == "" && prompt == "" {
			continue
		}
		if !promptName.MatchString(name) {
			return nil, fmt.Errorf("the saved prompt name %q: use letters, digits, - and _, up to 32, starting with a letter or digit", sp.Name)
		}
		if prompt == "" {
			return nil, fmt.Errorf("the saved prompt /%s has nothing to send", name)
		}
		if len([]rune(prompt)) > maxPromptChars {
			return nil, fmt.Errorf("the saved prompt /%s is over %d characters", name, maxPromptChars)
		}
		if seen[name] {
			return nil, fmt.Errorf("there are two saved prompts named /%s", name)
		}
		seen[name] = true
		out = append(out, SavedPrompt{Name: name, Prompt: prompt})
	}
	if len(out) > maxPrompts {
		return nil, fmt.Errorf("a project keeps up to %d saved prompts", maxPrompts)
	}
	return out, nil
}

// slashCommand is one entry of the list the message box offers.
type slashCommand struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Prompt      string `json:"prompt"`
}

// slashCommands is what p offers: its own, then the built-ins it has not
// replaced.
func slashCommands(p Project) []slashCommand {
	out := []slashCommand{}
	own := map[string]bool{}
	for _, sp := range p.Prompts {
		own[sp.Name] = true
		out = append(out, slashCommand{Name: sp.Name, Prompt: sp.Prompt})
	}
	for _, b := range builtinPrompts {
		if !own[b.Name] {
			out = append(out, slashCommand{Name: b.Name, Description: b.desc, Prompt: b.Prompt})
		}
	}
	return out
}

func (T *Anvil) handleCommands(c *jsonapi.Call) (any, error) {
	p, ok := T.findProject(c.UDB, c.User, callerIsAdmin(c.R), strings.TrimSpace(c.R.URL.Query().Get("project_id")))
	if !ok {
		return []slashCommand{}, nil
	}
	return slashCommands(p), nil
}

// promptsField is the project setting.
func promptsField() ui.FormField {
	return ui.FormField{
		Field: "prompts", Label: "Saved prompts", Type: "rows", AddLabel: "＋ Add a prompt",
		Help: "Type / in the message box to use one: /name sends its prompt, with anything you type after the name. /review and /test come with every project; save one under the same name to change it.",
		Columns: []ui.FormField{
			{Field: "name", Label: "Name", Type: "text", Placeholder: "The word after the /"},
			{Field: "prompt", Label: "Prompt", Type: "textarea", Placeholder: "What is sent when you use it"},
		},
	}
}
