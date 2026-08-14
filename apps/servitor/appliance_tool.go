// A capability, mapped once and then callable by name.
//
// The alternative was handing an agent a general run_command over SSH, and it
// is worse in every direction that matters. The model composes a shell string,
// so the injection surface is whatever it writes; the risk classifier runs
// against text that did not exist a second ago; and the permission an owner
// grants is a whole RISK CATEGORY — an open set of commands nobody has read.
//
// So the agent asks for a capability instead, servitor works out the SHAPE
// once against what it knows about that box (the real binary, the flags that
// build takes, whether it needs sudo, what the service is actually called), and
// mints a bound tool. From then on the agent picks a TOOL rather than writing a
// command.
//
// WHAT MAKES IT SAFE IS THE SPLIT, NOT THE VALUES. Structure — the binary,
// flags, subcommands, pipes, redirections — is authored once and frozen. The
// model supplies VALUES only, and every string value is shell-quoted at render
// (renderApplianceCommand), so a value of "; rm -rf /" arrives as a literal
// argument. That is what lets a parameter be free-form: enumerating a version
// number or a ticket id was never possible, and refusing to mint anything with
// an open value would have ruled out most real tools.
//
// What quoting does NOT stop is a value that is dangerous in the app's own
// terms — "--env production" when staging was meant. Enums close that where
// servitor actually knows the set; the risk category and the per-agent grant
// cover it where it does not.
package servitor

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// applianceToolsTable holds minted tools, keyed appliance+name so the same
// capability can exist on two boxes without colliding.
const applianceToolsTable = "ssh_appliance_tools"

// ApplianceTool is one capability bound to one system.
type ApplianceTool struct {
	Name        string `json:"name"`        // llm-facing, snake_case
	Description string `json:"description"` // what it does, in the agent's terms
	ApplianceID string `json:"appliance_id"`
	// Template is the command with {param} placeholders. Structure lives here
	// and ONLY here — the model never contributes to it.
	Template string               `json:"template"`
	Params   map[string]ToolParam `json:"params,omitempty"`
	Required []string             `json:"required,omitempty"`
	// Risk is the category the template classified as when it was minted.
	// Stored rather than recomputed so an owner approves a decision they can
	// see, and so a reclassification cannot silently widen an approved tool.
	Risk RiskCategory `json:"risk,omitempty"`
	// MintedBy is the agent that asked for it, and Approved is the owner's
	// decision. A minted tool is inert until approved: asking for a capability
	// must not be the same act as being granted it.
	MintedBy string    `json:"minted_by,omitempty"`
	Approved bool      `json:"approved,omitempty"`
	Created  time.Time `json:"created"`
}

func applianceToolKey(applianceID, name string) string {
	return strings.ToLower(strings.TrimSpace(applianceID)) + "|" + strings.ToLower(strings.TrimSpace(name))
}

