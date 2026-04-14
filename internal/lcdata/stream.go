package lcdata

import (
	"encoding/json"
	"time"
)

// EventType identifies what happened in a run
type EventType string

const (
	EventRunStarted    EventType = "run_started"
	EventRunCompleted  EventType = "run_completed"
	EventRunFailed     EventType = "run_failed"
	EventRunCancelled  EventType = "run_cancelled"
	EventStepStarted   EventType = "step_started"
	EventStepCompleted EventType = "step_completed"
	EventStepFailed    EventType = "step_failed"
	EventChunk         EventType = "chunk"
	EventLoopIteration EventType = "loop_iteration"
	EventMapProgress   EventType = "map_progress"
	EventRetry         EventType = "retry"
)

// Event is a single streaming event emitted during a run
type Event struct {
	Event      EventType `json:"event"`
	RunID      string    `json:"run_id"`
	Node       string    `json:"node,omitempty"`
	StepID     string    `json:"step_id,omitempty"`
	Data       string    `json:"data,omitempty"`
	Output     any       `json:"output,omitempty"`
	Error      string    `json:"error,omitempty"`
	Iteration  int       `json:"iteration,omitempty"`
	Progress   int       `json:"progress,omitempty"`
	Total      int       `json:"total,omitempty"`
	DurationMS int64     `json:"duration_ms,omitempty"`
	Timestamp  time.Time `json:"timestamp"`
}

func (e Event) JSON() []byte {
	b, _ := json.Marshal(e)
	return b
}

// RunStatus tracks the state of an in-progress or completed run
type RunStatus string

const (
	RunStatusPending   RunStatus = "pending"
	RunStatusRunning   RunStatus = "running"
	RunStatusCompleted RunStatus = "completed"
	RunStatusFailed    RunStatus = "failed"
	RunStatusCancelled RunStatus = "cancelled"
)

// StepResult captures the outcome of a single pipeline step
type StepResult struct {
	ID         string    `json:"id"`
	Node       string    `json:"node"`
	Status     RunStatus `json:"status"`
	Output     any       `json:"output,omitempty"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	EndedAt    time.Time `json:"ended_at"`
	DurationMS int64     `json:"duration_ms"`

	// LLM token usage for this step
	InputTokens  int64 `json:"input_tokens,omitempty"`
	OutputTokens int64 `json:"output_tokens,omitempty"`
}

// Run is the full record of one execution
type Run struct {
	ID         string         `json:"run_id"`
	Node       string         `json:"node"`
	Env        string         `json:"env"`
	Status     RunStatus      `json:"status"`
	Input      map[string]any `json:"input"`
	Output     map[string]any `json:"output,omitempty"`
	Steps      []StepResult   `json:"steps,omitempty"`
	Error      string         `json:"error,omitempty"`
	StartedAt  time.Time      `json:"started_at"`
	EndedAt    time.Time      `json:"ended_at,omitempty"`
	DurationMS int64          `json:"duration_ms,omitempty"`

	// Token cost tracking (aggregated across all LLM steps)
	InputTokens  int64 `json:"input_tokens,omitempty"`
	OutputTokens int64 `json:"output_tokens,omitempty"`

	// Runtime-only: channel for subscribers to receive events
	Events chan Event `json:"-"`
	// Runtime-only: cancel function
	Cancel func() `json:"-"`
}

// RunRequest is the body of POST /api/nodes/{name}/run
type RunRequest struct {
	Input map[string]any `json:"input"`
	RunID string         `json:"run_id,omitempty"`
	Env   string         `json:"env,omitempty"`
}
