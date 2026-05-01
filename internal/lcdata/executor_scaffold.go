package lcdata

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func executeScaffold(
	ctx context.Context,
	node *Node,
	inputs map[string]any,
	env EnvironmentConfig,
	events chan<- Event,
) (map[string]any, error) {

	nodesPath := env.NodesPath
	if nodesPath == "" {
		nodesPath = "./nodes"
	}

	switch strings.ToLower(node.Operation) {
	case "create":
		return executeScaffoldCreate(ctx, inputs, nodesPath)
	case "delete":
		return executeScaffoldDelete(ctx, inputs, nodesPath)
	case "list":
		return executeScaffoldList(ctx, inputs, nodesPath)
	case "read":
		return executeScaffoldRead(ctx, inputs, nodesPath)
	case "run":
		return executeScaffoldRun(ctx, inputs, nodesPath, env, events)
	default:
		return nil, fmt.Errorf("unknown scaffold operation: %s (supported: create, delete, list, read, run)", node.Operation)
	}
}

// executeScaffoldCreate writes a new node directory and config to disk.
// Inputs:
//   - name (string, required): the node name — becomes the directory and file name
//   - config (string|object, required): the node JSON config; if a string it is
//     parsed as JSON, if an object it is marshalled back to JSON
//   - system_prompt (string, optional): written to system.md in the node dir when present
func executeScaffoldCreate(
	ctx context.Context,
	inputs map[string]any,
	nodesPath string,
) (map[string]any, error) {

	name := stringVal(inputs, "name")
	if name == "" {
		return nil, fmt.Errorf("scaffold create: input.name is required")
	}
	// Sanitise: no path separators
	if strings.ContainsAny(name, "/\\") {
		return nil, fmt.Errorf("scaffold create: name must not contain path separators")
	}

	// Resolve config JSON bytes
	var configJSON []byte
	switch v := inputs["config"].(type) {
	case string:
		configJSON = []byte(v)
	case map[string]any:
		var err error
		configJSON, err = json.MarshalIndent(v, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("scaffold create: failed to marshal config object: %w", err)
		}
	case nil:
		return nil, fmt.Errorf("scaffold create: input.config is required")
	default:
		return nil, fmt.Errorf("scaffold create: input.config must be a JSON string or object, got %T", v)
	}

	// Validate the config parses and passes node validation
	parsed, err := parseNodeJSON(configJSON)
	if err != nil {
		return nil, fmt.Errorf("scaffold create: invalid node config JSON: %w", err)
	}
	if err := parsed.validate(); err != nil {
		return nil, fmt.Errorf("scaffold create: node config validation failed: %w", err)
	}

	// Force name consistency: the node's "name" field must match the directory name
	if parsed.Name != name {
		return nil, fmt.Errorf("scaffold create: config name %q does not match requested name %q", parsed.Name, name)
	}

	nodeDir := filepath.Join(nodesPath, name)
	if err := os.MkdirAll(nodeDir, 0755); err != nil {
		return nil, fmt.Errorf("scaffold create: failed to create node directory %s: %w", nodeDir, err)
	}

	// Ensure pretty-printed JSON for readability
	var reindented map[string]any
	if err := json.Unmarshal(configJSON, &reindented); err == nil {
		if pretty, err := json.MarshalIndent(reindented, "", "  "); err == nil {
			configJSON = pretty
		}
	}

	configPath := filepath.Join(nodeDir, name+".json")
	if err := os.WriteFile(configPath, configJSON, 0644); err != nil {
		return nil, fmt.Errorf("scaffold create: failed to write node config: %w", err)
	}

	// Optional system prompt file
	systemPromptText := stringVal(inputs, "system_prompt")
	if systemPromptText != "" {
		promptPath := filepath.Join(nodeDir, "system.md")
		if err := os.WriteFile(promptPath, []byte(systemPromptText), 0644); err != nil {
			return nil, fmt.Errorf("scaffold create: failed to write system.md: %w", err)
		}
	}

	// Optional scripts — map of filename → content, written into the node directory.
	// Allows creating complete command nodes including their Python/shell scripts in one call.
	// e.g. {"my_script.py": "import sys\nprint('hello')\n"}
	// Accepts either map[string]any (direct) or a JSON string (from pipeline templates).
	var filesWritten []string
	if scriptsRaw, ok := inputs["scripts"]; ok {
		// Coerce JSON string to map if needed
		if s, ok := scriptsRaw.(string); ok && strings.TrimSpace(s) != "" && strings.TrimSpace(s) != "null" {
			var decoded map[string]any
			if err := json.Unmarshal([]byte(s), &decoded); err == nil {
				scriptsRaw = decoded
			}
		}
		if scriptsMap, ok := scriptsRaw.(map[string]any); ok {
			for filename, content := range scriptsMap {
				// Reject path traversal attempts
				if strings.ContainsAny(filename, "/\\") {
					return nil, fmt.Errorf("scaffold create: script filename %q must not contain path separators", filename)
				}
				contentStr, ok := content.(string)
				if !ok {
					return nil, fmt.Errorf("scaffold create: script %q content must be a string", filename)
				}
				scriptPath := filepath.Join(nodeDir, filename)
				if err := os.WriteFile(scriptPath, []byte(contentStr), 0755); err != nil {
					return nil, fmt.Errorf("scaffold create: failed to write script %s: %w", filename, err)
				}
				filesWritten = append(filesWritten, filename)
			}
		}
	}

	result := map[string]any{
		"name":    name,
		"path":    configPath,
		"created": true,
	}
	if len(filesWritten) > 0 {
		result["scripts"] = filesWritten
	}
	return result, nil
}

