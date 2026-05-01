package lcdata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// springgResponse is the envelope returned by every springg API call.
type springgResponse struct {
	Status string          `json:"status"`
	Data   json.RawMessage `json:"data,omitempty"`
	Error  string          `json:"error,omitempty"`
}

func executeVector(
	ctx context.Context,
	node *Node,
	inputs map[string]any,
	env EnvironmentConfig,
	events chan<- Event,
) (map[string]any, error) {
	endpoint := env.SpringgEndpoint
	if endpoint == "" {
		endpoint = "http://localhost:8181"
	}
	endpoint = strings.TrimRight(endpoint, "/")

	switch strings.ToLower(node.Operation) {
	case "upsert":
		return executeVectorUpsert(ctx, node, inputs, endpoint, env.SpringgKey)
	case "search":
		return executeVectorSearch(ctx, node, inputs, endpoint, env.SpringgKey)
	case "get":
		return executeVectorGet(ctx, node, inputs, endpoint, env.SpringgKey)
	case "delete":
		return executeVectorDelete(ctx, node, inputs, endpoint, env.SpringgKey)
	case "create_index":
		return executeVectorCreateIndex(ctx, node, endpoint, env.SpringgKey)
	case "delete_index":
		return executeVectorDeleteIndex(ctx, node, endpoint, env.SpringgKey)
	default:
		return nil, fmt.Errorf("unknown vector operation: %s (supported: upsert, search, get, delete, create_index, delete_index)", node.Operation)
	}
}

// executeVectorUpsert adds or updates a vector in the index.
// Inputs: id (string), vector ([]float64), metadata (object, optional)
func executeVectorUpsert(
	ctx context.Context,
	node *Node,
	inputs map[string]any,
	endpoint, key string,
) (map[string]any, error) {
	id := stringVal(inputs, "id")
	if id == "" {
		return nil, fmt.Errorf("input.id is required for vector upsert")
	}

	vec, err := toFloat32Slice(inputs["vector"])
	if err != nil {
		return nil, fmt.Errorf("input.vector: %w", err)
	}

	body := map[string]any{
		"id":     id,
		"vector": vec,
	}
	if meta, ok := inputs["metadata"]; ok && meta != nil {
		body["metadata"] = map[string]any{
			"custom": meta,
		}
	}

	// Try add (POST); if the ID already exists springg returns 409 — fall through to update (PUT).
	addURL := fmt.Sprintf("%s/api/indexes/%s/vectors", endpoint, url.PathEscape(node.Index))
	resp, err := springgDo(ctx, http.MethodPost, addURL, key, body)
	if err != nil {
		return nil, err
	}
	if resp["_status_code"] == 409 {
		delete(resp, "_status_code")
		updateURL := fmt.Sprintf("%s/api/indexes/%s/vectors/%s", endpoint, url.PathEscape(node.Index), url.PathEscape(id))
		updateBody := map[string]any{"vector": vec}
		if meta, ok := body["metadata"]; ok {
			updateBody["metadata"] = meta
		}
		return springgDo(ctx, http.MethodPut, updateURL, key, updateBody)
	}
	delete(resp, "_status_code")
	return resp, nil
}

// executeVectorSearch finds the top-k most similar vectors.
// Inputs: vector ([]float64), k (number, optional — overrides node.top_k)
func executeVectorSearch(
	ctx context.Context,
	node *Node,
	inputs map[string]any,
	endpoint, key string,
) (map[string]any, error) {
	vec, err := toFloat32Slice(inputs["vector"])
	if err != nil {
		return nil, fmt.Errorf("input.vector: %w", err)
	}

	k := node.TopK
	if k <= 0 {
		k = 10
	}
	if kInput, ok := inputs["k"]; ok {
		if kNum := toInt(kInput); kNum > 0 {
			k = kNum
		}
	}

	body := map[string]any{
		"vector": vec,
		"k":      k,
	}

	searchURL := fmt.Sprintf("%s/api/indexes/%s/search", endpoint, url.PathEscape(node.Index))
	raw, err := springgDo(ctx, http.MethodPost, searchURL, key, body)
	if err != nil {
		return nil, err
	}

	// results is []any decoded from springg's []SearchResult
	results, _ := raw["results"].([]any)
	if results == nil {
		results = []any{}
	}
	return map[string]any{
		"results": results,
		"count":   len(results),
	}, nil
}

