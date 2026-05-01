package lcdata

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

func executeLLM(
	ctx context.Context,
	node *Node,
	runID string,
	inputs map[string]any,
	env EnvironmentConfig,
	events chan<- Event,
	allNodes Nodes,
) (map[string]any, error) {

	// Render template expressions in the system prompt.
	// inputs are passed as dot (.) so {{.available_nodes}}, {{now}}, etc. all work.
	if strings.Contains(node.SystemPromptText, "{{") {
		node = cloneNodeWithPrompt(node, RenderSystemPrompt(node.SystemPromptText, inputs))
	}

	switch strings.ToLower(node.Provider) {
	case "anthropic":
		return executeLLMAnthropic(ctx, node, runID, inputs, env, events, allNodes)
	case "ollama":
		return executeLLMOllama(ctx, node, runID, inputs, env, events, allNodes)
	case "openai":
		return executeLLMOpenAI(ctx, node, runID, inputs, env, events)
	default:
		return nil, fmt.Errorf("unknown LLM provider: %s", node.Provider)
	}
}

// cloneNodeWithPrompt returns a shallow copy of node with SystemPromptText replaced.
// This avoids mutating the shared node registry.
func cloneNodeWithPrompt(n *Node, prompt string) *Node {
	copy := *n
	copy.SystemPromptText = prompt
	return &copy
}