// executeScaffoldDelete removes a node directory from disk.
// Inputs:
//   - name (string, required): the node name to delete
func executeScaffoldDelete(
	ctx context.Context,
	inputs map[string]any,
	nodesPath string,
) (map[string]any, error) {

	name := stringVal(inputs, "name")
	if name == "" {
		return nil, fmt.Errorf("scaffold delete: input.name is required")
	}
	if strings.ContainsAny(name, "/\\") {
		return nil, fmt.Errorf("scaffold delete: name must not contain path separators")
	}

	nodeDir := filepath.Join(nodesPath, name)

	// Verify it looks like a node directory (has the expected JSON file) before removing
	configPath := filepath.Join(nodeDir, name+".json")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("scaffold delete: node %q not found at %s", name, configPath)
	}

	if err := os.RemoveAll(nodeDir); err != nil {
		return nil, fmt.Errorf("scaffold delete: failed to remove node directory: %w", err)
	}

	return map[string]any{
		"name":    name,
		"path":    nodeDir,
		"deleted": true,
	}, nil
}

// executeScaffoldList loads all nodes from the nodes directory and returns summaries.
// No required inputs.
func executeScaffoldList(
	ctx context.Context,
	inputs map[string]any,
	nodesPath string,
) (map[string]any, error) {

	nodes, err := LoadNodes(nodesPath)
	if err != nil {
		return nil, fmt.Errorf("scaffold list: %w", err)
	}

	summaries := make([]any, len(nodes))
	for i, n := range nodes {
		summaries[i] = n.Summary()
	}

	return map[string]any{
		"nodes": summaries,
		"count": len(nodes),
	}, nil
}

// executeScaffoldRead returns the raw JSON config for a single node.
// Inputs:
//   - name (string, required): the node name to read
func executeScaffoldRead(
	ctx context.Context,
	inputs map[string]any,
	nodesPath string,
) (map[string]any, error) {

	name := stringVal(inputs, "name")
	if name == "" {
		return nil, fmt.Errorf("scaffold read: input.name is required")
	}
	if strings.ContainsAny(name, "/\\") {
		return nil, fmt.Errorf("scaffold read: name must not contain path separators")
	}

	configPath := filepath.Join(nodesPath, name, name+".json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("scaffold read: node %q not found", name)
		}
		return nil, fmt.Errorf("scaffold read: failed to read node config: %w", err)
	}

	// Also decode for structured access
	var configObj map[string]any
	_ = json.Unmarshal(data, &configObj)

	return map[string]any{
		"name":   name,
		"path":   configPath,
		"config": string(data),
		"object": configObj,
	}, nil
}

// executeScaffoldRun loads a node fresh from disk and runs it with the given inputs.
// This lets builder_llm test a newly created node immediately after scaffold_create,
// getting real output or errors before the cap_build retry loop verifies it.
// Inputs:
//   - name (string, required): the node name to run
//   - input (object, optional): input key/value pairs for the node
func executeScaffoldRun(
	ctx context.Context,
	inputs map[string]any,
	nodesPath string,
	env EnvironmentConfig,
	events chan<- Event,
) (map[string]any, error) {

	name := stringVal(inputs, "name")
	if name == "" {
		return nil, fmt.Errorf("scaffold run: input.name is required")
	}
	if strings.ContainsAny(name, "/\\") {
		return nil, fmt.Errorf("scaffold run: name must not contain path separators")
	}

	// Reload nodes from disk so newly created nodes are included
	nodes, err := LoadNodes(nodesPath)
	if err != nil {
		return nil, fmt.Errorf("scaffold run: failed to load nodes: %w", err)
	}

	targetNode, err := nodes.Get(name)
	if err != nil {
		return nil, fmt.Errorf("scaffold run: node %q not found — was it created successfully?", name)
	}

	// Build test inputs from the "input" field
	testInputs := make(map[string]any)
	if raw, ok := inputs["input"]; ok {
		switch v := raw.(type) {
		case map[string]any:
			testInputs = v
		case string:
			// Accept JSON string too
			_ = json.Unmarshal([]byte(v), &testInputs)
		}
	}

	rc := NewRunContext("scaffold-run-"+name, testInputs)

	if targetNode.Type == NodeTypePipeline {
		output, _, err := executePipeline(ctx, targetNode, rc, nodes, env, events)
		if err != nil {
			return map[string]any{"error": err.Error(), "ok": false}, nil
		}
		return output, nil
	}

	output, err := executeLeafNode(ctx, targetNode, rc, env, events, nodes)
	if err != nil {
		return map[string]any{"error": err.Error(), "ok": false}, nil
	}
	return output, nil
}