// executeVectorGet fetches a single vector by ID.
// Inputs: id (string)
func executeVectorGet(
	ctx context.Context,
	node *Node,
	inputs map[string]any,
	endpoint, key string,
) (map[string]any, error) {
	id := stringVal(inputs, "id")
	if id == "" {
		return nil, fmt.Errorf("input.id is required for vector get")
	}

	getURL := fmt.Sprintf("%s/api/indexes/%s/vectors/%s", endpoint, url.PathEscape(node.Index), url.PathEscape(id))
	return springgDo(ctx, http.MethodGet, getURL, key, nil)
}

// executeVectorDelete removes a vector by ID.
// Inputs: id (string)
func executeVectorDelete(
	ctx context.Context,
	node *Node,
	inputs map[string]any,
	endpoint, key string,
) (map[string]any, error) {
	id := stringVal(inputs, "id")
	if id == "" {
		return nil, fmt.Errorf("input.id is required for vector delete")
	}

	delURL := fmt.Sprintf("%s/api/indexes/%s/vectors/%s", endpoint, url.PathEscape(node.Index), url.PathEscape(id))
	return springgDo(ctx, http.MethodDelete, delURL, key, nil)
}

// executeVectorCreateIndex creates the index defined in node.index with node.dimensions.
func executeVectorCreateIndex(
	ctx context.Context,
	node *Node,
	endpoint, key string,
) (map[string]any, error) {
	createURL := fmt.Sprintf("%s/api/indexes/%s", endpoint, url.PathEscape(node.Index))
	return springgDo(ctx, http.MethodPost, createURL, key, map[string]any{
		"dimensions": node.Dimensions,
	})
}

// executeVectorDeleteIndex deletes the index defined in node.index.
func executeVectorDeleteIndex(
	ctx context.Context,
	node *Node,
	endpoint, key string,
) (map[string]any, error) {
	delURL := fmt.Sprintf("%s/api/indexes/%s", endpoint, url.PathEscape(node.Index))
	return springgDo(ctx, http.MethodDelete, delURL, key, nil)
}

// springgDo executes a single springg HTTP request and unwraps the response envelope.
// It returns the contents of "data" as a flat map[string]any, with "_status_code" injected
// so callers can inspect it (e.g. for 409 upsert logic) before stripping it.
func springgDo(ctx context.Context, method, rawURL, key string, body any) (map[string]any, error) {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal springg request: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, rawURL, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to build springg request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("springg request failed: %w", err)
	}
	defer resp.Body.Close()

	var envelope springgResponse
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("failed to decode springg response (status %d): %w", resp.StatusCode, err)
	}

	if envelope.Status != "success" {
		return nil, fmt.Errorf("springg error: %s", envelope.Error)
	}

	// Decode the data payload into a generic map or array.
	result := map[string]any{"_status_code": resp.StatusCode}
	if len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		// Try object first; if data is an array (e.g. search results, list indexes)
		// wrap it under "results".
		if envelope.Data[0] == '[' {
			var arr []any
			if err := json.Unmarshal(envelope.Data, &arr); err != nil {
				return nil, fmt.Errorf("failed to decode springg data array: %w", err)
			}
			result["results"] = arr
		} else {
			if err := json.Unmarshal(envelope.Data, &result); err != nil {
				return nil, fmt.Errorf("failed to decode springg data: %w", err)
			}
			result["_status_code"] = resp.StatusCode
		}
	}
	return result, nil
}

// toFloat32Slice converts a vector value to []float32.
// Handles []float64 (returned directly by embedding executors) and
// []any (from JSON-decoded inputs).
func toFloat32Slice(v any) ([]float32, error) {
	switch arr := v.(type) {
	case []float32:
		return arr, nil
	case []float64:
		out := make([]float32, len(arr))
		for i, f := range arr {
			out[i] = float32(f)
		}
		return out, nil
	case []any:
		out := make([]float32, len(arr))
		for i, val := range arr {
			switch n := val.(type) {
			case float64:
				out[i] = float32(n)
			case float32:
				out[i] = n
			default:
				return nil, fmt.Errorf("element %d is not a number", i)
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("must be an array of numbers, got %T", v)
	}
}

