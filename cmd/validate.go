package cmd

import (
	"fmt"

	"github.com/murdinc/lcdata/internal/lcdata"
	"github.com/spf13/cobra"
)

var validateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate all node configs",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := lcdata.LoadConfig()
		if err != nil {
			return err
		}

		nodes, err := lcdata.LoadNodes(cfg.NodesPath)
		if err != nil {
			return err
		}

		errs := nodes.Validate()
		if len(errs) == 0 {
			fmt.Printf("All %d nodes valid\n", len(nodes))
			return nil
		}

		for _, e := range errs {
			fmt.Println("ERROR:", e)
		}
		return fmt.Errorf("%d validation error(s)", len(errs))
	},
}
