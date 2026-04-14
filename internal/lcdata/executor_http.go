package lcdata

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

func executeHTTP(
	ctx context.Context,
	node *Node,
	inputs map[string]any,
	rc *RunContext,
	env EnvironmentConfig,
	events chan<- Event,
) (map[string]any, error) {

	// Render URL
	url, err := rc.Render(node.URL)
	if err != nil {
		return nil, fmt.Errorf("url template: %w", err)
	}

	// Render body
	var bodyReader io.Reader
	if node.Body != "" {
		rendered, err := rc.Render(node.Body)
		if err != nil {
			return nil, fmt.Errorf("body template: %w", err)
		}
		bodyReader = strings.NewReader(rendered)
	}

	method := node.Method
	if method == "" {
		method = "GET"
	}

	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}

	// Render and set headers
	for k, v := range node.Headers {
		rendered, err := rc.Render(v)
		if err != nil {
			return nil, fmt.Errorf("header %s template: %w", k, err)
		}
		req.Header.Set(k, rendered)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	bodyStr := string(body)
	if node.StripHTML {
		bodyStr = stripHTML(bodyStr)
	}

	return map[string]any{
		"status": resp.StatusCode,
		"body":   bodyStr,
	}, nil
}

var (
	reHTMLTags    = regexp.MustCompile(`<[^>]+>`)
	reHTMLComment = regexp.MustCompile(`<!--.*?-->`)
	reScript      = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	reStyle       = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	reWhitespace  = regexp.MustCompile(`[ \t]{2,}`)
	reBlankLines  = regexp.MustCompile(`\n{3,}`)
)

// stripHTML removes HTML tags and collapses whitespace, producing readable plain text
func stripHTML(s string) string {
	s = reScript.ReplaceAllString(s, "")
	s = reStyle.ReplaceAllString(s, "")
	s = reHTMLComment.ReplaceAllString(s, "")
	// Replace block-level tags with newlines for readable output
	for _, tag := range []string{"p", "div", "br", "li", "h1", "h2", "h3", "h4", "h5", "h6", "tr", "blockquote"} {
		s = regexp.MustCompile(`(?i)</?`+tag+`[^>]*>`).ReplaceAllString(s, "\n")
	}
	s = reHTMLTags.ReplaceAllString(s, "")
	// Decode common HTML entities
	s = strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", `"`,
		"&#39;", "'",
		"&nbsp;", " ",
	).Replace(s)
	s = reWhitespace.ReplaceAllString(s, " ")
	s = reBlankLines.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
