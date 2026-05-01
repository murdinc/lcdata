package lcdata

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"text/template"
	"time"
)

// RunContext is the shared data envelope for a single run.
// All steps read from and write to this context.
// Keys are namespaced: "step_id.field" or "input.field".
type RunContext struct {
	mu    sync.RWMutex
	data  map[string]any
	RunID string
}

// NewRunContext creates a new context with the given run ID and user inputs
func NewRunContext(runID string, inputs map[string]any) *RunContext {
	rc := &RunContext{
		RunID: runID,
		data:  make(map[string]any),
	}
	for k, v := range inputs {
		rc.data["input."+k] = v
	}
	return rc
}

// Set writes a value under the given key
func (rc *RunContext) Set(key string, value any) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.data[key] = value
}

// SetStepOutput writes all output fields from a step under "stepID.field" namespace
func (rc *RunContext) SetStepOutput(stepID string, output map[string]any) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	for k, v := range output {
		rc.data[stepID+"."+k] = v
	}
}

// Get retrieves a value by key
func (rc *RunContext) Get(key string) (any, bool) {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	v, ok := rc.data[key]
	return v, ok
}

// Snapshot returns a copy of all context data as a deeply nested map for template rendering.
// Keys like "stt.transcript" become {"stt": {"transcript": ...}}.
// Keys like "gather.web.results" become {"gather": {"web": {"results": ...}}}.
func (rc *RunContext) Snapshot() map[string]any {
	rc.mu.RLock()
	defer rc.mu.RUnlock()

	nested := make(map[string]any)
	for k, v := range rc.data {
		parts := strings.Split(k, ".")
		setNestedValue(nested, parts, v)
	}
	return nested
}

// setNestedValue sets a value in a nested map, creating intermediate maps as needed
func setNestedValue(m map[string]any, parts []string, v any) {
	if len(parts) == 0 {
		return
	}
	if len(parts) == 1 {
		m[parts[0]] = v
		return
	}
	child, ok := m[parts[0]]
	if !ok {
		child = make(map[string]any)
		m[parts[0]] = child
	}
	if childMap, ok := child.(map[string]any); ok {
		setNestedValue(childMap, parts[1:], v)
	}
}

// Render executes a Go template string against the current context snapshot.
// Template syntax: {{.step_id.field}}, {{.input.field}}
func (rc *RunContext) Render(tmplStr string) (string, error) {
	if tmplStr == "" {
		return "", nil
	}
	if !strings.Contains(tmplStr, "{{") {
		return tmplStr, nil
	}

	funcMap := template.FuncMap{
		"toFloat":  toFloat,
		"toInt":    toInt,
		"toJSON":   toJSON,
		"fromJSON": fromJSON,
		"default":  defaultVal,
		"join":     strings.Join,
		"now":      func() string { return time.Now().Format(time.RFC3339) },
		"date":     func() string { return time.Now().Format("2006-01-02") },
		"datetime": func() string { return time.Now().Format("2006-01-02 15:04:05") },
		"unix":     func() string { return fmt.Sprintf("%d", time.Now().UnixNano()) },
		"gt":       func(a, b float64) bool { return a > b },
		"lt":       func(a, b float64) bool { return a < b },
	}

	t, err := template.New("").Funcs(funcMap).Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("template parse error: %w", err)
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, rc.Snapshot()); err != nil {
		return "", fmt.Errorf("template execute error: %w", err)
	}
	return buf.String(), nil
}

// RenderValue renders a template string but preserves the original type for simple
// path references like "{{.step_id.field}}". If the template is a complex expression
// (string interpolation, function calls), it falls back to string rendering.
//
// This is the key fix for passing slices, numbers, and objects between pipeline steps
// without stringifying them.
func (rc *RunContext) RenderValue(tmplStr string) (any, error) {
	trimmed := strings.TrimSpace(tmplStr)

	// Detect a simple single-reference template: "{{.a}}" or "{{.a.b.c}}"
	// Must start with "{{.", end with "}}", and contain no spaces (no function calls)
	if strings.HasPrefix(trimmed, "{{.") && strings.HasSuffix(trimmed, "}}") {
		inner := trimmed[3 : len(trimmed)-2] // strip "{{." and "}}"
		inner = strings.TrimSpace(inner)
		// Simple path: only dots and word characters, no spaces or pipes
		if !strings.ContainsAny(inner, " \t|()") {
			snap := rc.Snapshot()
			val := lookupPath(snap, strings.Split(inner, "."))
			if val != nil {
				return val, nil
			}
			// Path exists but is nil/zero — still return empty string
			return "", nil
		}
	}

	// Fall back to string rendering for complex templates
	return rc.Render(tmplStr)
}

