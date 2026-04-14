package lcdata

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// NodeType identifies how a node is executed
type NodeType string

const (
	NodeTypeLLM       NodeType = "llm"
	NodeTypeSTT       NodeType = "stt"
	NodeTypeTTS       NodeType = "tts"
	NodeTypeCommand   NodeType = "command"
	NodeTypeDatabase  NodeType = "database"
	NodeTypeHTTP      NodeType = "http"
	NodeTypeTransform NodeType = "transform"
	NodeTypePipeline  NodeType = "pipeline"
	NodeTypeSearch    NodeType = "search"
	NodeTypeFile      NodeType = "file"
)

// FieldSchema defines a single input or output field
type FieldSchema struct {
	Type     string `json:"type"`
	Required bool   `json:"required,omitempty"`
	Default  any    `json:"default,omitempty"`
}

// rawNode is used for JSON parsing before post-processing
type rawNode struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Type        NodeType          `json:"type"`
	Provider    string            `json:"provider,omitempty"`
	Model       string            `json:"model,omitempty"`

	SystemPrompt     string  `json:"system_prompt,omitempty"`
	SystemPromptFile string  `json:"system_prompt_file,omitempty"`
	Temperature      float64 `json:"temperature,omitempty"`
	MaxTokens        int     `json:"max_tokens,omitempty"`
	Stream           bool    `json:"stream,omitempty"`
	Tools            []string       `json:"tools,omitempty"`
	StructuredOutput map[string]any `json:"structured_output,omitempty"`

	Language string `json:"language,omitempty"`
	VoiceID  string `json:"voice_id,omitempty"`

	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Timeout string            `json:"timeout,omitempty"`
	Env     map[string]string `json:"env,omitempty"`

	Connection string   `json:"connection,omitempty"`
	Driver     string   `json:"driver,omitempty"`
	Query      string   `json:"query,omitempty"`
	Params     []string `json:"params,omitempty"`

	Method    string            `json:"method,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      string            `json:"body,omitempty"`
	StripHTML bool              `json:"strip_html,omitempty"`

	SearchProvider string `json:"search_provider,omitempty"`
	SearchCount    int    `json:"search_count,omitempty"`

	Operation string `json:"operation,omitempty"`

	RetryCount int    `json:"retry_count,omitempty"`
	RetryDelay string `json:"retry_delay,omitempty"`

	Template string `json:"template,omitempty"`
	Steps    []Step `json:"steps,omitempty"`

	Input  map[string]FieldSchema `json:"input,omitempty"`
	Output json.RawMessage        `json:"output,omitempty"`
}

// Node represents a single executable unit loaded from a nodes/ directory
type Node struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Type        NodeType `json:"type"`

	Provider         string         `json:"provider,omitempty"`
	Model            string         `json:"model,omitempty"`
	SystemPrompt     string         `json:"system_prompt,omitempty"`
	SystemPromptFile string         `json:"system_prompt_file,omitempty"`
	Temperature      float64        `json:"temperature,omitempty"`
	MaxTokens        int            `json:"max_tokens,omitempty"`
	Stream           bool           `json:"stream,omitempty"`
	Tools            []string       `json:"tools,omitempty"`
	StructuredOutput map[string]any `json:"structured_output,omitempty"`

	Language string `json:"language,omitempty"`
	VoiceID  string `json:"voice_id,omitempty"`

	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Timeout string            `json:"timeout,omitempty"`
	Env     map[string]string `json:"env,omitempty"`

	Connection string   `json:"connection,omitempty"`
	Driver     string   `json:"driver,omitempty"`
	Query      string   `json:"query,omitempty"`
	Params     []string `json:"params,omitempty"`

	Method    string            `json:"method,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      string            `json:"body,omitempty"`
	StripHTML bool              `json:"strip_html,omitempty"`

	SearchProvider string `json:"search_provider,omitempty"`
	SearchCount    int    `json:"search_count,omitempty"`

	Operation string `json:"operation,omitempty"`

	RetryCount int    `json:"retry_count,omitempty"`
	RetryDelay string `json:"retry_delay,omitempty"`

	Template string `json:"template,omitempty"`
	Steps    []Step `json:"steps,omitempty"`

	Input  map[string]FieldSchema `json:"input,omitempty"`

	// Output schema — used by non-pipeline nodes and for API discovery
	OutputSchema map[string]FieldSchema `json:"output,omitempty"`

	// OutputTemplates — used by pipeline nodes to render the final output
	// from run context. Values are Go template strings like "{{.step_id.field}}"
	// Populated at load time from "output" JSON when values are template strings.
	OutputTemplates map[string]string `json:"-"`

	// Runtime-populated fields
	Directory        string `json:"-"`
	SystemPromptText string `json:"-"`
}

// Nodes is a collection of loaded nodes
type Nodes []*Node

// LoadNodes reads all node directories from the given path
func LoadNodes(nodesPath string) (Nodes, error) {
	entries, err := os.ReadDir(nodesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("nodes directory not found: %s", nodesPath)
		}
		return nil, fmt.Errorf("failed to read nodes directory: %w", err)
	}

	var nodes Nodes
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		dirPath := filepath.Join(nodesPath, entry.Name())
		configPath := filepath.Join(dirPath, entry.Name()+".json")

		data, err := os.ReadFile(configPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("failed to read node config %s: %w", configPath, err)
		}

		node, err := parseNodeJSON(data)
		if err != nil {
			return nil, fmt.Errorf("failed to parse node config %s: %w", configPath, err)
		}

		node.Directory = dirPath

		// Load system prompt file for LLM nodes
		if node.SystemPromptFile != "" {
			promptPath := filepath.Join(dirPath, node.SystemPromptFile)
			promptData, err := os.ReadFile(promptPath)
			if err != nil && !os.IsNotExist(err) {
				return nil, fmt.Errorf("failed to read system prompt %s: %w", promptPath, err)
			}
			node.SystemPromptText = string(promptData)
		}
		if node.SystemPrompt != "" {
			node.SystemPromptText = node.SystemPrompt
		}

		nodes = append(nodes, node)
	}

	return nodes, nil
}