// executeLLMAnthropic calls the Anthropic Claude API
func executeLLMAnthropic(
	ctx context.Context,
	node *Node,
	runID string,
	inputs map[string]any,
	env EnvironmentConfig,
	events chan<- Event,
	allNodes Nodes,
) (map[string]any, error) {

	if env.AnthropicKey == "" {
		return nil, fmt.Errorf("anthropicKey not set in environment config (also checks ANTHROPIC_API_KEY)")
	}

	message := stringVal(inputs, "message")
	if message == "" {
		return nil, fmt.Errorf("input.message is required for llm nodes")
	}

	client := anthropic.NewClient(option.WithAPIKey(env.AnthropicKey))

	maxTokens := int64(node.MaxTokens)
	if maxTokens == 0 {
		maxTokens = 4096
	}

	// Build messages — include history if provided
	var messages []anthropic.MessageParam
	if history, ok := inputs["history"]; ok {
		if histSlice, ok := history.([]any); ok {
			histSlice = trimHistory(histSlice, node.MaxHistory)
			for _, h := range histSlice {
				if hMap, ok := h.(map[string]any); ok {
					role := stringVal(hMap, "role")
					content := stringVal(hMap, "content")
					switch role {
					case "user":
						messages = append(messages, anthropic.NewUserMessage(
							anthropic.NewTextBlock(content),
						))
					case "assistant":
						messages = append(messages, anthropic.NewAssistantMessage(
							anthropic.NewTextBlock(content),
						))
					}
				}
			}
		}
	}
	messages = append(messages, anthropic.NewUserMessage(
		anthropic.NewTextBlock(message),
	))

	params := anthropic.MessageNewParams{
		Model:     anthropic.F(anthropic.Model(node.Model)),
		MaxTokens: anthropic.F(maxTokens),
		Messages:  anthropic.F(messages),
	}

	if node.Temperature > 0 {
		params.Temperature = anthropic.F(node.Temperature)
	}

	// System prompt
	if node.SystemPromptText != "" {
		params.System = anthropic.F([]anthropic.TextBlockParam{
			anthropic.NewTextBlock(node.SystemPromptText),
		})
	}

	// Build tool definitions from referenced nodes
	toolDefs := buildAnthropicToolDefs(node.Tools, allNodes)
	if len(toolDefs) > 0 {
		params.Tools = anthropic.F(toolDefs)
	}

	if node.Stream {
		return executeLLMAnthropicStreamWithTools(ctx, client, params, node, runID, events, allNodes, env, inputs)
	}

	// Agentic tool-use loop — capped by node config (max_tool_turns) or default 5
	var totalInputTokens, totalOutputTokens int64
	maxToolTurns := node.MaxToolTurns
	if maxToolTurns <= 0 {
		maxToolTurns = 5
	}

	for turn := 0; turn < maxToolTurns; turn++ {
		resp, err := client.Messages.New(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("anthropic API error: %w", err)
		}

		totalInputTokens += resp.Usage.InputTokens
		totalOutputTokens += resp.Usage.OutputTokens

		// Collect text and tool use blocks
		var sb strings.Builder
		var toolUseBlocks []anthropic.ContentBlock
		for _, block := range resp.Content {
			if block.Type == "text" {
				sb.WriteString(block.Text)
			} else if block.Type == "tool_use" {
				toolUseBlocks = append(toolUseBlocks, block)
			}
		}

		// If no tool calls, we're done
		if len(toolUseBlocks) == 0 || resp.StopReason != "tool_use" {
			text := sb.String()
			output := map[string]any{
				"response": text,
				"history":  buildUpdatedHistory(inputs, message, text),
				"usage": map[string]any{
					"input_tokens":  totalInputTokens,
					"output_tokens": totalOutputTokens,
				},
				"stop_reason": string(resp.StopReason),
			}
			if node.StructuredOutput != nil {
				if parsed, err := parseJSONResponse(text); err == nil {
					for k, v := range parsed {
						output[k] = v
					}
				}
			}
			return output, nil
		}

		// Execute each tool call and collect results
		assistantMsg := resp.ToParam()
		var toolResults []anthropic.ToolResultBlockParam

		for _, block := range toolUseBlocks {
			toolName := block.Name
			toolUseID := block.ID

			events <- Event{
				Event:     EventChunk,
				RunID:     runID,
				StepID:    node.Name,
				Data:      fmt.Sprintf("[tool: %s]", toolName),
				Timestamp: time.Now(),
			}

			// Resolve tool input from the block's JSON input
			toolInputs := make(map[string]any)
			if b, err := json.Marshal(block.Input); err == nil {
				json.Unmarshal(b, &toolInputs)
			}

			// Find and execute the tool node
			var resultContent string
			if toolNode, err := allNodes.Get(toolName); err != nil {
				resultContent = fmt.Sprintf("error: tool node %q not found", toolName)
			} else {
				toolRC := NewRunContext(runID, toolInputs)
				var toolOutput map[string]any
				var toolErr error
				if toolNode.Type == NodeTypePipeline {
					toolOutput, _, toolErr = executePipeline(ctx, toolNode, toolRC, allNodes, env, events)
				} else {
					toolOutput, toolErr = executeLeafNode(ctx, toolNode, toolRC, env, events, allNodes)
				}
				if toolErr != nil {
					resultContent = fmt.Sprintf("error: %s", toolErr.Error())
				} else {
					b, _ := json.Marshal(toolOutput)
					resultContent = string(b)
				}
			}

			toolResults = append(toolResults, anthropic.NewToolResultBlock(toolUseID, resultContent, false))
		}

		// Append assistant message + tool results and continue the loop
		userContent := make([]anthropic.MessageParamContentUnion, len(toolResults))
		for i, tr := range toolResults {
			userContent[i] = tr
		}
		currentMessages := params.Messages.Value
		currentMessages = append(currentMessages, assistantMsg)
		currentMessages = append(currentMessages, anthropic.NewUserMessage(userContent...))
		params.Messages = anthropic.F(currentMessages)
	}

	return nil, fmt.Errorf("anthropic tool use exceeded maximum turns (%d)", maxToolTurns)
}

func executeLLMAnthropicStream(
	ctx context.Context,
	client *anthropic.Client,
	params anthropic.MessageNewParams,
	node *Node,
	runID string,
	events chan<- Event,
) (map[string]any, error) {

	stream := client.Messages.NewStreaming(ctx, params)

	var sb strings.Builder
	var inputTokens, outputTokens int64

	for stream.Next() {
		event := stream.Current()

		switch e := event.AsUnion().(type) {
		case anthropic.ContentBlockDeltaEvent:
			if e.Delta.Type == anthropic.ContentBlockDeltaEventDeltaTypeTextDelta {
				chunk := e.Delta.Text
				sb.WriteString(chunk)
				events <- Event{
					Event:     EventChunk,
					RunID:     runID,
					StepID:    node.Name,
					Data:      chunk,
					Timestamp: time.Now(),
				}
			}
		case anthropic.MessageDeltaEvent:
			outputTokens = e.Usage.OutputTokens
		case anthropic.MessageStartEvent:
			inputTokens = e.Message.Usage.InputTokens
		}
	}

	if err := stream.Err(); err != nil {
		return nil, fmt.Errorf("anthropic stream error: %w", err)
	}

	return map[string]any{
		"response": sb.String(),
		"usage": map[string]any{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
		},
	}, nil
}

