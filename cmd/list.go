package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/murdinc/lcdata/internal/lcdata"
	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all nodes",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := lcdata.LoadConfig()
		if err != nil {
			return err
		}

		nodes, err := lcdata.LoadNodes(cfg.NodesPath)
		if err != nil {
			return err
		}

		if len(nodes) == 0 {
			fmt.Printf("No nodes found in %s\n", cfg.NodesPath)
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tTYPE\tDESCRIPTION")
		fmt.Fprintln(w, "----\t----\t-----------")
		for _, n := range nodes {
			fmt.Fprintf(w, "%s\t%s\t%s\n", n.Name, n.Type, n.Description)
		}
		return w.Flush()
	},
}
