package lcdata

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// executePipeline runs all steps of a pipeline node in order,
// collecting results and handling switch/parallel/loop/map steps
func executePipeline(
	ctx context.Context,
	node *Node,
	rc *RunContext,
	nodes Nodes,
	env EnvironmentConfig,
	events chan<- Event,
) (map[string]any, []StepResult, error) {

	var allSteps []StepResult

	for _, step := range node.Steps {
		if err := ctx.Err(); err != nil {
			return nil, allSteps, err
		}

		steps, err := executeStep(ctx, step, rc, nodes, env, events, rc.RunID)
		allSteps = append(allSteps, steps...)
		if err != nil {
			return nil, allSteps, fmt.Errorf("step %q: %w", step.ID, err)
		}
	}

	// Render pipeline output from final context.
	// If OutputTemplates are defined, render them. Otherwise expose the full
	// run context snapshot so callers can access any step output directly.
	output := make(map[string]any)
	if len(node.OutputTemplates) > 0 {
		for k, tmpl := range node.OutputTemplates {
			v, err := rc.RenderValue(tmpl)
			if err != nil {
				return nil, allSteps, fmt.Errorf("output template %q: %w", k, err)
			}
			output[k] = v
		}
	} else {
		// No explicit output templates — expose the full context snapshot
		// so the caller has access to all step outputs
		output = rc.Snapshot()
	}

	return output, allSteps, nil
}

// executeStep dispatches a single pipeline step to the correct handler
func executeStep(
	ctx context.Context,
	step Step,
	rc *RunContext,
	nodes Nodes,
	env EnvironmentConfig,
	events chan<- Event,
	runID string,
) ([]StepResult, error) {

	switch {
	case step.Node != "":
		return executeSimpleStep(ctx, step, rc, nodes, env, events, runID)
	case step.Switch != "":
		return executeSwitchStep(ctx, step, rc, nodes, env, events, runID)
	case len(step.Parallel) > 0:
		return executeParallelStep(ctx, step, rc, nodes, env, events, runID)
	case step.Loop != nil:
		return executeLoopStep(ctx, step, rc, nodes, env, events, runID)
	case step.Map != nil:
		return executeMapStep(ctx, step, rc, nodes, env, events, runID)
	default:
		return nil, fmt.Errorf("step %q: no executable field (node/switch/parallel/loop/map)", step.ID)
	}
}

// executeSimpleStep runs a single node and writes its output to context
func executeSimpleStep(
	ctx context.Context,
	step Step,
	rc *RunContext,
	nodes Nodes,
	env EnvironmentConfig,
	events chan<- Event,
	runID string,
) ([]StepResult, error) {

	node, err := nodes.Get(step.Node)
	if err != nil {
		return nil, err
	}

	// Render input templates against current context
	renderedInput, err := rc.RenderMap(step.Input)
	if err != nil {
		return nil, fmt.Errorf("rendering inputs: %w", err)
	}

	// Merge rendered inputs into a child context so the node reads them as "input.*"
	childRC := rc.Clone()
	for k, v := range renderedInput {
		childRC.Set("input."+k, v)
	}

	started := time.Now()
	events <- Event{
		Event:     EventStepStarted,
		RunID:     runID,
		StepID:    step.ID,
		Node:      node.Name,
		Timestamp: started,
	}

	var output map[string]any
	var execErr error

	if node.Type == NodeTypePipeline {
		output, _, execErr = executePipeline(ctx, node, childRC, nodes, env, events)
	} else {
		output, execErr = executeLeafNode(ctx, node, childRC, env, events)
	}

	ended := time.Now()
	dur := ended.Sub(started).Milliseconds()

	result := StepResult{
		ID:         step.ID,
		Node:       node.Name,
		StartedAt:  started,
		EndedAt:    ended,
		DurationMS: dur,
	}

	if execErr != nil {
		result.Status = RunStatusFailed
		result.Error = execErr.Error()
		events <- Event{
			Event:      EventStepFailed,
			RunID:      runID,
			StepID:     step.ID,
			Node:       node.Name,
			Error:      execErr.Error(),
			DurationMS: dur,
			Timestamp:  ended,
		}
		return []StepResult{result}, execErr
	}

	// Write output to main context under step ID namespace
	rc.SetStepOutput(step.ID, output)

	result.Status = RunStatusCompleted
	result.Output = output
	events <- Event{
		Event:      EventStepCompleted,
		RunID:      runID,
		StepID:     step.ID,
		Node:       node.Name,
		Output:     output,
		DurationMS: dur,
		Timestamp:  ended,
	}

	return []StepResult{result}, nil
}