// ollamaMsg is the message format used by Ollama's /api/chat endpoint.
type ollamaMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// executeLLMOllama calls a local Ollama instance
func executeLLMOllama(
	ctx context.Context,
	node *Node,
	runID string,
	inputs map[string]any,
	env EnvironmentConfig,
	events chan<- Event,
	allNodes Nodes,
) (map[string]any, error) {

	endpoint := env.OllamaEndpoint
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}

	message := stringVal(inputs, "message")
	if message == "" {
		return nil, fmt.Errorf("input.message is required for llm nodes")
	}

	// Build messages array
	var messages []ollamaMsg

	if node.SystemPromptText != "" {
		messages = append(messages, ollamaMsg{Role: "system", Content: node.SystemPromptText})
	}
	// Add history if provided
	if history, ok := inputs["history"]; ok {
		if histSlice, ok := history.([]any); ok {
			histSlice = trimHistory(histSlice, node.MaxHistory)
			for _, h := range histSlice {
				if hMap, ok := h.(map[string]any); ok {
					role := stringVal(hMap, "role")
					content := stringVal(hMap, "content")
					if role != "" && content != "" {
						messages = append(messages, ollamaMsg{Role: role, Content: content})
					}
				}
			}
		}
	}
	messages = append(messages, ollamaMsg{Role: "user", Content: message})

	// Build tool definitions if any
	ollamaTools := buildOllamaToolDefs(node.Tools, allNodes)
	if len(ollamaTools) > 0 {
		return executeOllamaToolLoop(ctx, node, runID, messages, ollamaTools, endpoint, inputs, env, events, allNodes)
	}

	body := map[string]any{
		"model":    node.Model,
		"messages": messages,
		"stream":   node.Stream,
		"options": map[string]any{
			"temperature": node.Temperature,
		},
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal Ollama request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint+"/api/chat", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama request failed: %w", err)
	}
	defer resp.Body.Close()

	if node.Stream {
		streamResult, err := executeOllamaStream(resp.Body, node, runID, events)
		if err != nil {
			return nil, err
		}
		if response, ok := streamResult["response"].(string); ok {
			streamResult["history"] = buildUpdatedHistory(inputs, message, response)
		}
		return streamResult, nil
	}

	var result struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode Ollama response: %w", err)
	}

	return map[string]any{
		"response": result.Message.Content,
		"history":  buildUpdatedHistory(inputs, message, result.Message.Content),
	}, nil
}

func executeOllamaStream(body io.Reader, node *Node, runID string, events chan<- Event) (map[string]any, error) {
	decoder := json.NewDecoder(body)
	var sb strings.Builder

	for {
		var chunk struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Done bool `json:"done"`
		}
		if err := decoder.Decode(&chunk); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("ollama stream decode: %w", err)
		}

		if chunk.Message.Content != "" {
			sb.WriteString(chunk.Message.Content)
			events <- Event{
				Event:     EventChunk,
				RunID:     runID,
				StepID:    node.Name,
				Data:      chunk.Message.Content,
				Timestamp: time.Now(),
			}
		}

		if chunk.Done {
			break
		}
	}

	return map[string]any{
		"response": sb.String(),
	}, nil
}

