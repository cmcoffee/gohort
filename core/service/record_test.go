package service

import (
	"context"
	"errors"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A connection's record is its own: deleted, it goes, and one made again
// under the same name starts with none of the one before's calls, failures
// or health.
func TestARecordBelongsToItsConnection(t *testing.T) {
	world(t)
	install(t, "vec", embedSpec("m1"))
	noteCall(context.Background(), "vec", "embed.text", outcome{}, errors.New("the server refused"))
	updateRecord("vec", func(r *Record) { r.Health = &HealthCheck{OK: false, Message: "down"} })
	if r := RecordOf("vec"); r.Capabilities["embed.text"] == nil || r.Capabilities["embed.text"].LastError == "" || r.Health == nil {
		t.Fatalf("the record: %+v", r)
	}
	if err := RemoveConnector(RootDB, "vec"); err != nil {
		t.Fatal(err)
	}
	install(t, "vec", embedSpec("m1"))
	if r := RecordOf("vec"); len(r.Capabilities) != 0 || r.Health != nil {
		t.Errorf("made again, it inherited: %+v", r)
	}
	if keys := RootDB.Keys(recordsTable); len(keys) != 0 {
		t.Errorf("records kept: %v", keys)
	}
	// Deleted another way, its record left behind is still not the new
	// one's: a record is the connection's, by its id.
	noteCall(context.Background(), "vec", "embed.text", outcome{}, errors.New("again"))
	if err := DeleteConnector(RootDB, "vec"); err != nil {
		t.Fatal(err)
	}
	install(t, "vec", embedSpec("m1"))
	if r := RecordOf("vec"); len(r.Capabilities) != 0 {
		t.Errorf("made again after a plain delete, it inherited: %+v", r)
	}
}
