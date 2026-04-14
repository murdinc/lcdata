package lcdata

import (
	"context"
	"fmt"
)

// executeLeafNode dispatches a non-pipeline node to its type-specific executor.
// rc is passed to all executors so they can read context values and emit events
// with the correct run ID.
func executeLeafNode(
	ctx context.Context,
	node *Node,
	rc *RunContext,
	env EnvironmentConfig,
	events chan<- Event,
) (map[string]any, error) {

	// Collect input values from context (populated by runner from rendered templates)
	inputs := make(map[string]any)
	snap := rc.Snapshot()
	if inputMap, ok := snap["input"]; ok {
		if m, ok := inputMap.(map[string]any); ok {
			inputs = m
		}
	}

	switch node.Type {
	case NodeTypeLLM:
		return executeLLM(ctx, node, rc.RunID, inputs, env, events)
	case NodeTypeSTT:
		return executeSTT(ctx, node, inputs, env, events)
	case NodeTypeTTS:
		return executeTTS(ctx, node, inputs, env, events)
	case NodeTypeCommand:
		return executeCommand(ctx, node, inputs, rc, env, events)
	case NodeTypeDatabase:
		return executeDatabase(ctx, node, inputs, env, events)
	case NodeTypeHTTP:
		return executeHTTP(ctx, node, inputs, rc, env, events)
	case NodeTypeTransform:
		return executeTransform(ctx, node, inputs, rc, events)
	case NodeTypeSearch:
		return executeSearch(ctx, node, inputs, env, events)
	case NodeTypeFile:
		return executeFile(ctx, node, inputs, events)
	default:
		return nil, fmt.Errorf("unknown node type: %s", node.Type)
	}
}
