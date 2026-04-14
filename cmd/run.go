package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/murdinc/lcdata/internal/lcdata"
	"github.com/spf13/cobra"
)

var (
	runInputs []string
	runEnv    string
	runID     string
)

var runCmd = &cobra.Command{
	Use:   "run [node]",
	Short: "Run a node locally",
	Long: `Run a node and print the output. No server required.

Input values are passed as key=value pairs:
  lcdata run my_node --input message="hello world" --input topic=AI`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := lcdata.LoadConfig()
		if err != nil {
			return err
		}

		nodes, err := lcdata.LoadNodes(cfg.NodesPath)
		if err != nil {
			return err
		}

		envCfg, err := lcdata.LoadEnvironmentConfigs()
		if err != nil {
			return err
		}

		envName := runEnv
		if envName == "" {
			envName = cfg.Env
		}

		// Parse --input key=value flags
		inputs := make(map[string]any)
		for _, kv := range runInputs {
			parts := strings.SplitN(kv, "=", 2)
			if len(parts) != 2 {
				return fmt.Errorf("invalid input format %q — use key=value", kv)
			}
			inputs[parts[0]] = parts[1]
		}

		runner := lcdata.NewRunner(nodes, envCfg, cfg)

		req := lcdata.RunRequest{
			Input: inputs,
			Env:   envName,
			RunID: runID,
		}

		// Stream events to stdout
		run, err := runner.Start(context.Background(), req, args[0])
		if err != nil {
			return err
		}

		for event := range run.Events {
			switch event.Event {
			case lcdata.EventChunk:
				fmt.Print(event.Data)
			case lcdata.EventStepStarted:
				fmt.Printf("\n[%s] starting %s...\n", event.StepID, event.Node)
			case lcdata.EventStepCompleted:
				fmt.Printf("[%s] completed (%dms)\n", event.StepID, event.DurationMS)
			case lcdata.EventStepFailed:
				fmt.Printf("[%s] FAILED: %s\n", event.StepID, event.Error)
			case lcdata.EventLoopIteration:
				fmt.Printf("[%s] loop iteration %d\n", event.StepID, event.Iteration)
			case lcdata.EventMapProgress:
				fmt.Printf("[%s] map %d/%d\n", event.StepID, event.Progress, event.Total)
			case lcdata.EventRunCompleted:
				fmt.Printf("\n\nCompleted in %dms\n", event.DurationMS)
			case lcdata.EventRunFailed:
				fmt.Printf("\nFailed: %s\n", event.Error)
			}
		}

		if run.Status == lcdata.RunStatusCompleted && run.Output != nil {
			fmt.Println("\nOutput:")
			data, _ := json.MarshalIndent(run.Output, "", "  ")
			fmt.Println(string(data))
		}

		if run.Status == lcdata.RunStatusFailed {
			return fmt.Errorf(run.Error)
		}

		return nil
	},
}

func init() {
	runCmd.Flags().StringArrayVarP(&runInputs, "input", "i", nil, "Input values as key=value")
	runCmd.Flags().StringVarP(&runEnv, "env", "e", "", "Environment name (default from lcdata.json)")
	runCmd.Flags().StringVar(&runID, "run-id", "", "Custom run ID")
}
