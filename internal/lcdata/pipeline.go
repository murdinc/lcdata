package lcdata

// Step is one element in a pipeline's steps array.
// Only one of Node, Switch, Parallel, Loop, or Map should be set.
type Step struct {
	ID string `json:"id"`

	// Simple node execution
	Node  string            `json:"node,omitempty"`
	Input map[string]string `json:"input,omitempty"`

	// Conditional branching
	Switch string                `json:"switch,omitempty"`
	Cases  map[string]SwitchCase `json:"cases,omitempty"`

	// Parallel execution
	Parallel []ParallelBranch `json:"parallel,omitempty"`

	// Loop until condition
	Loop *LoopConfig `json:"loop,omitempty"`

	// Fan-out over array
	Map *MapConfig `json:"map,omitempty"`

	// OnError: if set and this step fails, run this node instead of aborting.
	// The error message is available as input.error in the handler node.
	// The handler's output is written under this step's ID so downstream templates still work.
	OnError string `json:"on_error,omitempty"`
}

// SwitchCase is one branch of a switch step
type SwitchCase struct {
	Node  string            `json:"node"`
	Input map[string]string `json:"input,omitempty"`
}

// ParallelBranch is one concurrent branch in a parallel step
type ParallelBranch struct {
	ID    string            `json:"id"`
	Node  string            `json:"node"`
	Input map[string]string `json:"input,omitempty"`
}

// LoopConfig drives the loop step
type LoopConfig struct {
	// MaxIterations is a required safety cap
	MaxIterations int    `json:"max_iterations"`
	// Until is a Go template expression that evaluates to "true" to break
	Until         string `json:"until"`
	Steps         []Step `json:"steps"`
}

// MapConfig drives the map (fan-out) step
type MapConfig struct {
	// Over is a template expression resolving to a []any in context
	Over        string            `json:"over"`
	// As is the variable name for the current item in input templates
	As          string            `json:"as"`
	Node        string            `json:"node"`
	Input       map[string]string `json:"input,omitempty"`
	// CollectAs is the context key where []output is written
	CollectAs   string            `json:"collect_as"`
	// Concurrency controls parallel execution (0 or 1 = sequential)
	Concurrency int               `json:"concurrency,omitempty"`
}

// stepKind returns a human-readable label for what kind of step this is
func (s *Step) stepKind() string {
	switch {
	case s.Node != "":
		return "node:" + s.Node
	case s.Switch != "":
		return "switch"
	case len(s.Parallel) > 0:
		return "parallel"
	case s.Loop != nil:
		return "loop"
	case s.Map != nil:
		return "map"
	default:
		return "unknown"
	}
}