// toolNamePattern is what an LLM-facing tool name may be. Deliberately strict:
// the name reaches a tool catalog, and a name with a space or a slash in it is
// a name some caller will fail to address.
var toolNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{2,48}$`)

// SaveApplianceTool writes a minted tool. Approval is NOT settable here — it is
// the owner's decision and has its own path, so a mint call can never approve
// its own request.
func SaveApplianceTool(udb Database, t ApplianceTool) (ApplianceTool, error) {
	t.Name = strings.ToLower(strings.TrimSpace(t.Name))
	t.ApplianceID = strings.TrimSpace(t.ApplianceID)
	if !toolNamePattern.MatchString(t.Name) {
		return t, fmt.Errorf("tool name %q is not usable — lowercase letters, digits and underscore, 3-49 chars", t.Name)
	}
	if t.ApplianceID == "" {
		return t, fmt.Errorf("an appliance tool must be bound to a system")
	}
	if strings.TrimSpace(t.Template) == "" {
		return t, fmt.Errorf("a tool needs a command template")
	}
	if err := validateTemplateParams(t.Template, t.Params); err != nil {
		return t, err
	}
	t.Approved = false
	if t.Created.IsZero() {
		t.Created = time.Now()
	}
	if udb != nil {
		udb.Set(applianceToolsTable, applianceToolKey(t.ApplianceID, t.Name), t)
	}
	return t, nil
}

// validateTemplateParams refuses a template whose placeholders and declared
// params disagree.
//
// Both directions are real mistakes and neither is safe to paper over. A
// placeholder with no param renders as literal "{version}" and the command runs
// with a garbage argument; a param with no placeholder means the model is asked
// for a value that goes nowhere, and it will assume the value took effect.
func validateTemplateParams(template string, params map[string]ToolParam) error {
	used := map[string]bool{}
	for _, m := range templatePlaceholder.FindAllStringSubmatch(template, -1) {
		used[m[1]] = true
	}
	var missing, unused []string
	for name := range used {
		if _, ok := params[name]; !ok {
			missing = append(missing, name)
		}
	}
	for name := range params {
		if !used[name] {
			unused = append(unused, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(unused)
	if len(missing) > 0 {
		return fmt.Errorf("template uses {%s} with no matching parameter declared", strings.Join(missing, "}, {"))
	}
	if len(unused) > 0 {
		return fmt.Errorf("parameter(s) %s are declared but appear nowhere in the template — the value would be collected and discarded", strings.Join(unused, ", "))
	}
	return nil
}

var templatePlaceholder = regexp.MustCompile(`\{([a-z][a-z0-9_]*)\}`)

// ApproveApplianceTool is the owner's decision, and the only way Approved
// becomes true. Separate from SaveApplianceTool so that minting — which an
// agent can trigger — cannot approve anything.
func ApproveApplianceTool(udb Database, applianceID, name string, approved bool) bool {
	t, ok := LoadApplianceTool(udb, applianceID, name)
	if !ok {
		return false
	}
	t.Approved = approved
	udb.Set(applianceToolsTable, applianceToolKey(applianceID, name), t)
	return true
}

// LoadApplianceTool reads one tool.
func LoadApplianceTool(udb Database, applianceID, name string) (ApplianceTool, bool) {
	var t ApplianceTool
	if udb == nil {
		return t, false
	}
	if !udb.Get(applianceToolsTable, applianceToolKey(applianceID, name), &t) {
		return t, false
	}
	return t, true
}

// ListApplianceTools returns every minted tool, optionally narrowed to one
// system. Includes unapproved ones: the pending list is what an owner reviews.
func ListApplianceTools(udb Database, applianceID string) []ApplianceTool {
	if udb == nil {
		return nil
	}
	want := strings.ToLower(strings.TrimSpace(applianceID))
	var out []ApplianceTool
	for _, k := range udb.Keys(applianceToolsTable) {
		var t ApplianceTool
		if !udb.Get(applianceToolsTable, k, &t) {
			continue
		}
		if want != "" && !strings.EqualFold(t.ApplianceID, want) {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ApplianceID != out[j].ApplianceID {
			return out[i].ApplianceID < out[j].ApplianceID
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// DeleteApplianceTool removes one.
func DeleteApplianceTool(udb Database, applianceID, name string) {
	if udb != nil {
		udb.Unset(applianceToolsTable, applianceToolKey(applianceID, name))
	}
}

// --- rendering --------------------------------------------------------------

// renderApplianceCommand substitutes the caller's values into the frozen
// template, quoting every string so a value cannot become syntax.
//
// Numbers and booleans emit bare: their value space cannot hold a shell
// metacharacter, and quoting them would break templates that expect a bare
// integer. Everything else is single-quoted with embedded quotes escaped —
// the same rule temptool uses, kept identical on purpose so there is one
// answer to "how does a value reach a shell here".
func renderApplianceCommand(t ApplianceTool, args map[string]any) (string, error) {
	for _, name := range t.Required {
		if v, ok := args[name]; !ok || v == nil || strings.TrimSpace(fmt.Sprint(v)) == "" {
			return "", fmt.Errorf("%s is required", name)
		}
	}
	var b strings.Builder
	last := 0
	for _, loc := range templatePlaceholder.FindAllStringSubmatchIndex(t.Template, -1) {
		b.WriteString(t.Template[last:loc[0]])
		name := t.Template[loc[2]:loc[3]]
		p, declared := t.Params[name]
		if !declared {
			// Not a parameter of this tool: emit verbatim rather than guessing,
			// matching temptool's handling of literal braces.
			b.WriteString(t.Template[loc[0]:loc[1]])
			last = loc[1]
			continue
		}
		val, ok := args[name]
		if !ok {
			b.WriteString("''")
			last = loc[1]
			continue
		}
		if err := checkEnum(name, p, val); err != nil {
			return "", err
		}
		b.WriteString(shellValue(val, p))
		last = loc[1]
	}
	b.WriteString(t.Template[last:])
	return b.String(), nil
}

// checkEnum rejects a value outside a declared set. The enum is the only thing
// that can stop a value that is dangerous in the APP's terms rather than the
// shell's — "--env production" is perfectly well-quoted.
func checkEnum(name string, p ToolParam, val any) error {
	if len(p.Enum) == 0 {
		return nil
	}
	got := fmt.Sprint(val)
	for _, allowed := range p.Enum {
		if allowed == got {
			return nil
		}
	}
	return fmt.Errorf("%s must be one of: %s (got %q)", name, strings.Join(p.Enum, ", "), got)
}

// shellValue renders one value for a command line.
func shellValue(val any, p ToolParam) string {
	switch v := val.(type) {
	case bool:
		return fmt.Sprint(v)
	case int, int32, int64, float32, float64:
		return fmt.Sprint(v)
	}
	if p.Type == "integer" || p.Type == "number" || p.Type == "boolean" {
		// Declared constrained, but arrived as a string. Emit bare only when it
		// really is one — a "number" carrying "1; rm -rf /" is a string.
		s := fmt.Sprint(val)
		if numericLiteral.MatchString(s) {
			return s
		}
	}
	return quoteShell(fmt.Sprint(val))
}

var numericLiteral = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$|^(true|false)$`)

