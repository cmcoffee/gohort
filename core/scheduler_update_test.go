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
