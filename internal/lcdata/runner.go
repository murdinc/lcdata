package lcdata

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Runner executes nodes and pipelines
type Runner struct {
	nodes  Nodes
	envCfg EnvironmentConfigs
	cfg    *Config
	log    *slog.Logger
	store  *Store

	mu   sync.RWMutex
	runs map[string]*Run // in-flight runs only
}

// NewRunner creates a runner with loaded nodes and environment
func NewRunner(nodes Nodes, envCfg EnvironmentConfigs, cfg *Config, log *slog.Logger) *Runner {
	if log == nil {
		log = NewLogger("info")
	}
	return &Runner{
		nodes:  nodes,
		envCfg: envCfg,
		cfg:    cfg,
		log:    log,
		runs:   make(map[string]*Run),
	}
}

// SetStore attaches a persistent store. Call before serving.
func (r *Runner) SetStore(s *Store) {
	r.store = s
}

// Nodes returns the current node registry snapshot (safe for concurrent use).
func (r *Runner) Nodes() Nodes {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.nodes
}

// ReloadNodes atomically replaces the node registry.
func (r *Runner) ReloadNodes(nodes Nodes) {
	r.mu.Lock()
	r.nodes = nodes
	r.mu.Unlock()
}

// Start begins executing a node asynchronously, returns the Run record immediately
func (r *Runner) Start(ctx context.Context, req RunRequest, nodeName string) (*Run, error) {
	r.mu.RLock()
	nodes := r.nodes
	r.mu.RUnlock()

	node, err := nodes.Get(nodeName)
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

	// Validate inputs against the node's declared schema
	if err := validateInputs(node, req.Input); err != nil {
		return nil, err
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
	r.mu.Unlock()

	go func() {
		defer cancel()
		defer close(run.Events)

		r.log.Info("run started", "run_id", runID, "node", nodeName, "env", envName)

		run.Status = RunStatusRunning
		run.Events <- Event{
			Event:     EventRunStarted,
			RunID:     runID,
			Node:      nodeName,
			Timestamp: time.Now(),
		}

		rc := NewRunContext(runID, req.Input)
		output, steps, err := r.executeNode(runCtx, node, rc, env, run.Events, nodes)

		run.EndedAt = time.Now()
		run.DurationMS = run.EndedAt.Sub(run.StartedAt).Milliseconds()
		run.Steps = steps

		// Aggregate token costs from all steps
		for _, s := range steps {
			run.InputTokens += s.InputTokens
			run.OutputTokens += s.OutputTokens
		}

		if err != nil {
			if runCtx.Err() == context.Canceled {
				run.Status = RunStatusCancelled
				r.log.Info("run cancelled", "run_id", runID, "node", nodeName, "duration_ms", run.DurationMS)
				run.Events <- Event{
					Event:      EventRunCancelled,
					RunID:      runID,
					DurationMS: run.DurationMS,
					Timestamp:  time.Now(),
				}
			} else {
				run.Status = RunStatusFailed
				run.Error = err.Error()
				r.log.Error("run failed", "run_id", runID, "node", nodeName, "duration_ms", run.DurationMS, "error", err.Error())
				run.Events <- Event{
					Event:      EventRunFailed,
					RunID:      runID,
					Error:      err.Error(),
					DurationMS: run.DurationMS,
					Timestamp:  time.Now(),
				}
			}
		} else {
			run.Status = RunStatusCompleted
			run.Output = output
			r.log.Info("run completed",
				"run_id", runID,
				"node", nodeName,
				"duration_ms", run.DurationMS,
				"input_tokens", run.InputTokens,
				"output_tokens", run.OutputTokens,
			)
			run.Events <- Event{
				Event:      EventRunCompleted,
				RunID:      runID,
				Output:     output,
				DurationMS: run.DurationMS,
				Timestamp:  time.Now(),
			}
		}

		// Persist to store (best-effort)
		if r.store != nil {
			if serr := r.store.SaveRun(run); serr != nil {
				r.log.Error("failed to persist run", "run_id", runID, "error", serr)
			}
		}

		// Remove from in-flight map — now in store
		r.mu.Lock()
		delete(r.runs, runID)
		r.mu.Unlock()
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

// GetRun retrieves a run record by ID — checks in-flight first, then store.
func (r *Runner) GetRun(id string) (*Run, error) {
	r.mu.RLock()
	run, ok := r.runs[id]
	r.mu.RUnlock()
	if ok {
		return run, nil
	}
	if r.store != nil {
		return r.store.GetRun(id)
	}
	return nil, fmt.Errorf("run not found: %s", id)
}

// ListRuns returns recent runs from the store, merged with any in-flight runs.
func (r *Runner) ListRuns() []*Run {
	r.mu.RLock()
	inFlight := make([]*Run, 0, len(r.runs))
	inFlightIDs := make(map[string]bool, len(r.runs))
	for id, run := range r.runs {
		inFlight = append(inFlight, run)
		inFlightIDs[id] = true
	}
	r.mu.RUnlock()

	// Merge with persisted runs
	var all []*Run
	all = append(all, inFlight...)
	if r.store != nil {
		persisted, err := r.store.ListRuns(r.cfg.RunHistory)
		if err == nil {
			for _, run := range persisted {
				if !inFlightIDs[run.ID] {
					all = append(all, run)
				}
			}
		}
	}
	return all
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
	nodes Nodes,
) (map[string]any, []StepResult, error) {

	if node.Type == NodeTypePipeline {
		return executePipeline(ctx, node, rc, nodes, env, events)
	}

	started := time.Now()
	output, err := executeLeafNode(ctx, node, rc, env, events, nodes)
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

