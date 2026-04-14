package lcdata

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// Runner executes nodes and pipelines
type Runner struct {
	nodes  Nodes
	envCfg EnvironmentConfigs
	cfg    *Config

	mu   sync.RWMutex
	runs map[string]*Run
}

// NewRunner creates a runner with loaded nodes and environment
func NewRunner(nodes Nodes, envCfg EnvironmentConfigs, cfg *Config) *Runner {
	return &Runner{
		nodes:  nodes,
		envCfg: envCfg,
		cfg:    cfg,
		runs:   make(map[string]*Run),
	}
}

// Start begins executing a node asynchronously, returns the Run record immediately
func (r *Runner) Start(ctx context.Context, req RunRequest, nodeName string) (*Run, error) {
	node, err := r.nodes.Get(nodeName)
	if err != nil {
		return nil, err
	}

	envName := req.Env
	if envName == "" {
		envName = r.cfg.Env
	}

	env, err := r.envCfg.GetEnvironment(envName)
	if err != nil {
		return nil, fmt.Errorf("environment %q: %w", envName, err)
	}

	runID := req.RunID
	if runID == "" {
		runID = newRunID()
	}

	run := &Run{
		ID:        runID,
		Node:      nodeName,
		Env:       envName,
		Status:    RunStatusPending,
		Input:     req.Input,
		StartedAt: time.Now(),
		Events:    make(chan Event, 256),
	}

	runCtx, cancel := context.WithTimeout(ctx, r.cfg.RunTimeoutDuration)
	run.Cancel = cancel

	r.mu.Lock()
	r.runs[runID] = run
	// Trim history
	if len(r.runs) > r.cfg.RunHistory {
		r.evictOldest()
	}
	r.mu.Unlock()

	go func() {
		defer cancel()
		defer close(run.Events)

		run.Status = RunStatusRunning
		run.Events <- Event{
			Event:     EventRunStarted,
			RunID:     runID,
			Node:      nodeName,
			Timestamp: time.Now(),
		}

		rc := NewRunContext(runID, req.Input)
		output, steps, err := r.executeNode(runCtx, node, rc, env, run.Events)

		run.EndedAt = time.Now()
		run.DurationMS = run.EndedAt.Sub(run.StartedAt).Milliseconds()
		run.Steps = steps

		if err != nil {
			if runCtx.Err() == context.Canceled {
				run.Status = RunStatusCancelled
				run.Events <- Event{
					Event:     EventRunCancelled,
					RunID:     runID,
					DurationMS: run.DurationMS,
					Timestamp: time.Now(),
				}
			} else {
				run.Status = RunStatusFailed
				run.Error = err.Error()
				run.Events <- Event{
					Event:     EventRunFailed,
					RunID:     runID,
					Error:     err.Error(),
					DurationMS: run.DurationMS,
					Timestamp: time.Now(),
				}
			}
			return
		}

		run.Status = RunStatusCompleted
		run.Output = output
		run.Events <- Event{
			Event:      EventRunCompleted,
			RunID:      runID,
			Output:     output,
			DurationMS: run.DurationMS,
			Timestamp:  time.Now(),
		}
	}()

	return run, nil
}

// RunSync executes a node and waits for completion, returning the run record
func (r *Runner) RunSync(ctx context.Context, req RunRequest, nodeName string) (*Run, error) {
	run, err := r.Start(ctx, req, nodeName)
	if err != nil {
		return nil, err
	}

	// Drain the events channel to let the goroutine complete
	for range run.Events {
	}

	return run, nil
}

// GetRun retrieves a run record by ID
func (r *Runner) GetRun(id string) (*Run, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	run, ok := r.runs[id]
	if !ok {
		return nil, fmt.Errorf("run not found: %s", id)
	}
	return run, nil
}

// ListRuns returns all recent run records
func (r *Runner) ListRuns() []*Run {
	r.mu.RLock()
	defer r.mu.RUnlock()
	runs := make([]*Run, 0, len(r.runs))
	for _, run := range r.runs {
		runs = append(runs, run)
	}
	return runs
}

// CancelRun cancels an in-progress run
func (r *Runner) CancelRun(id string) error {
	r.mu.RLock()
	run, ok := r.runs[id]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("run not found: %s", id)
	}
	if run.Cancel != nil {
		run.Cancel()
	}
	return nil
}

// executeNode dispatches to the appropriate executor based on node type
func (r *Runner) executeNode(
	ctx context.Context,
	node *Node,
	rc *RunContext,
	env EnvironmentConfig,
	events chan<- Event,
) (map[string]any, []StepResult, error) {

	if node.Type == NodeTypePipeline {
		return executePipeline(ctx, node, rc, r.nodes, env, events)
	}

	started := time.Now()
	output, err := executeLeafNode(ctx, node, rc, env, events)
	dur := time.Since(started).Milliseconds()

	step := StepResult{
		ID:         node.Name,
		Node:       node.Name,
		StartedAt:  started,
		EndedAt:    time.Now(),
		DurationMS: dur,
	}
	if err != nil {
		step.Status = RunStatusFailed
		step.Error = err.Error()
	} else {
		step.Status = RunStatusCompleted
		step.Output = output
	}

	return output, []StepResult{step}, err
}

func newRunID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (r *Runner) evictOldest() {
	var oldestID string
	var oldestTime time.Time
	for id, run := range r.runs {
		if oldestID == "" || run.StartedAt.Before(oldestTime) {
			oldestID = id
			oldestTime = run.StartedAt
		}
	}
	if oldestID != "" {
		delete(r.runs, oldestID)
	}
}
