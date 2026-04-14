package lcdata

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func executeCommand(
	ctx context.Context,
	node *Node,
	inputs map[string]any,
	rc *RunContext,
	env EnvironmentConfig,
	events chan<- Event,
) (map[string]any, error) {

	// Render command args and env values against context
	args := make([]string, len(node.Args))
	for i, a := range node.Args {
		rendered, err := rc.Render(a)
		if err != nil {
			return nil, fmt.Errorf("arg %d: %w", i, err)
		}
		args[i] = rendered
	}

	cmd := exec.CommandContext(ctx, node.Command, args...)

	// Render env values
	for k, v := range node.Env {
		rendered, err := rc.Render(v)
		if err != nil {
			return nil, fmt.Errorf("env %s: %w", k, err)
		}
		cmd.Env = append(cmd.Env, k+"="+rendered)
	}

	// Set working directory to node directory if it has scripts
	if node.Directory != "" {
		cmd.Dir = node.Directory
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start command: %w", err)
	}

	var stdoutLines []string
	var stderrLines []string

	// Stream stdout line by line
	stdoutScanner := bufio.NewScanner(stdout)
	for stdoutScanner.Scan() {
		line := stdoutScanner.Text()
		stdoutLines = append(stdoutLines, line)
		events <- Event{
			Event:     EventChunk,
			RunID:     rc.RunID,
			StepID:    node.Name,
			Data:      line,
			Timestamp: time.Now(),
		}
	}

	// Collect stderr
	stderrScanner := bufio.NewScanner(stderr)
	for stderrScanner.Scan() {
		stderrLines = append(stderrLines, stderrScanner.Text())
	}

	err = cmd.Wait()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("command error: %w", err)
		}
	}

	return map[string]any{
		"stdout":    strings.Join(stdoutLines, "\n"),
		"stderr":    strings.Join(stderrLines, "\n"),
		"exit_code": exitCode,
	}, nil
}