// executeSwitchStep evaluates a switch expression and runs the matching case
func executeSwitchStep(
	ctx context.Context,
	step Step,
	rc *RunContext,
	nodes Nodes,
	env EnvironmentConfig,
	events chan<- Event,
	runID string,
) ([]StepResult, error) {

	value, err := rc.Render(step.Switch)
	if err != nil {
		return nil, fmt.Errorf("switch expression: %w", err)
	}

	// Normalize: trim whitespace, then try to extract a string from JSON
	// e.g. '{"intent":"search"}' → "search", or '  search\n' → "search"
	value = normalizeSwitchValue(value)

	switchCase, ok := step.Cases[value]
	if !ok {
		switchCase, ok = step.Cases["default"]
		if !ok {
			return nil, fmt.Errorf("switch: no case for %q and no default", value)
		}
	}

	syntheticStep := Step{
		ID:    step.ID,
		Node:  switchCase.Node,
		Input: switchCase.Input,
	}
	return executeSimpleStep(ctx, syntheticStep, rc, nodes, env, events, runID)
}

// executeParallelStep runs all branches concurrently and merges results
func executeParallelStep(
	ctx context.Context,
	step Step,
	rc *RunContext,
	nodes Nodes,
	env EnvironmentConfig,
	events chan<- Event,
	runID string,
) ([]StepResult, error) {

	type branchResult struct {
		steps  []StepResult
		output map[string]any
		id     string
		err    error
	}

	results := make([]branchResult, len(step.Parallel))
	var wg sync.WaitGroup

	for i, branch := range step.Parallel {
		wg.Add(1)
		go func(idx int, b ParallelBranch) {
			defer wg.Done()

			syntheticStep := Step{
				ID:    step.ID + "." + b.ID,
				Node:  b.Node,
				Input: b.Input,
			}

			branchRC := rc.Clone()
			steps, err := executeSimpleStep(ctx, syntheticStep, branchRC, nodes, env, events, runID)

			output := make(map[string]any)
			if err == nil {
				snap := branchRC.Snapshot()
				parts := strings.Split(step.ID+"."+b.ID, ".")
				if branchData := lookupPath(snap, parts); branchData != nil {
					if m, ok := branchData.(map[string]any); ok {
						output = m
					}
				}
			}

			results[idx] = branchResult{
				steps:  steps,
				output: output,
				id:     b.ID,
				err:    err,
			}
		}(i, branch)
	}
	wg.Wait()

	var allSteps []StepResult
	mergedOutput := make(map[string]any)

	for _, res := range results {
		allSteps = append(allSteps, res.steps...)
		if res.err != nil {
			return allSteps, fmt.Errorf("parallel branch %q: %w", res.id, res.err)
		}
		// Nest output under branch ID: step.ID.branch_id.field
		mergedOutput[res.id] = res.output
	}

	// Write merged parallel output under the step ID
	rc.SetStepOutput(step.ID, mergedOutput)

	return allSteps, nil
}