// executeLLMOpenAI calls an OpenAI-compatible API
func executeLLMOpenAI(
	ctx context.Context,
	node *Node,
	runID string,
	inputs map[string]any,
	env EnvironmentConfig,
	events chan<- Event,
) (map[string]any, error) {

	if env.OpenAIKey == "" {
		return nil, fmt.Errorf("openaiKey not set in environment config (also checks OPENAI_API_KEY)")
	}

	message := stringVal(inputs, "message")
	if message == "" {
		return nil, fmt.Errorf("input.message is required for llm nodes")
	}

	type openAIMsg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	var messages []openAIMsg
	if node.SystemPromptText != "" {
		messages = append(messages, openAIMsg{Role: "system", Content: node.SystemPromptText})
	}
	// Add history if provided
	if history, ok := inputs["history"]; ok {
		if histSlice, ok := history.([]any); ok {
			histSlice = trimHistory(histSlice, node.MaxHistory)
			for _, h := range histSlice {
				if hMap, ok := h.(map[string]any); ok {
					role := stringVal(hMap, "role")
					content := stringVal(hMap, "content")
					if role != "" && content != "" {
						messages = append(messages, openAIMsg{Role: role, Content: content})
					}
				}
			}
		}
	}
	messages = append(messages, openAIMsg{Role: "user", Content: message})

	maxTokens := node.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}

	body := map[string]any{
		"model":      node.Model,
		"messages":   messages,
		"max_tokens": maxTokens,
		"stream":     node.Stream,
	}
	if node.Temperature > 0 {
		body["temperature"] = node.Temperature
	}

	bodyBytes, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+env.OpenAIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai request failed: %w", err)
	}
	defer resp.Body.Close()

	if node.Stream {
		streamResult, err := executeOpenAIStream(resp.Body, node, runID, events)
		if err != nil {
			return nil, err
		}
		if response, ok := streamResult["response"].(string); ok {
			streamResult["history"] = buildUpdatedHistory(inputs, message, response)
		}
		return streamResult, nil
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode OpenAI response: %w", err)
	}
	if result.Error != nil {
		return nil, fmt.Errorf("openai error: %s", result.Error.Message)
	}
	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("openai returned no choices")
	}

	text := result.Choices[0].Message.Content
	output := map[string]any{
		"response": text,
		"history":  buildUpdatedHistory(inputs, message, text),
		"usage": map[string]any{
			"input_tokens":  result.Usage.PromptTokens,
			"output_tokens": result.Usage.CompletionTokens,
		},
	}
	if node.StructuredOutput != nil {
		if parsed, err := parseJSONResponse(text); err == nil {
			for k, v := range parsed {
				output[k] = v
			}
		}
	}
	return output, nil
}

func executeOpenAIStream(body io.Reader, node *Node, runID string, events chan<- Event) (map[string]any, error) {
	var sb strings.Builder
	var promptTokens, completionTokens int

	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := line[6:]
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage,omitempty"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Usage != nil {
			promptTokens = chunk.Usage.PromptTokens
			completionTokens = chunk.Usage.CompletionTokens
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			text := chunk.Choices[0].Delta.Content
			sb.WriteString(text)
			events <- Event{
				Event:     EventChunk,
				RunID:     runID,
				StepID:    node.Name,
				Data:      text,
				Timestamp: time.Now(),
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("openai stream read error: %w", err)
	}

	return map[string]any{
		"response": sb.String(),
		"usage": map[string]any{
			"input_tokens":  promptTokens,
			"output_tokens": completionTokens,
		},
	}, nil
}

// buildAnthropicToolDefs converts node names to Anthropic tool definitions,
// building the input_schema from each node's declared input fields.
func buildAnthropicToolDefs(toolNames []string, allNodes Nodes) []anthropic.ToolParam {
	if len(toolNames) == 0 || len(allNodes) == 0 {
		return nil
	}

	var defs []anthropic.ToolParam
	for _, name := range toolNames {
		toolNode, err := allNodes.Get(name)
		if err != nil {
			continue
		}

		// Build JSON Schema properties from input field schemas
		properties := make(map[string]any)
		required := []string{}
		for fieldName, schema := range toolNode.Input {
			prop := map[string]any{"type": schema.Type}
			if schema.Type == "" {
				prop["type"] = "string"
			}
			properties[fieldName] = prop
			if schema.Required {
				required = append(required, fieldName)
			}
		}

		inputSchema := map[string]any{
			"type":       "object",
			"properties": properties,
		}
		if len(required) > 0 {
			inputSchema["required"] = required
		}

		schemaBytes, _ := json.Marshal(inputSchema)

		defs = append(defs, anthropic.ToolParam{
			Name:        anthropic.F(toolNode.Name),
			Description: anthropic.F(toolNode.Description),
			InputSchema: anthropic.F[any](anthropic.ToolInputSchemaParam{
				Type:       anthropic.F(anthropic.ToolInputSchemaTypeObject),
				Properties: anthropic.F[any](json.RawMessage(schemaBytes)),
			}),
		})
	}
	return defs
}

