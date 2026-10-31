package service

// The contracts explained: what each is, what it takes and gives, what its
// check requires and where gohort uses it, made from the registry, so the
// admin's reference, the docs and what an assistant writing a spec is told
// say the same thing, and cannot drift from what is enforced.

import (
	"fmt"
	"strings"
)

// ContractInfo is one contract explained.
type ContractInfo struct {
	Name   string `json:"name"`
	About  string `json:"about"`
	Takes  string `json:"takes"`
	Gives  string `json:"gives"`
	Checks string `json:"checks"`
	Where  string `json:"where"`
}

// ContractInfos explains every contract, sorted by name.
func ContractInfos() []ContractInfo {
	var out []ContractInfo
	for _, n := range Names() {
		c := contracts[n]
		out = append(out, ContractInfo{Name: c.Name, About: c.About, Takes: ports(c.Inputs, true),
			Gives: ports(c.Outputs, false), Checks: c.Checks, Where: c.Where})
	}
	return out
}

// ports lists ports as name (type), marking the required ones when asked;
// "nothing" when there are none.
func ports(ps []Port, markRequired bool) string {
	var out []string
	for _, p := range ps {
		s := p.Name + " (" + string(p.Type)
		if markRequired && p.Required {
			s += ", required"
		}
		out = append(out, s+")")
	}
	if len(out) == 0 {
		return "nothing"
	}
	return strings.Join(out, ", ")
}

// ContractText says what a capability takes and gives, and what its check
// requires, for an assistant writing or fixing a spec.
func ContractText(capability string) string {
	c, ok := Lookup(capability)
	if !ok {
		return capability + ": unknown"
	}
	return fmt.Sprintf("%s: %s It takes %s and gives %s. Its check: %s",
		c.Name, c.About, ports(c.Inputs, true), ports(c.Outputs, false), c.Checks)
}

// ContractsMarkdown is the contracts as a Markdown table, for the docs.
func ContractsMarkdown() string {
	var b strings.Builder
	b.WriteString("| Contract | What it is | Takes | Gives | Checked for | Where it plugs in |\n")
	b.WriteString("|---|---|---|---|---|---|\n")
	for _, c := range ContractInfos() {
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s | %s |\n", c.Name, cell(c.About), cell(c.Takes),
			cell(c.Gives), cell(c.Checks), cell(c.Where))
	}
	return b.String()
}

// cell keeps a value from breaking a Markdown table row.
func cell(s string) string {
	return strings.NewReplacer("|", `\|`, "\n", " ").Replace(s)
}