// quoteShell is temptool's rule, restated here rather than imported: this
// package cannot reach into a tools/ package, and a second spelling of the
// SAME rule is safer than a near-miss of it.
func quoteShell(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// --- minting ----------------------------------------------------------------

// mintPrompt asks for the SHAPE of a command, never for a decision about
// whether it should run. The classification and the grant answer that, and a
// model asked to judge its own request will approve it.
const mintPrompt = `You turn a request into ONE reusable shell command for a specific machine.

You are given what is already known about that machine. Use it: the real service names, package manager, init system, paths and flags it reports are more reliable than your defaults.

Rules:
- Produce ONE command. No shell pipelines that chain unrelated work, no "&&" sequences that do two jobs.
- Put every flag, path, subcommand and sudo INSIDE the template. The template is frozen after this.
- Use {placeholders} ONLY where a value genuinely varies between runs (a version, a filename, a service name). A request with nothing variable should have no parameters at all.
- Declare every placeholder as a parameter with a type. Use an enum ONLY when the known facts list the allowed values.
- When a placeholder is a FOLDER that belongs to one of the file stores listed below, declare "path_scope":"files:<slug>" on that parameter instead of an enum. The value is then checked against that store when the tool RUNS, and substituted as an absolute path. Use this rather than an enum whenever the set of folders can change, which it usually can — an enum is frozen when you write it and a drop folder is not.
- Never put a placeholder where a flag or subcommand goes. Values are quoted at runtime, so a placeholder can never contribute syntax.
- Name the tool for what it DOES on this machine, lowercase with underscores.

Reply with ONLY JSON:
{"name":"restart_nginx","description":"Restart the nginx service","template":"sudo systemctl restart nginx","params":{},"required":[]}

With a folder parameter it looks like this:
{"name":"parse_bundle","description":"Run logparse over one bundle","template":"logparse --dir {dir}","params":{"dir":{"type":"string","description":"Which bundle to parse.","path_scope":"files:support_bundles"}},"required":["dir"]}`

// MintApplianceTool maps an intent onto a concrete command for one system.
//
// Returns the tool UNSAVED and unapproved — the caller decides whether to keep
// it, and the owner decides whether it may run. Minting is a proposal.
func MintApplianceTool(ctx context.Context, chat FactChatFunc, appliance Appliance, facts []SshFact, intent, agentID, owner string) (ApplianceTool, error) {
	if chat == nil {
		return ApplianceTool{}, fmt.Errorf("no worker model available to map that request")
	}
	if strings.TrimSpace(intent) == "" {
		return ApplianceTool{}, fmt.Errorf("say what the tool should do")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "MACHINE: %s\n", applianceLabel(appliance.Name, appliance.ID))
	// Advertise the path constraints available on this deployment. An
	// unadvertised constraint is one nobody declares: the model cannot
	// scope a parameter to a store it has never been told exists, and
	// would reach for a free string or a frozen enum instead.
	if roots := PathScopeRoots(owner); len(roots) > 0 {
		b.WriteString("\nFILE STORES ON THIS SERVER (use path_scope for a folder parameter):\n")
		for _, rt := range roots {
			line := "- " + rt.Ref + " — " + rt.Label
			if rt.Detail != "" {
				line += ": " + rt.Detail
			}
			b.WriteString(line + "\n")
		}
	}
	if known := formatFacts(facts); strings.TrimSpace(known) != "" {
		fmt.Fprintf(&b, "\nWHAT IS KNOWN ABOUT IT:\n%s\n", known)
	} else {
		b.WriteString("\nNothing is known about this machine yet — prefer portable, widely available commands.\n")
	}
	fmt.Fprintf(&b, "\nREQUEST: %s\n", strings.TrimSpace(intent))

	resp, err := chat(ctx, []Message{{Role: "user", Content: b.String()}},
		WithSystemPrompt(mintPrompt), WithJSONMode(), WithThink(false))
	if err != nil {
		return ApplianceTool{}, fmt.Errorf("could not map that request: %w", err)
	}
	var out struct {
		Name        string               `json:"name"`
		Description string               `json:"description"`
		Template    string               `json:"template"`
		Params      map[string]ToolParam `json:"params"`
		Required    []string             `json:"required"`
	}
	if derr := DecodeJSON(ResponseText(resp), &out); derr != nil {
		return ApplianceTool{}, fmt.Errorf("could not read the mapped command: %w", derr)
	}
	t := ApplianceTool{
		Name:        strings.ToLower(strings.TrimSpace(out.Name)),
		Description: strings.TrimSpace(out.Description),
		ApplianceID: appliance.ID,
		Template:    strings.TrimSpace(out.Template),
		Params:      out.Params,
		Required:    out.Required,
		MintedBy:    strings.TrimSpace(agentID),
		Created:     time.Now(),
	}
	if t.Template == "" {
		return t, fmt.Errorf("no command could be mapped for that request")
	}
	if err := validateTemplateParams(t.Template, t.Params); err != nil {
		return t, fmt.Errorf("the mapped command is inconsistent: %w", err)
	}
	// Classified ONCE, here, against the frozen template — not per call against
	// text a model just produced. This is the number the owner approves.
	t.Risk, _ = classify_command_scoped(t.Template, "")
	return t, nil
}

// ApplianceToolJSON renders a minted tool for review. The template is shown in
// full deliberately: approving a capability without reading the command it runs
// is the whole failure this replaces.
func ApplianceToolJSON(t ApplianceTool) string {
	raw, _ := json.MarshalIndent(t, "", "  ")
	return string(raw)
}

// --- unchecked path parameters ----------------------------------------

// pathishParam matches a parameter name that almost certainly carries a
// filesystem path. Deliberately generous: over-flagging costs a sentence
// on an approval row, under-flagging costs the thing this exists to
// catch.
var pathishParam = regexp.MustCompile(`(?i)(^|_)(dir|directory|folder|path|file|filename|bundle|log|logs|target)($|_)`)

// UncheckedPathParams names the parameters that look like a path and
// carry no constraint at all — no path_scope, no enum.
//
// This is the SILENT failure of the whole arrangement. Rendering quotes
// a value so it can never contribute shell syntax, which reads as safety
// and is not: "../../var/lib/something" is a perfectly well-formed
// single argument. A tool minted without a path_scope still WORKS, and
// nothing about running it looks wrong, so the miss survives until
// someone goes looking for it.
//
// The answer is not to refuse the tool — a path parameter that genuinely
// has no store behind it is legitimate, and refusing would push authors
// toward baking a fixed path into the template instead. The answer is to
// say so on the row where the decision is made.
func (t ApplianceTool) UncheckedPathParams() []string {
	var out []string
	for name, p := range t.Params {
		if strings.TrimSpace(p.PathScope) != "" || len(p.Enum) > 0 {
			continue
		}
		if !pathishParam.MatchString(name) {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ScopedPathParams names the parameters that ARE constrained to a
// registered root, with the root they are constrained to. Shown for the
// same reason: an approver should be able to see the check exists, not
// just its absence.
func (t ApplianceTool) ScopedPathParams() []string {
	var out []string
	for name, p := range t.Params {
		if s := strings.TrimSpace(p.PathScope); s != "" {
			out = append(out, name+" → "+s)
		}
	}
	sort.Strings(out)
	return out
}