// parseJSONResponse extracts a JSON object from an LLM response.
// Handles plain JSON, JSON inside ```json fences, and leading/trailing prose.
func parseJSONResponse(text string) (map[string]any, error) {
	text = strings.TrimSpace(text)

	// Strip ```json ... ``` fences
	if idx := strings.Index(text, "```json"); idx >= 0 {
		text = text[idx+7:]
		if end := strings.Index(text, "```"); end >= 0 {
			text = text[:end]
		}
	} else if idx := strings.Index(text, "```"); idx >= 0 {
		text = text[idx+3:]
		if end := strings.Index(text, "```"); end >= 0 {
			text = text[:end]
		}
	}

	// Find the first { and last } to extract JSON object
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end < 0 || end <= start {
		return nil, fmt.Errorf("no JSON object found in response")
	}
	text = text[start : end+1]

	var result map[string]any
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return nil, fmt.Errorf("JSON parse error: %w", err)
	}
	return result, nil
}

// executeLLMAnthropicStreamWithTools streams an Anthropic response, emitting chunk events
// for text tokens while handling tool-use turns inline. Replaces the old streaming path
// (which silently dropped tools) and the non-streaming tool loop when stream=true.
func executeLLMAnthropicStreamWithTools(
	ctx context.Context,
	client *anthropic.Client,
	params anthropic.MessageNewParams,
	node *Node,
	runID string,
	events chan<- Event,
	allNodes Nodes,
	env EnvironmentConfig,
	inputs map[string]any,
) (map[string]any, error) {

	type pendingBlock struct {
		btype    string
		text     strings.Builder
		toolID   string
		toolName string
		toolJSON strings.Builder
	}

	var totalInputTokens, totalOutputTokens int64
	maxToolTurns := node.MaxToolTurns
	if maxToolTurns <= 0 {
		maxToolTurns = 5
	}

	for turn := 0; turn < maxToolTurns; turn++ {
		stream := client.Messages.NewStreaming(ctx, params)

		var blocks []*pendingBlock
		var stopReason anthropic.MessageDeltaEventDeltaStopReason

		for stream.Next() {
			event := stream.Current()
			switch e := event.AsUnion().(type) {
			case anthropic.ContentBlockStartEvent:
				b := &pendingBlock{btype: string(e.ContentBlock.Type)}
				if e.ContentBlock.Type == anthropic.ContentBlockStartEventContentBlockTypeToolUse {
					b.toolID = e.ContentBlock.ID
					b.toolName = e.ContentBlock.Name
				}
				blocks = append(blocks, b)

			case anthropic.ContentBlockDeltaEvent:
				idx := int(e.Index)
				if idx < len(blocks) {
					switch delta := e.Delta.AsUnion().(type) {
					case anthropic.TextDelta:
						blocks[idx].text.WriteString(delta.Text)
						events <- Event{
							Event:     EventChunk,
							RunID:     runID,
							StepID:    node.Name,
							Data:      delta.Text,
							Timestamp: time.Now(),
						}
					case anthropic.InputJSONDelta:
						blocks[idx].toolJSON.WriteString(delta.PartialJSON)
					}
				}

			case anthropic.MessageDeltaEvent:
				stopReason = e.Delta.StopReason
				totalOutputTokens += e.Usage.OutputTokens

			case anthropic.MessageStartEvent:
				totalInputTokens += e.Message.Usage.InputTokens
			}
		}
		if err := stream.Err(); err != nil {
			return nil, fmt.Errorf("anthropic stream error: %w", err)
		}

		// Collect full text from this turn
		var sb strings.Builder
		for _, b := range blocks {
			if b.btype == "text" {
				sb.WriteString(b.text.String())
			}
		}

		if stopReason != anthropic.MessageDeltaEventDeltaStopReasonToolUse {
			text := sb.String()
			output := map[string]any{
				"response": text,
				"history":  buildUpdatedHistory(inputs, stringVal(inputs, "message"), text),
				"usage": map[string]any{
					"input_tokens":  totalInputTokens,
					"output_tokens": totalOutputTokens,
				},
				"stop_reason": string(stopReason),
			}
			if node.StructuredOutput != nil {
				if parsed, err := parseJSONResponse(text); err == nil {
					for k, v := range parsed {
						output[k] = v
					}
				}
			}
			return output, nil
		}

		// Build assistant message from streamed blocks
		var assistantContent []anthropic.MessageParamContentUnion
		for _, b := range blocks {
			if b.btype == "text" && b.text.Len() > 0 {
				assistantContent = append(assistantContent, anthropic.NewTextBlock(b.text.String()))
			} else if b.btype == "tool_use" {
				var toolInput map[string]any
				json.Unmarshal([]byte(b.toolJSON.String()), &toolInput)
				assistantContent = append(assistantContent,
					anthropic.NewToolUseBlockParam(b.toolID, b.toolName, toolInput))
			}
		}

		// Execute tool calls and collect results
		var toolResults []anthropic.ToolResultBlockParam
		for _, b := range blocks {
			if b.btype != "tool_use" {
				continue
			}
			events <- Event{
				Event:     EventChunk,
				RunID:     runID,
				StepID:    node.Name,
				Data:      fmt.Sprintf("[tool: %s]", b.toolName),
				Timestamp: time.Now(),
			}
			var toolInputs map[string]any
			json.Unmarshal([]byte(b.toolJSON.String()), &toolInputs)

			var resultContent string
			if toolNode, err := allNodes.Get(b.toolName); err != nil {
				resultContent = fmt.Sprintf("error: tool node %q not found", b.toolName)
			} else {
				toolRC := NewRunContext(runID, toolInputs)
				toolOutput, toolErr := executeLeafNode(ctx, toolNode, toolRC, env, events, allNodes)
				if toolErr != nil {
					resultContent = fmt.Sprintf("error: %s", toolErr.Error())
				} else {
					byt, _ := json.Marshal(toolOutput)
					resultContent = string(byt)
				}
			}
			toolResults = append(toolResults, anthropic.NewToolResultBlock(b.toolID, resultContent, false))
		}

		// Append assistant turn + tool results and loop
		var userContent []anthropic.MessageParamContentUnion
		for _, tr := range toolResults {
			userContent = append(userContent, tr)
		}
		currentMessages := params.Messages.Value
		currentMessages = append(currentMessages, anthropic.NewAssistantMessage(assistantContent...))
		currentMessages = append(currentMessages, anthropic.NewUserMessage(userContent...))
		params.Messages = anthropic.F(currentMessages)
	}

	return nil, fmt.Errorf("anthropic tool use exceeded maximum turns (%d)", maxToolTurns)
}