// executeLoopStep repeats steps until the until condition is true or max_iterations reached
func executeLoopStep(
	ctx context.Context,
	step Step,
	rc *RunContext,
	nodes Nodes,
	env EnvironmentConfig,
	events chan<- Event,
	runID string,
) ([]StepResult, error) {

	cfg := step.Loop
	var allSteps []StepResult

	for i := 0; i < cfg.MaxIterations; i++ {
		if err := ctx.Err(); err != nil {
			return allSteps, err
		}

		events <- Event{
			Event:     EventLoopIteration,
			RunID:     runID,
			StepID:    step.ID,
			Iteration: i + 1,
			Timestamp: time.Now(),
		}

		for _, innerStep := range cfg.Steps {
			steps, err := executeStep(ctx, innerStep, rc, nodes, env, events, runID)
			allSteps = append(allSteps, steps...)
			if err != nil {
				return allSteps, fmt.Errorf("loop iteration %d step %q: %w", i+1, innerStep.ID, err)
			}
		}

		if cfg.Until != "" {
			done, err := rc.RenderBool(cfg.Until)
			if err != nil {
				return allSteps, fmt.Errorf("loop until expression: %w", err)
			}
			if done {
				break
			}
		}
	}

	return allSteps, nil
}

// executeMapStep fans out a node call over each item in a slice
func executeMapStep(
	ctx context.Context,
	step Step,
	rc *RunContext,
	nodes Nodes,
	env EnvironmentConfig,
	events chan<- Event,
	runID string,
) ([]StepResult, error) {

	cfg := step.Map

	items, err := rc.RenderSlice(cfg.Over)
	if err != nil {
		return nil, fmt.Errorf("map over: %w", err)
	}

	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 1
	}

	type itemResult struct {
		idx    int
		steps  []StepResult
		output map[string]any
		err    error
	}

	results := make([]itemResult, len(items))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, item := range items {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, it any) {
			defer wg.Done()
			defer func() { <-sem }()

			// Build input map with item substituted for the "as" variable
			renderedInput := make(map[string]string, len(cfg.Input))
			for k, v := range cfg.Input {
				renderedInput[k] = v
			}

			// Clone context and set the current item under cfg.As
			itemRC := rc.Clone()
			itemRC.Set(cfg.As, it)

			syntheticStep := Step{
				ID:    fmt.Sprintf("%s.%d", step.ID, idx),
				Node:  cfg.Node,
				Input: renderedInput,
			}

			steps, err := executeSimpleStep(ctx, syntheticStep, itemRC, nodes, env, events, runID)

			output := make(map[string]any)
			if err == nil {
				snap := itemRC.Snapshot()
				parts := strings.Split(fmt.Sprintf("%s.%d", step.ID, idx), ".")
				if v := lookupPath(snap, parts); v != nil {
					if m, ok := v.(map[string]any); ok {
						output = m
					}
				}
			}

			results[idx] = itemResult{idx: idx, steps: steps, output: output, err: err}

			events <- Event{
				Event:     EventMapProgress,
				RunID:     runID,
				StepID:    step.ID,
				Progress:  idx + 1,
				Total:     len(items),
				Timestamp: time.Now(),
			}
		}(i, item)
	}
	wg.Wait()

	var allSteps []StepResult
	collected := make([]any, len(items))

	for _, res := range results {
		allSteps = append(allSteps, res.steps...)
		if res.err != nil {
			return allSteps, fmt.Errorf("map item %d: %w", res.idx, res.err)
		}
		collected[res.idx] = res.output
	}

	rc.Set(cfg.CollectAs, collected)

	return allSteps, nil
}

// normalizeSwitchValue trims whitespace and, if the value looks like JSON,
// tries to extract a meaningful string. This handles LLMs that return
// {"intent": "search"} or "  search\n" when asked to classify.
func normalizeSwitchValue(v string) string {
	v = strings.TrimSpace(v)

	// Try JSON object — look for a single string value
	if strings.HasPrefix(v, "{") {
		var m map[string]any
		if err := json.Unmarshal([]byte(v), &m); err == nil {
			// Common key names for classification outputs
			for _, key := range []string{"intent", "value", "category", "class", "result", "label"} {
				if val, ok := m[key]; ok {
					if s, ok := val.(string); ok {
						return strings.TrimSpace(s)
					}
				}
			}
			// If only one key, use its value
			if len(m) == 1 {
				for _, val := range m {
					if s, ok := val.(string); ok {
						return strings.TrimSpace(s)
					}
				}
			}
		}
	}

	// Try stripping quotes
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		return v[1 : len(v)-1]
	}

	return v
}
