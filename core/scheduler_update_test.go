package core

import (
	"encoding/json"
	"testing"
	"time"
)

// TestUpdateScheduledTaskPayload: the pre-arm pattern's write-back — update a
// still-queued task's payload in place, and refuse (false) once the task has
// been consumed so a caller can't resurrect an already-fired occurrence.
func TestUpdateScheduledTaskPayload(t *testing.T) {
	db := memDB(t)
	schedDBMu.Lock()
	prev := schedDB
	schedDB = db
	schedDBMu.Unlock()
	defer func() {
		schedDBMu.Lock()
		schedDB = prev
		schedDBMu.Unlock()
	}()

	id, err := ScheduleTask("test.prearm", map[string]int{"fires": 1}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("ScheduleTask: %v", err)
	}
	if !UpdateScheduledTaskPayload(id, map[string]int{"fires": 2}) {
		t.Fatal("update of a queued task reported false")
	}
	found := false
	for _, task := range ListScheduledTasks("test.prearm") {
		if task.ID != id {
			continue
		}
		found = true
		var p map[string]int
		if jerr := json.Unmarshal(task.Payload, &p); jerr != nil || p["fires"] != 2 {
			t.Fatalf("payload not updated in place: %s (err=%v)", task.Payload, jerr)
		}
	}
	if !found {
		t.Fatal("updated task missing from the queue")
	}

	UnscheduleTask(id)
	if UpdateScheduledTaskPayload(id, map[string]int{"fires": 3}) {
		t.Fatal("update of a consumed task must report false, not re-create it")
	}
	if got := len(ListScheduledTasks("test.prearm")); got != 0 {
		t.Fatalf("consumed task resurrected: %d tasks in queue", got)
	}
}

// TestRescheduleTaskAt: the other half of the pre-arm write-back. The next
// occurrence is armed BEFORE a long fire, so anything the fire learns about
// WHEN it should run arrives after the entry exists (docs/objective-pacing.md).
// Same refusal contract as the payload updater: a consumed task is not moved
// and must not be re-created.
func TestRescheduleTaskAt(t *testing.T) {
	db := memDB(t)
	schedDBMu.Lock()
	prev := schedDB
	schedDB = db
	schedDBMu.Unlock()
	defer func() {
		schedDBMu.Lock()
		schedDB = prev
		schedDBMu.Unlock()
	}()

	id, err := ScheduleTask("test.pacing", map[string]int{"n": 1}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("ScheduleTask: %v", err)
	}
	want := time.Now().Add(6 * time.Hour).UTC().Truncate(time.Second)
	if !RescheduleTaskAt(id, want) {
		t.Fatal("moving a queued task reported false")
	}
	moved := false
	for _, task := range ListScheduledTasks("test.pacing") {
		if task.ID != id {
			continue
		}
		moved = true
		got, perr := time.Parse(time.RFC3339, task.RunAt)
		if perr != nil {
			t.Fatalf("RunAt is not RFC3339 after the move: %q (%v)", task.RunAt, perr)
		}
		if !got.Equal(want) {
			t.Fatalf("task did not move: RunAt %s, wanted %s", got, want)
		}
		var p map[string]int
		if jerr := json.Unmarshal(task.Payload, &p); jerr != nil || p["n"] != 1 {
			t.Fatalf("moving a task must not disturb its payload: %s (err=%v)", task.Payload, jerr)
		}
	}
	if !moved {
		t.Fatal("moved task missing from the queue")
	}

	UnscheduleTask(id)
	if RescheduleTaskAt(id, time.Now().Add(time.Hour)) {
		t.Fatal("moving a consumed task must report false, not re-create it")
	}
	if got := len(ListScheduledTasks("test.pacing")); got != 0 {
		t.Fatalf("consumed task resurrected: %d tasks in queue", got)
	}
}
