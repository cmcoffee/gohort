package ui

// A page hands a topic to a pipeline page as ?<start_param>=<topic>: the
// topic field fills and focus goes to Start. It must not submit: a link
// anyone can write would otherwise launch a run for whoever opened it.

import (
	"strings"
	"testing"
)

func TestStartParamFillsWithoutSubmitting(t *testing.T) {
	b, err := PipelinePanel{StartParam: "start"}.MarshalJSON()
	if err != nil || !strings.Contains(string(b), `"start_param":"start"`) {
		t.Fatalf("StartParam does not reach the runtime: %s %v", b, err)
	}
	js := readRuntimeFile(t, "40_pipeline_panel.js")
	i := strings.Index(js, "var startVal = (!sid && cfg.start_param)")
	if i < 0 {
		t.Fatal("the start_param handling is gone")
	}
	block := js[i:]
	if end := strings.Index(block, "} catch (_) {}"); end > 0 {
		block = block[:end]
	}
	if !strings.Contains(block, "startInput.value = startVal;") || !strings.Contains(block, "submitBtn.focus()") {
		t.Error("the handed-over topic should fill the field and focus Start")
	}
	if strings.Contains(block, "doSubmit(") {
		t.Error("a start_param link must not submit the run by itself")
	}
}
