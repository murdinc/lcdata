package lcdata

import (
	"context"
	"fmt"
	"time"
)

// executeLeafNode dispatches a non-pipeline node to its type-specific executor.
// rc is passed to all executors so they can read context values and emit events
// with the correct run ID. allNodes is used by LLM nodes that invoke tools.
func executeLeafNode(
	ctx context.Context,
	node *Node,
	rc *RunContext,
	env EnvironmentConfig,
	events chan<- Event,
	allNodes ...Nodes,
) (map[string]any, error) {
	var nodes Nodes
	if len(allNodes) > 0 {
		nodes = allNodes[0]
	}

	// Collect input values from context (populated by runner from rendered templates)
	inputs := make(map[string]any)
	snap := rc.Snapshot()
	if inputMap, ok := snap["input"]; ok {
		if m, ok := inputMap.(map[string]any); ok {
			inputs = m
		}
	}

	// retryable wraps an executor call with the node's retry config.
	retryable := func(fn func() (map[string]any, error)) (map[string]any, error) {
		if node.RetryCount <= 0 {
			return fn()
		}
		return withRetry(ctx, node.RetryCount, node.RetryDelay, func(attempt int, err error) {
			events <- Event{
				Event:     EventRetry,
				RunID:     rc.RunID,
				StepID:    node.Name,
				Node:      node.Name,
				Error:     err.Error(),
				Iteration: attempt,
				Timestamp: time.Now(),
			}
		}, fn)
	}

	switch node.Type {
	case NodeTypeLLM:
		return retryable(func() (map[string]any, error) {
			return executeLLM(ctx, node, rc.RunID, inputs, env, events, nodes)
		})
	case NodeTypeSTT:
		return executeSTT(ctx, node, inputs, env, events)
	case NodeTypeTTS:
		return executeTTS(ctx, node, inputs, env, events)
	case NodeTypeCommand:
		return executeCommand(ctx, node, inputs, rc, env, events)
	case NodeTypeDatabase:
		return executeDatabase(ctx, node, inputs, env, events)
	case NodeTypeHTTP:
		return retryable(func() (map[string]any, error) {
			return executeHTTP(ctx, node, inputs, rc, env, events)
		})
	case NodeTypeTransform:
		return executeTransform(ctx, node, inputs, rc, events)
	case NodeTypeSearch:
		return retryable(func() (map[string]any, error) {
			return executeSearch(ctx, node, inputs, env, events)
		})
	case NodeTypeFile:
		return executeFile(ctx, node, inputs, events)
	case NodeTypeVector:
		return retryable(func() (map[string]any, error) {
			return executeVector(ctx, node, inputs, env, events)
		})
	case NodeTypeEmbedding:
		return retryable(func() (map[string]any, error) {
			return executeEmbedding(ctx, node, inputs, env, events)
		})
	case NodeTypeScaffold:
		return executeScaffold(ctx, node, inputs, env, events)
	default:
		return nil, fmt.Errorf("unknown node type: %s", node.Type)
	}
}
