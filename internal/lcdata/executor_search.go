package lcdata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

func executeSearch(
	ctx context.Context,
	node *Node,
	inputs map[string]any,
	env EnvironmentConfig,
	events chan<- Event,
) (map[string]any, error) {

	query := stringVal(inputs, "query")
	if query == "" {
		return nil, fmt.Errorf("input.query is required for search nodes")
	}

	count := node.SearchCount
	if count <= 0 {
		count = 10
	}

	switch node.SearchProvider {
	case "brave":
		return executeBraveSearch(ctx, query, count, env)
	case "searxng":
		return executeSearXNGSearch(ctx, query, count, env)
	default:
		return nil, fmt.Errorf("unknown search_provider: %q (supported: brave, searxng)", node.SearchProvider)
	}
}

// SearchResult is a single search result item
type SearchResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

func executeBraveSearch(ctx context.Context, query string, count int, env EnvironmentConfig) (map[string]any, error) {
	if env.BraveKey == "" {
		return nil, fmt.Errorf("braveKey not set in environment config (also checks BRAVE_API_KEY)")
	}

	endpoint := "https://api.search.brave.com/res/v1/web/search"
	params := url.Values{}
	params.Set("q", query)
	params.Set("count", fmt.Sprintf("%d", count))

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint+"?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build Brave request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("X-Subscription-Token", env.BraveKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("brave search request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read brave response: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("brave search returned status %d: %s", resp.StatusCode, string(body))
	}

	var raw struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("failed to decode brave response: %w", err)
	}

	results := make([]any, 0, len(raw.Web.Results))
	for _, r := range raw.Web.Results {
		results = append(results, map[string]any{
			"title":       r.Title,
			"url":         r.URL,
			"description": r.Description,
		})
	}

	return map[string]any{
		"results": results,
		"count":   len(results),
		"query":   query,
	}, nil
}

func executeSearXNGSearch(ctx context.Context, query string, count int, env EnvironmentConfig) (map[string]any, error) {
	endpoint := env.SearxngEndpoint
	if endpoint == "" {
		endpoint = "http://localhost:8888"
	}

	params := url.Values{}
	params.Set("q", query)
	params.Set("format", "json")
	params.Set("pageno", "1")

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint+"/search?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build SearXNG request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("searxng request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read searxng response: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("searxng returned status %d: %s", resp.StatusCode, string(body))
	}

	var raw struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("failed to decode searxng response: %w", err)
	}

	// Cap to requested count
	rawResults := raw.Results
	if len(rawResults) > count {
		rawResults = rawResults[:count]
	}

	results := make([]any, 0, len(rawResults))
	for _, r := range rawResults {
		results = append(results, map[string]any{
			"title":       r.Title,
			"url":         r.URL,
			"description": r.Content,
		})
	}

	return map[string]any{
		"results": results,
		"count":   len(results),
		"query":   query,
	}, nil
}
