package temptool

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// createPipelineGrouped builds a pipeline-mode TempTool from the
// grouped-action arg map and registers it on the session. Wraps a
// multi-step sub-agent flow as a single LLM-callable tool. Two
// shapes: adaptive (pipeline_prompt drives an LLM sub-agent over
// pipeline_tools) or deterministic (pipeline_steps runs in order
// with no inner LLM). One of the two is required, unless the tool carries
// a whole saved pipeline (from_pipeline / pipeline_def). The returned fold,
// non-nil only for from_pipeline, is called once the tool is saved where it
// lives, and removes the original when nothing else uses it.
func createPipelineGrouped(args map[string]any, sess *ToolSession) (string, func() string, error) {
	if sess == nil {
		return "", nil, fmt.Errorf("requires a session")
	}
	name := strings.TrimSpace(StringArg(args, "name"))
	if name == "" {
		return "", nil, fmt.Errorf("name is required")
	}
	if err := checkNewToolName(sess, name); err != nil {
		return "", nil, err
	}
	desc := strings.TrimSpace(StringArg(args, "description"))
	if desc == "" {
		return "", nil, fmt.Errorf("description is required")
	}
	params, err := parseParamsArg(args["params"])
	if err != nil {
		return "", nil, fmt.Errorf("params: %w", err)
	}
	required := stringSliceArg(args["required"])
	if len(required) == 0 {
		for k := range params {
			required = append(required, k)
		}
	}
	prompt := strings.TrimSpace(StringArg(args, "pipeline_prompt"))
	steps := pipelineStepsFromArg(args["pipeline_steps"])
	if carried, ok, err := pipelineCopyFromArgs(args, sess); ok {
		if err != nil {
			return "", nil, err
		}
		if prompt != "" || len(steps) > 0 {
			return "", nil, fmt.Errorf("a tool carrying a whole pipeline (from_pipeline / pipeline_def) runs that pipeline: drop pipeline_prompt and pipeline_steps")
		}
		res, err := carried.register(name, desc, params, required)
		return res, carried.fold, err
	}
	if prompt == "" && len(steps) == 0 {
		return "", nil, fmt.Errorf("either pipeline_prompt (adaptive) or pipeline_steps (deterministic) is required for mode=\"pipeline\"")
	}
	if prompt != "" && len(steps) > 0 {
		return "", nil, fmt.Errorf("pipeline_prompt and pipeline_steps are mutually exclusive: pick one")
	}
	inner := stringSliceArg(args["pipeline_tools"])
	if len(inner) == 0 {
		return "", nil, fmt.Errorf("pipeline_tools must list at least one inner tool name")
	}
	// Every inner tool must be one the pipeline can actually call. A pipeline
	// naming a tool it cannot reach used to save, report itself registered,
	// and fail on its first run.
	var unreachable []string
	for _, n := range inner {
		if n == name {
			return "", nil, fmt.Errorf("pipeline_tools lists %q, the pipeline itself: a pipeline cannot call itself", n)
		}
		if !pipelineToolReachable(sess, n) {
			unreachable = append(unreachable, n)
		}
	}
	if len(unreachable) > 0 {
		return "", nil, fmt.Errorf("pipeline_tools names %v, which this pipeline cannot call: no custom tool or catalog tool has that name. Create the tool first, or fix the name (action=\"list\" shows your tools)", unreachable)
	}
	if len(steps) > 0 {
		allowed := map[string]bool{}
		for _, n := range inner {
			allowed[n] = true
		}
		for i, s := range steps {
			if !allowed[s.Tool] {
				return "", nil, fmt.Errorf("pipeline_steps[%d].tool %q is not in pipeline_tools %v: add it or pick a different tool", i, s.Tool, inner)
			}
		}
	}
	maxRounds := 0
	if v, ok := args["pipeline_max_rounds"]; ok {
		switch n := v.(type) {
		case float64:
			maxRounds = int(n)
		case int:
			maxRounds = n
		}
	}

	tool := &TempTool{
		Name:              name,
		Description:       desc,
		Params:            params,
		Required:          required,
		Mode:              TempToolModePipeline,
		PipelinePrompt:    prompt,
		PipelineSteps:     steps,
		PipelineTools:     inner,
		PipelineMaxRounds: maxRounds,
	}
	sess.RemoveTempTool(tool.Name)
	if err := sess.AppendTempTool(tool); err != nil {
		return "", nil, err
	}
	// Durable home for the unapproved pipeline tool — see persistUnapprovedTool.
	persistUnapprovedTool(sess, tool)
	shape := "adaptive (pipeline_prompt + pipeline_tools)"
	if len(steps) > 0 {
		shape = fmt.Sprintf("deterministic (%d steps)", len(steps))
	}
	return fmt.Sprintf("Pipeline tool %q registered (%s) for this session. Inner tools: %v. Dispatch by name with the declared params to verify the flow.", name, shape, inner), nil, nil
}

