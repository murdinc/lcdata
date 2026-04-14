package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "lcdata",
	Short: "lcdata — agentic LLM execution engine",
	Long: `lcdata (Lieutenant Commander Data) is a declarative agentic execution engine.

Each node is a directory in nodes/ with a JSON config. Nodes compose into
pipelines with typed data flow, conditional branching, parallel execution,
loops, and fan-out. The server exposes a REST + WebSocket API so remote
services can discover and execute nodes.`,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(showCmd)
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(validateCmd)
	rootCmd.AddCommand(graphCmd)
	rootCmd.AddCommand(jwtCmd)
	rootCmd.AddCommand(versionCmd)
}
