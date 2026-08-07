// The review surface. Its whole job is that a person reads the command before
// agreeing to it, so the tests are about what the row carries and what order
// the rows arrive in.
package servitor

import (
	"encoding/json"
	"strings"
	"testing"
)

// The command must be a first-class field on the row. An approval surface that
// shows a name and a risk badge and hides the command is one that gets clicked
// through.
func TestRowCarriesTheCommandItself(t *testing.T) {
	raw, err := json.Marshal(applianceToolRow{
		Name: "restart_web", Command: "sudo systemctl restart nginx", ApplianceID: "lab-box",
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["command"] != "sudo systemctl restart nginx" {
		t.Errorf("the row must carry the exact command, got %v", got["command"])
	}
	// The row actions template these two into their URLs; without them under
	// exactly these names the buttons build a URL with empty parameters.
	for _, field := range []string{"appliance_id", "name"} {
		if v, ok := got[field]; !ok || v == "" {
			t.Errorf("row must expose %q for the action URLs, got %v", field, v)
		}
	}
}

// Undecided first — those are the reason anyone opens the page.
func TestUndecidedRowsSortFirst(t *testing.T) {
	rows := []applianceToolRow{
		{Name: "b_approved", Appliance: "Lab", Approved: true},
		{Name: "a_pending", Appliance: "Lab", Approved: false},
		{Name: "c_pending", Appliance: "Alpha", Approved: false},
	}
	sortRows(rows)
	if rows[0].Approved || rows[1].Approved {
		t.Fatalf("pending rows must lead, got %+v", rows)
	}
	// Then by machine, so a fleet reads as an inventory rather than a jumble.
	if rows[0].Appliance != "Alpha" {
		t.Errorf("within pending, sort by machine; got %q first", rows[0].Appliance)
	}
	if !rows[2].Approved {
		t.Error("approved rows should follow")
	}
}

// One fact, one field: a separate "pending" flag would be a second copy of the
// same truth and free to disagree with it.
func TestApprovalStateIsASingleField(t *testing.T) {
	raw, _ := json.Marshal(applianceToolRow{Approved: false, Status: "pending"})
	if strings.Contains(string(raw), `"pending":`) {
		t.Errorf("approval state should be one boolean, got %s", raw)
	}
}

// The confirm text has to say what approval actually grants, or it is a speed
// bump rather than a decision.
func TestApproveConfirmStatesTheConsequence(t *testing.T) {
	var approve, revoke, del bool
	tbl := capabilitiesSectionActions()
	for _, a := range tbl {
		switch a.Label {
		case "Approve":
			approve = true
			if !strings.Contains(a.Confirm, "whenever it decides to") {
				t.Errorf("approve confirm should state the standing nature, got %q", a.Confirm)
			}
			if a.HideIf != "approved" {
				t.Errorf("approve should not render on an approved row, got %q", a.HideIf)
			}
		case "Revoke":
			revoke = true
			if a.OnlyIf != "approved" {
				t.Errorf("revoke should render only on approved rows, got %q", a.OnlyIf)
			}
		case "Delete":
			del = true
			if a.Variant != "danger" {
				t.Error("delete should read as destructive")
			}
		}
	}
	if !approve || !revoke || !del {
		t.Errorf("expected approve/revoke/delete actions, got approve=%v revoke=%v delete=%v", approve, revoke, del)
	}
}