// pipelineStepsFromArg coerces the LLM-supplied pipeline_steps value
// into []PipelineStep. Accepts the native []any of objects shape;
// silently drops malformed entries instead of erroring so the LLM
// gets a clear "missing pipeline_steps" or step-mismatch message
// from the caller instead of a parse complaint.
func pipelineStepsFromArg(raw any) []PipelineStep {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]PipelineStep, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		tool, _ := m["tool"].(string)
		tool = strings.TrimSpace(tool)
		if tool == "" {
			continue
		}
		step := PipelineStep{Tool: tool}
		if name, ok := m["name"].(string); ok {
			step.Name = strings.TrimSpace(name)
		}
		if a, ok := m["args"].(map[string]any); ok {
			step.Args = a
		}
		out = append(out, step)
	}
	return out
}

// carriedPipeline is a whole pipeline a tool is about to keep as its own
// copy: from_pipeline names a saved one to take, pipeline_def is the copy
// itself (what an edit of such a tool sends back).
type carriedPipeline struct {
	sess *ToolSession
	copy []byte
	def  PipelineDef
	// fold removes the saved original once the tool holds it, when nothing
	// else uses it. Nil for a pipeline_def, which has no original.
	fold func() string
}

// pipelineCopyFromArgs reads from_pipeline or pipeline_def. ok is false when
// neither was given, so the caller goes on to the step and prompt forms.
func pipelineCopyFromArgs(args map[string]any, sess *ToolSession) (*carriedPipeline, bool, error) {
	ref := strings.TrimSpace(StringArg(args, "from_pipeline"))
	raw, hasDef := args["pipeline_def"]
	if ref == "" && (!hasDef || raw == nil) {
		return nil, false, nil
	}
	if ref != "" && hasDef && raw != nil {
		return nil, true, fmt.Errorf("from_pipeline and pipeline_def are two ways to give the same pipeline: pass one")
	}
	cp := &carriedPipeline{sess: sess}
	if ref != "" {
		if sess.PipelineSource == nil {
			return nil, true, fmt.Errorf("from_pipeline is not available here")
		}
		def, fold, err := sess.PipelineSource(ref)
		if err != nil {
			return nil, true, err
		}
		cp.def, cp.fold = def, fold
	} else {
		var b []byte
		switch v := raw.(type) {
		case string:
			b = []byte(v)
		default:
			b, _ = json.Marshal(v)
		}
		if err := json.Unmarshal(b, &cp.def); err != nil {
			return nil, true, fmt.Errorf("pipeline_def is not a pipeline: %v", err)
		}
	}
	if err := cp.def.Validate(); err != nil {
		return nil, true, fmt.Errorf("the pipeline does not run as written: %v", err)
	}
	b, err := pipelineCopyBytes(cp.def)
	if err != nil {
		return nil, true, err
	}
	cp.copy = b
	return cp, true, nil
}

// register saves the tool carrying the copy, then folds the original in.
// A tool with no declared params takes one "input", which is what the
// pipeline itself takes.
func (cp *carriedPipeline) register(name, desc string, params map[string]ToolParam, required []string) (string, error) {
	if len(params) == 0 {
		params = map[string]ToolParam{"input": {Type: "string", Description: "What the pipeline starts from."}}
		required = []string{"input"}
	}
	tool := &TempTool{
		Name:        name,
		Description: desc,
		Params:      params,
		Required:    required,
		Mode:        TempToolModePipeline,
		Pipeline:    cp.copy,
	}
	cp.sess.RemoveTempTool(tool.Name)
	if err := cp.sess.AppendTempTool(tool); err != nil {
		return "", err
	}
	persistUnapprovedTool(cp.sess, tool)
	msg := fmt.Sprintf("Pipeline tool %q registered for this session: it carries its own copy of the pipeline %q (%d stage(s)) and runs it when called.", name, cp.def.Name, len(cp.def.Stages))
	return msg + " To change the pipeline, update this tool with pipeline_def (action=\"get\" shows it).", nil
}

// pipelineCopyBytes is the copy a tool keeps of def: the portable recipe,
// with nothing that identifies the original, so it can never be mistaken
// for it or edited through it.
func pipelineCopyBytes(def PipelineDef) ([]byte, error) {
	cp := ExportPipeline(def)
	cp.ID = ""
	cp.OwningApp = ""
	cp.Previous = nil
	cp.Published = false
	return json.Marshal(cp)
}

// carriedPipelineOf decodes the pipeline a tool carries; ok is false when it
// carries none.
func carriedPipelineOf(tt *TempTool) (PipelineDef, bool, error) {
	if tt == nil || len(tt.Pipeline) == 0 {
		return PipelineDef{}, false, nil
	}
	var def PipelineDef
	if err := json.Unmarshal(tt.Pipeline, &def); err != nil {
		return PipelineDef{}, true, fmt.Errorf("the pipeline in tool %q does not read: %w", tt.Name, err)
	}
	return def, true, nil
}

// carriedPipelineInput is the text a tool's carried pipeline starts from. A
// pipeline takes one input; a tool takes named args. One arg is passed as
// its value, so a tool with a single "input" reads like the pipeline run
// directly; several are passed as "name: value" lines in name order.
func carriedPipelineInput(args map[string]any) string {
	if len(args) == 1 {
		for _, v := range args {
			return strings.TrimSpace(fmt.Sprint(v))
		}
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s: %v\n", k, args[k])
	}
	return strings.TrimSpace(b.String())
}
