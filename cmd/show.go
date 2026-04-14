package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/murdinc/lcdata/internal/lcdata"
	"github.com/spf13/cobra"
)

var showCmd = &cobra.Command{
	Use:   "show [node]",
	Short: "Show node config",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := lcdata.LoadConfig()
		if err != nil {
			return err
		}

		nodes, err := lcdata.LoadNodes(cfg.NodesPath)
		if err != nil {
			return err
		}

		node, err := nodes.Get(args[0])
		if err != nil {
			return err
		}

		data, err := json.MarshalIndent(node, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	},
}