// executeOllamaToolLoop runs an agentic tool-use loop for Ollama models.
// Uses []map[string]any for messages so tool_calls can be round-tripped faithfully.
func executeOllamaToolLoop(
	ctx context.Context,
	node *Node,
	runID string,
	seed []ollamaMsg,
	tools []map[string]any,
	endpoint string,
	inputs map[string]any,
	env EnvironmentConfig,
	events chan<- Event,
	allNodes Nodes,
) (map[string]any, error) {
	maxToolTurns := node.MaxToolTurns
	if maxToolTurns <= 0 {
		maxToolTurns = 5
	}

	// Convert seed messages to generic maps so we can append tool_calls later
	messages := make([]map[string]any, len(seed))
	for i, m := range seed {
		messages[i] = map[string]any{"role": m.Role, "content": m.Content}
	}

	for turn := 0; turn < maxToolTurns; turn++ {
		body := map[string]any{
			"model":    node.Model,
			"messages": messages,
			"tools":    tools,
			"stream":   false,
			"options":  map[string]any{"temperature": node.Temperature},
		}
		bodyBytes, _ := json.Marshal(body)
		req, err := http.NewRequestWithContext(ctx, "POST", endpoint+"/api/chat", bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("ollama tool request failed: %w", err)
		}
		defer resp.Body.Close()

		var result struct {
			Message struct {
				Role      string          `json:"role"`
				Content   string          `json:"content"`
				ToolCalls json.RawMessage `json:"tool_calls,omitempty"`
			} `json:"message"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return nil, fmt.Errorf("failed to decode Ollama tool response: %w", err)
		}

		// No tool calls via API — check for text-embedded tool calls
		// (some models output {"name":"tool","arguments":{...}} as text instead of using tool_calls)
		if len(result.Message.ToolCalls) == 0 || string(result.Message.ToolCalls) == "null" {
			textCalls := extractTextToolCalls(result.Message.Content)
			if len(textCalls) > 0 {
				// Append assistant turn so the model can see its own output
				messages = append(messages, map[string]any{
					"role":    "assistant",
					"content": result.Message.Content,
				})

				// Execute each text-embedded tool and collect results
				var resultParts []string
				for _, tc := range textCalls {
					events <- Event{
						Event:     EventChunk,
						RunID:     runID,
						StepID:    node.Name,
						Data:      fmt.Sprintf("[tool: %s]", tc.Name),
						Timestamp: time.Now(),
					}
					var resultContent string
					if toolNode, toolErr := allNodes.Get(tc.Name); toolErr != nil {
						resultContent = fmt.Sprintf("error: tool %q not found", tc.Name)
					} else {
						toolRC := NewRunContext(runID, tc.Arguments)
						toolOutput, execErr := executeLeafNode(ctx, toolNode, toolRC, env, events, allNodes)
						if execErr != nil {
							resultContent = fmt.Sprintf("error: %s", execErr.Error())
						} else {
							b, _ := json.Marshal(toolOutput)
							resultContent = string(b)
						}
					}
					resultParts = append(resultParts, fmt.Sprintf("%s: %s", tc.Name, resultContent))
				}

				// Inject results and ask for a clean voice response
				injection := strings.Join(resultParts, "\n") +
					"\n\nUsing the above results, answer the user's request in 1-2 plain spoken sentences. No JSON, no markdown, no code."
				messages = append(messages, map[string]any{"role": "user", "content": injection})
				continue
			}

			// Final answer — no tool calls of any kind
			return map[string]any{
				"response": result.Message.Content,
				"history":  buildUpdatedHistory(inputs, stringVal(inputs, "message"), result.Message.Content),
			}, nil
		}

		// Decode tool calls
		var toolCalls []struct {
			Function struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"function"`
		}
		if err := json.Unmarshal(result.Message.ToolCalls, &toolCalls); err != nil || len(toolCalls) == 0 {
			return map[string]any{"response": result.Message.Content}, nil
		}

		// Append assistant message preserving tool_calls for the model's context
		assistantMsg := map[string]any{
			"role":       "assistant",
			"content":    result.Message.Content,
			"tool_calls": toolCalls,
		}
		messages = append(messages, assistantMsg)

		// Execute each tool and add results
		for _, tc := range toolCalls {
			toolName := tc.Function.Name
			toolArgs := tc.Function.Arguments

			argsJSON, _ := json.Marshal(toolArgs)
			events <- Event{
				Event:     EventChunk,
				RunID:     runID,
				StepID:    node.Name,
				Data:      fmt.Sprintf("[tool: %s] args: %s", toolName, argsJSON),
				Timestamp: time.Now(),
			}

			var resultContent string
			allowed := false
			for _, t := range node.Tools {
				if t == toolName {
					allowed = true
					break
				}
			}
			if !allowed {
				resultContent = fmt.Sprintf("error: tool %q is not available. Available tools: %s", toolName, strings.Join(node.Tools, ", "))
			} else if toolNode, err := allNodes.Get(toolName); err != nil {
				resultContent = fmt.Sprintf("error: tool node %q not found", toolName)
			} else {
				toolRC := NewRunContext(runID, toolArgs)
				var toolOutput map[string]any
				var toolErr error
				if toolNode.Type == NodeTypePipeline {
					toolOutput, _, toolErr = executePipeline(ctx, toolNode, toolRC, allNodes, env, events)
				} else {
					toolOutput, toolErr = executeLeafNode(ctx, toolNode, toolRC, env, events, allNodes)
				}
				if toolErr != nil {
					resultContent = fmt.Sprintf("error: %s", toolErr.Error())
				} else {
					b, _ := json.Marshal(toolOutput)
					resultContent = string(b)
				}
			}
			events <- Event{
				Event:     EventChunk,
				RunID:     runID,
				StepID:    node.Name,
				Data:      fmt.Sprintf("[tool result: %s] %s", toolName, resultContent),
				Timestamp: time.Now(),
			}
			messages = append(messages, map[string]any{"role": "tool", "content": resultContent})
		}
	}

	return nil, fmt.Errorf("ollama tool use exceeded maximum turns (%d)", maxToolTurns)
}

