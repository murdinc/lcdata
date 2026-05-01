package lcdata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

func executeEmbedding(
	ctx context.Context,
	node *Node,
	inputs map[string]any,
	env EnvironmentConfig,
	events chan<- Event,
) (map[string]any, error) {
	text := stringVal(inputs, "text")
	if text == "" {
		return nil, fmt.Errorf("input.text is required for embedding nodes")
	}

	switch strings.ToLower(node.Provider) {
	case "openai":
		return executeEmbeddingOpenAI(ctx, node, text, env)
	case "ollama":
		return executeEmbeddingOllama(ctx, node, text, env)
	default:
		return nil, fmt.Errorf("unknown embedding provider: %s (supported: openai, ollama)", node.Provider)
	}
}

func executeEmbeddingOpenAI(
	ctx context.Context,
	node *Node,
	text string,
	env EnvironmentConfig,
) (map[string]any, error) {
	if env.OpenAIKey == "" {
		return nil, fmt.Errorf("openaiKey not set in environment config (also checks OPENAI_API_KEY)")
	}

	model := node.Model
	if model == "" {
		model = "text-embedding-3-small"
	}

	payload, _ := json.Marshal(map[string]any{
		"model": model,
		"input": text,
	})

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/embeddings", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+env.OpenAIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai embeddings request failed: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode OpenAI embeddings response: %w", err)
	}
	if result.Error != nil {
		return nil, fmt.Errorf("openai embeddings error: %s", result.Error.Message)
	}
	if len(result.Data) == 0 {
		return nil, fmt.Errorf("openai embeddings returned no data")
	}

	return map[string]any{
		"vector":     result.Data[0].Embedding,
		"dimensions": len(result.Data[0].Embedding),
		"model":      model,
		"usage": map[string]any{
			"input_tokens": result.Usage.PromptTokens,
			"total_tokens": result.Usage.TotalTokens,
		},
	}, nil
}

func executeEmbeddingOllama(
	ctx context.Context,
	node *Node,
	text string,
	env EnvironmentConfig,
) (map[string]any, error) {
	endpoint := env.OllamaEndpoint
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}

	model := node.Model
	if model == "" {
		return nil, fmt.Errorf("model is required for ollama embedding nodes (e.g. nomic-embed-text)")
	}

	payload, _ := json.Marshal(map[string]any{
		"model":  model,
		"prompt": text,
	})

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint+"/api/embeddings", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama embeddings request failed: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Embedding []float64 `json:"embedding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode Ollama embeddings response: %w", err)
	}
	if len(result.Embedding) == 0 {
		return nil, fmt.Errorf("ollama embeddings returned empty vector")
	}

	return map[string]any{
		"vector":     result.Embedding,
		"dimensions": len(result.Embedding),
		"model":      model,
	}, nil
}
