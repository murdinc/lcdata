package lcdata

import (
	"context"
	"fmt"
)

func executeTransform(
	ctx context.Context,
	node *Node,
	inputs map[string]any,
	rc *RunContext,
	events chan<- Event,
) (map[string]any, error) {

	if node.Template == "" {
		return nil, fmt.Errorf("transform node requires a template field")
	}

	result, err := rc.Render(node.Template)
	if err != nil {
		return nil, fmt.Errorf("transform template error: %w", err)
	}

	return map[string]any{
		"result": result,
	}, nil
}
