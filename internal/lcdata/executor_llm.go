package lcdata

import (
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
) (map[string]any, error) {

	switch strings.ToLower(node.Provider) {
	case "anthropic":
		return executeLLMAnthropic(ctx, node, runID, inputs, env, events)
	case "ollama":
		return executeLLMOllama(ctx, node, runID, inputs, env, events)
	case "openai":
		return executeLLMOpenAI(ctx, node, runID, inputs, env, events)
	default:
		return nil, fmt.Errorf("unknown LLM provider: %s", node.Provider)
	}
}

// executeLLMAnthropic calls the Anthropic Claude API
func executeLLMAnthropic(
	ctx context.Context,
	node *Node,
	runID string,
	inputs map[string]any,
	env EnvironmentConfig,
	events chan<- Event,
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

	// Stream or single call
	if node.Stream {
		return executeLLMAnthropicStream(ctx, client, params, node, runID, events)
	}

	resp, err := client.Messages.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("anthropic API error: %w", err)
	}

	var sb strings.Builder
	for _, block := range resp.Content {
		if block.Type == "text" {
			sb.WriteString(block.Text)
		}
	}

	text := sb.String()
	output := map[string]any{
		"response": text,
		"usage": map[string]any{
			"input_tokens":  resp.Usage.InputTokens,
			"output_tokens": resp.Usage.OutputTokens,
		},
		"stop_reason": string(resp.StopReason),
	}

	// If structured_output is defined, try to parse the response as JSON
	// and merge the fields into the output map
	if node.StructuredOutput != nil {
		if parsed, err := parseJSONResponse(text); err == nil {
			for k, v := range parsed {
				output[k] = v
			}
		}
	}

	return output, nil
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

// executeLLMOllama calls a local Ollama instance
func executeLLMOllama(
	ctx context.Context,
	node *Node,
	runID string,
	inputs map[string]any,
	env EnvironmentConfig,
	events chan<- Event,
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
	type ollamaMsg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	var messages []ollamaMsg

	if node.SystemPromptText != "" {
		messages = append(messages, ollamaMsg{Role: "system", Content: node.SystemPromptText})
	}
	messages = append(messages, ollamaMsg{Role: "user", Content: message})

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
		return executeOllamaStream(resp.Body, node, runID, events)
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
	messages = append(messages, openAIMsg{Role: "user", Content: message})

	maxTokens := node.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}

	body := map[string]any{
		"model":      node.Model,
		"messages":   messages,
		"max_tokens": maxTokens,
		"stream":     false,
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

	return map[string]any{
		"response": result.Choices[0].Message.Content,
		"usage": map[string]any{
			"input_tokens":  result.Usage.PromptTokens,
			"output_tokens": result.Usage.CompletionTokens,
		},
	}, nil
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
