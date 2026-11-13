package servitor

import (
	"reflect"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The form writes a rule string the gate reads as it reads any rule, and a
// rule it wrote reads back into the same choices; it says what the rule
// allows in words.
func TestTheRuleFormWritesAndReadsBack(t *testing.T) {
	f := ruleForm{Program: "/opt/tool.py", Sudo: true, Flags: "only",
		Allowed: []ruleFlag{{Name: "--check"}, {Name: "--level", Value: "number"}, {Name: "--out", Value: "any"}}}
	rule, err := composeRule(f)
	if err != nil || rule != "sudo /opt/tool.py <flags:--check,--level=#,--out=*>" {
		t.Fatalf("composed %q, %v", rule, err)
	}
	back, ok := parseRuleForm(rule)
	if !ok || !reflect.DeepEqual(back, f) {
		t.Errorf("read back %+v, want %+v", back, f)
	}
	if got := describeRule(rule); got != "`/opt/tool.py`, with only these flags, in any order: --check, --level <number>, --out <value> (with or without sudo)" {
		t.Errorf("described %q", got)
	}
	subs := ruleForm{Program: "/opt/tool.py", Subcommands: []string{"report", "status"}, Flags: "any"}
	rule, err = composeRule(subs)
	if err != nil || rule != "/opt/tool.py <word:report,status> <args>" {
		t.Fatalf("with subcommands: %q, %v", rule, err)
	}
	if back, ok := parseRuleForm(rule); !ok || !reflect.DeepEqual(back, subs) {
		t.Errorf("read back %+v", back)
	}
	if got := describeRule(rule); got != "`/opt/tool.py` followed by report or status, with anything after it" {
		t.Errorf("described %q", got)
	}
	if _, ok := parseRuleForm("tail -n <number> /var/log/*.log | grep x"); ok {
		t.Error("a pipeline read as a form")
	}
}

// Only the listed flags, in any order and any subset, each with a value
// where it takes one: after = or as the next word, and the next word only for
// a flag that takes a value. Anything else is refused.
func TestOnlyTheListedFlags(t *testing.T) {
	rule := "sudo /opt/tool.py <flags:--check,--level=#,--out=*>"
	for _, cmd := range []string{
		"sudo /opt/tool.py",
		"sudo /opt/tool.py --check",
		"/opt/tool.py --level=3 --check",
		"sudo -n /opt/tool.py --level 3 --out=/tmp/x.json",
		"sudo /opt/tool.py --out /tmp/x.json --check",
	} {
		if !matchAllowRule(rule, cmd) {
			t.Errorf("%q does not cover %q", rule, cmd)
		}
	}
	for _, cmd := range []string{
		"sudo /opt/tool.py --verbose",
		"sudo /opt/tool.py --check=1",
		"sudo /opt/tool.py --level=abc",
		"sudo /opt/tool.py --level",
		"sudo /opt/tool.py --level --check",
		"sudo /opt/tool.py --out=../../etc/passwd",
		"sudo /opt/tool.py --check report",
		"sudo /opt/tool.py --out $(id)",
		"sudo /opt/tool.py --check | sh",
	} {
		if matchAllowRule(rule, cmd) {
			t.Errorf("%q let through %q", rule, cmd)
		}
	}
}

// A subcommand from a list: one of those words, exactly, under sudo too.
func TestASubcommandFromAList(t *testing.T) {
	rule := "sudo /opt/tool.py <word:report,status> <flags>"
	for _, cmd := range []string{"sudo /opt/tool.py report --x", "sudo /opt/tool.py status", "/opt/tool.py status -v"} {
		if !matchAllowRule(rule, cmd) {
			t.Errorf("%q does not cover %q", rule, cmd)
		}
	}
	for _, cmd := range []string{"sudo /opt/tool.py delete", "sudo /opt/tool.py", "sudo /opt/tool.py 'report'", "sudo /opt/other.py report"} {
		if matchAllowRule(rule, cmd) {
			t.Errorf("%q let through %q", rule, cmd)
		}
	}
}

// What the form refuses: a program it cannot take, a list with no flags, a
// flag carrying its own value, a subcommand that is not one word.
func TestTheRuleFormRefusesWhatItCannotTake(t *testing.T) {
	for name, f := range map[string]ruleForm{
		"no program":            {Flags: "any"},
		"a program with spaces": {Program: "/opt/my tool.py", Flags: "any"},
		"a pattern":             {Program: "/opt/*.py", Flags: "any"},
		"sudo as the program":   {Program: "sudo", Flags: "any"},
		"a path out with ..":    {Program: "/opt/../bin/sh", Flags: "any"},
		"only, with none":       {Program: "/opt/tool.py", Flags: "only"},
		"a flag with a value":   {Program: "/opt/tool.py", Flags: "only", Allowed: []ruleFlag{{Name: "--level=3"}}},
		"a subcommand path":     {Program: "/opt/tool.py", Subcommands: []string{"../x"}, Flags: "none"},
	} {
		if rule, err := composeRule(f); err == nil {
			t.Errorf("%s: composed %q", name, rule)
		}
	}
}

// The form adds a rule and edits one, keeping its program; the list says
// what each rule allows and gives the form back for one that fits it.
func TestTheRuleFormInPermissions(t *testing.T) {
	T := &Servitor{}
	T.DB = &DBase{Store: kvlite.MemStore()}
	udb := &DBase{Store: kvlite.MemStore()}
	udb.Set(applianceTable, "box-1", Appliance{ID: "box-1", Name: "web-01"})
	call := &jsonapi.Call{User: "alice", UDB: udb}
	form := ruleForm{Program: "/opt/tool.py", Sudo: true, Flags: "any"}
	if _, err := T.handleAlwaysAllowedSave(call, alwaysAllowedReq{ApplianceID: "box-1", Add: true, Form: &form}); err != nil {
		t.Fatalf("adding: %v", err)
	}
	list := T.listAlwaysAllowed("alice", udb)
	if len(list) != 1 || list[0].Command != "sudo /opt/tool.py <args>" || list[0].Form == nil || !strings.Contains(list[0].Label, "with anything after it") {
		t.Fatalf("listed %+v", list)
	}
	narrow := ruleForm{Program: "/opt/tool.py", Sudo: true, Flags: "only", Allowed: []ruleFlag{{Name: "--check"}}}
	if _, err := T.handleAlwaysAllowedSave(call, alwaysAllowedReq{ApplianceID: "box-1", Command: list[0].Command, Form: &narrow}); err != nil {
		t.Fatalf("editing: %v", err)
	}
	if !commandAlwaysAllowed(udb, "box-1", "sudo /opt/tool.py --check") || commandAlwaysAllowed(udb, "box-1", "sudo /opt/tool.py --force") {
		t.Error("the edited rule is not the one in force")
	}
	other := ruleForm{Program: "/opt/other.py", Flags: "any"}
	if _, err := T.handleAlwaysAllowedSave(call, alwaysAllowedReq{ApplianceID: "box-1", Command: "sudo /opt/tool.py <flags:--check>", Form: &other}); err == nil {
		t.Error("an edit changed the rule's program")
	}
}