// buildOllamaToolDefs converts node names to Ollama/OpenAI-format tool definitions.
func buildOllamaToolDefs(toolNames []string, allNodes Nodes) []map[string]any {
	if len(toolNames) == 0 || len(allNodes) == 0 {
		return nil
	}
	var defs []map[string]any
	for _, name := range toolNames {
		toolNode, err := allNodes.Get(name)
		if err != nil {
			continue
		}
		properties := map[string]any{}
		required := []string{}
		for fieldName, schema := range toolNode.Input {
			prop := map[string]any{"type": schema.Type}
			if prop["type"] == "" {
				prop["type"] = "string"
			}
			properties[fieldName] = prop
			if schema.Required {
				required = append(required, fieldName)
			}
		}
		params := map[string]any{
			"type":       "object",
			"properties": properties,
		}
		if len(required) > 0 {
			params["required"] = required
		}
		defs = append(defs, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        toolNode.Name,
				"description": toolNode.Description,
				"parameters":  params,
			},
		})
	}
	return defs
}

// textToolCall is a tool call embedded as plain JSON text in a model response.
type textToolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// extractTextToolCalls finds tool calls that a model embedded as JSON text in its response
// instead of using the proper tool_calls API field. Handles:
//
//	{"name": "tool_name", "arguments": {"key": "value"}}   (plain JSON)
//	<tool_call>{"name": "tool_name", "arguments": {...}}</tool_call>  (hermes3/chatml style)
func extractTextToolCalls(content string) []textToolCall {
	// Strip markdown code fences
	content = strings.ReplaceAll(content, "```json", "")
	content = strings.ReplaceAll(content, "```", "")

	// Extract from <tool_call>...</tool_call> tags (hermes3, chatml)
	var xmlCalls []textToolCall
	for {
		open := strings.Index(content, "<tool_call>")
		if open < 0 {
			break
		}
		close := strings.Index(content[open:], "</tool_call>")
		if close < 0 {
			break
		}
		inner := strings.TrimSpace(content[open+len("<tool_call>") : open+close])
		var c textToolCall
		if err := json.Unmarshal([]byte(inner), &c); err == nil && c.Name != "" {
			if c.Arguments == nil {
				c.Arguments = map[string]any{}
			}
			xmlCalls = append(xmlCalls, c)
		}
		content = content[open+close+len("</tool_call>"):]
	}
	if len(xmlCalls) > 0 {
		return xmlCalls
	}

	var calls []textToolCall
	depth := 0
	start := -1

	for i := range content {
		switch content[i] {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 && start >= 0 {
					var c textToolCall
					if err := json.Unmarshal([]byte(content[start:i+1]), &c); err == nil && c.Name != "" {
						if c.Arguments == nil {
							c.Arguments = map[string]any{}
						}
						calls = append(calls, c)
					}
					start = -1
				}
			}
		}
	}

	return calls
}

// buildUpdatedHistory appends the current user/assistant exchange to the existing history.
func buildUpdatedHistory(inputs map[string]any, userMsg, assistantMsg string) []any {
	var history []any
	if h, ok := inputs["history"]; ok {
		if hs, ok := h.([]any); ok {
			history = append(history, hs...)
		}
	}
	return append(history,
		map[string]any{"role": "user", "content": userMsg},
		map[string]any{"role": "assistant", "content": assistantMsg},
	)
}

// trimHistory trims a history slice to the most recent maxHistory entries.
// If maxHistory <= 0, history is returned unchanged.
func trimHistory(history []any, maxHistory int) []any {
	if maxHistory <= 0 || len(history) <= maxHistory {
		return history
	}
	return history[len(history)-maxHistory:]
}

// stringVal extracts a string from a map[string]any
func stringVal(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return fmt.Sprintf("%v", v)
	}
	return s
}