// parseNodeJSON parses a node JSON, handling the dual-use of "output"
// as either map[string]FieldSchema (non-pipeline) or map[string]string templates (pipeline).
func parseNodeJSON(data []byte) (*Node, error) {
	var raw rawNode
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	node := &Node{
		Name:             raw.Name,
		Description:      raw.Description,
		Type:             raw.Type,
		Provider:         raw.Provider,
		Model:            raw.Model,
		SystemPrompt:     raw.SystemPrompt,
		SystemPromptFile: raw.SystemPromptFile,
		Temperature:      raw.Temperature,
		MaxTokens:        raw.MaxTokens,
		Stream:           raw.Stream,
		Tools:            raw.Tools,
		StructuredOutput: raw.StructuredOutput,
		Language:         raw.Language,
		VoiceID:          raw.VoiceID,
		Command:          raw.Command,
		Args:             raw.Args,
		Timeout:          raw.Timeout,
		Env:              raw.Env,
		Connection:       raw.Connection,
		Driver:           raw.Driver,
		Query:            raw.Query,
		Params:           raw.Params,
		Method:           raw.Method,
		URL:              raw.URL,
		Headers:          raw.Headers,
		Body:             raw.Body,
		StripHTML:        raw.StripHTML,
		SearchProvider:   raw.SearchProvider,
		SearchCount:      raw.SearchCount,
		Operation:        raw.Operation,
		RetryCount:       raw.RetryCount,
		RetryDelay:       raw.RetryDelay,
		Template:         raw.Template,
		Steps:            raw.Steps,
		Input:            raw.Input,
	}

	// Parse "output" field — try FieldSchema map first, then string template map
	if len(raw.Output) > 0 && string(raw.Output) != "null" {
		var fieldSchemas map[string]FieldSchema
		if err := json.Unmarshal(raw.Output, &fieldSchemas); err == nil {
			// Check if values look like FieldSchema (have a "type" key) or are raw strings
			looksLikeSchema := false
			for _, v := range fieldSchemas {
				if v.Type != "" {
					looksLikeSchema = true
					break
				}
			}
			if looksLikeSchema || node.Type != NodeTypePipeline {
				node.OutputSchema = fieldSchemas
			}
		}

		// For pipeline nodes, also try parsing as template strings
		if node.Type == NodeTypePipeline {
			var templates map[string]string
			if err := json.Unmarshal(raw.Output, &templates); err == nil {
				// Check if values contain template markers
				for _, v := range templates {
					if len(v) > 0 {
						node.OutputTemplates = templates
						break
					}
				}
			}
		}
	}

	return node, nil
}

// Get finds a node by name
func (nodes Nodes) Get(name string) (*Node, error) {
	for _, n := range nodes {
		if n.Name == name {
			return n, nil
		}
	}
	return nil, fmt.Errorf("node not found: %s", name)
}

// Validate checks all nodes for config errors
func (nodes Nodes) Validate() []error {
	var errs []error
	for _, n := range nodes {
		if err := n.validate(); err != nil {
			errs = append(errs, fmt.Errorf("node %s: %w", n.Name, err))
		}
	}
	return errs
}

func (n *Node) validate() error {
	if n.Name == "" {
		return fmt.Errorf("name is required")
	}
	if n.Type == "" {
		return fmt.Errorf("type is required")
	}
	switch n.Type {
	case NodeTypeLLM:
		if n.Provider == "" {
			return fmt.Errorf("provider is required for llm nodes")
		}
		if n.Model == "" {
			return fmt.Errorf("model is required for llm nodes")
		}
	case NodeTypeCommand:
		if n.Command == "" {
			return fmt.Errorf("command is required for command nodes")
		}
	case NodeTypeDatabase:
		if n.Connection == "" {
			return fmt.Errorf("connection is required for database nodes")
		}
		if n.Query == "" {
			return fmt.Errorf("query is required for database nodes")
		}
	case NodeTypeHTTP:
		if n.URL == "" {
			return fmt.Errorf("url is required for http nodes")
		}
	case NodeTypeSearch:
		if n.SearchProvider == "" {
			return fmt.Errorf("search_provider is required for search nodes")
		}
	case NodeTypeFile:
		if n.Operation == "" {
			return fmt.Errorf("operation is required for file nodes (read, write, append)")
		}
	case NodeTypePipeline:
		if len(n.Steps) == 0 {
			return fmt.Errorf("steps are required for pipeline nodes")
		}
	}
	return nil
}

// validateInputs checks that all required input fields are present and non-empty.
func validateInputs(node *Node, inputs map[string]any) error {
	if len(node.Input) == 0 {
		return nil
	}
	var missing []string
	for name, schema := range node.Input {
		if !schema.Required {
			continue
		}
		v, ok := inputs[name]
		if !ok || v == nil || v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required input field(s): %s", strings.Join(missing, ", "))
	}
	return nil
}

// Summary returns a lightweight description for list/discovery endpoints
func (n *Node) Summary() map[string]any {
	return map[string]any{
		"name":        n.Name,
		"description": n.Description,
		"type":        n.Type,
		"input":       n.Input,
		"output":      n.OutputSchema,
	}
}