// RenderMap renders all values in a template map, preserving types via RenderValue.
// Simple references like "{{.step.field}}" return the raw typed value (slice, map, number).
// Complex templates like "https://{{.host}}/{{.path}}" return strings.
func (rc *RunContext) RenderMap(m map[string]string) (map[string]any, error) {
	result := make(map[string]any, len(m))
	for k, tmpl := range m {
		v, err := rc.RenderValue(tmpl)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", k, err)
		}
		result[k] = v
	}
	return result, nil
}

// RenderBool renders a template and checks if the result is "true"
func (rc *RunContext) RenderBool(tmplStr string) (bool, error) {
	result, err := rc.Render(tmplStr)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(result) == "true", nil
}

// RenderSlice looks up a value in context expected to be a slice.
// tmplStr should be a simple dot-path like "{{.step_id.field}}" or just "step_id.field".
func (rc *RunContext) RenderSlice(tmplStr string) ([]any, error) {
	key := strings.TrimSpace(tmplStr)
	key = strings.TrimPrefix(key, "{{")
	key = strings.TrimSuffix(key, "}}")
	key = strings.TrimSpace(key)
	key = strings.TrimPrefix(key, ".")

	snap := rc.Snapshot()
	val := lookupPath(snap, strings.Split(key, "."))
	if val == nil {
		return nil, fmt.Errorf("value at %q not found in context", tmplStr)
	}

	switch v := val.(type) {
	case []any:
		return v, nil
	case []string:
		result := make([]any, len(v))
		for i, s := range v {
			result[i] = s
		}
		return result, nil
	case []map[string]any:
		result := make([]any, len(v))
		for i, m := range v {
			result[i] = m
		}
		return result, nil
	default:
		return nil, fmt.Errorf("value at %q is not a slice (got %T)", tmplStr, val)
	}
}

// Clone returns a copy of the RunContext — used for loop/parallel step scoping
func (rc *RunContext) Clone() *RunContext {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	clone := &RunContext{
		RunID: rc.RunID,
		data:  make(map[string]any, len(rc.data)),
	}
	for k, v := range rc.data {
		clone.data[k] = v
	}
	return clone
}

// MergeFrom copies all keys from another context into this one
func (rc *RunContext) MergeFrom(other *RunContext) {
	other.mu.RLock()
	defer other.mu.RUnlock()
	rc.mu.Lock()
	defer rc.mu.Unlock()
	for k, v := range other.data {
		rc.data[k] = v
	}
}

func lookupPath(m map[string]any, parts []string) any {
	if len(parts) == 0 {
		return nil
	}
	v, ok := m[parts[0]]
	if !ok {
		return nil
	}
	if len(parts) == 1 {
		return v
	}
	if nested, ok := v.(map[string]any); ok {
		return lookupPath(nested, parts[1:])
	}
	return nil
}

// RenderSystemPrompt renders template expressions in a system prompt string.
// inputs is available as dot (.) in the template, so {{.available_nodes}} etc. work.
// Fails silently: returns the original string on any error.
func RenderSystemPrompt(text string, inputs map[string]any) string {
	if !strings.Contains(text, "{{") {
		return text
	}
	funcMap := template.FuncMap{
		"now":      func() string { return time.Now().Format(time.RFC3339) },
		"date":     func() string { return time.Now().Format("2006-01-02") },
		"datetime": func() string { return time.Now().Format("2006-01-02 15:04:05") },
		"unix":     func() string { return fmt.Sprintf("%d", time.Now().UnixNano()) },
		"toJSON":   toJSON,
		"default":  defaultVal,
	}
	t, err := template.New("").Funcs(funcMap).Parse(text)
	if err != nil {
		return text
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, inputs); err != nil {
		return text
	}
	return buf.String()
}

// --- template helper functions ---

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case string:
		var f float64
		fmt.Sscanf(n, "%f", &f)
		return f
	}
	return 0
}

func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case string:
		var i int
		fmt.Sscanf(n, "%d", &i)
		return i
	}
	return 0
}

func toJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func fromJSON(s string) any {
	var out any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return s
	}
	return out
}

func defaultVal(v any, fallback any) any {
	if v == nil || v == "" {
		return fallback
	}
	return v
}
