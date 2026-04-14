package cmd

import (
	"fmt"

	"github.com/murdinc/lcdata/internal/lcdata"
	"github.com/spf13/cobra"
)

var graphCmd = &cobra.Command{
	Use:   "graph [node]",
	Short: "Print node dependency/flow tree",
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

		printGraph(node, nodes, 0, "")
		return nil
	},
}

func printGraph(node *lcdata.Node, nodes lcdata.Nodes, depth int, prefix string) {
	icon := nodeIcon(node.Type)
	fmt.Printf("%s%s %s  (%s)\n", prefix, icon, node.Name, node.Type)

	if node.Type != lcdata.NodeTypePipeline {
		return
	}

	for i, step := range node.Steps {
		isLast := i == len(node.Steps)-1
		connector := "├──"
		childPrefix := prefix + "│   "
		if isLast {
			connector = "└──"
			childPrefix = prefix + "    "
		}

		switch {
		case step.Node != "":
			child, err := nodes.Get(step.Node)
			if err != nil {
				fmt.Printf("%s%s [%s] → %s\n", prefix, connector, step.ID, step.Node)
				continue
			}
			fmt.Printf("%s%s [%s]\n", prefix, connector, step.ID)
			printGraph(child, nodes, depth+1, childPrefix)

		case step.Switch != "":
			fmt.Printf("%s%s [%s] switch: %s\n", prefix, connector, step.ID, step.Switch)
			casePrefix := childPrefix
			caseKeys := make([]string, 0)
			for k := range step.Cases {
				caseKeys = append(caseKeys, k)
			}
			for j, k := range caseKeys {
				c := step.Cases[k]
				isLastCase := j == len(caseKeys)-1
				caseConn := "├──"
				casePfx := casePrefix + "│   "
				if isLastCase {
					caseConn = "└──"
					casePfx = casePrefix + "    "
				}
				child, err := nodes.Get(c.Node)
				if err != nil {
					fmt.Printf("%s%s case %q → %s\n", casePrefix, caseConn, k, c.Node)
					continue
				}
				fmt.Printf("%s%s case %q\n", casePrefix, caseConn, k)
				printGraph(child, nodes, depth+2, casePfx)
			}

		case len(step.Parallel) > 0:
			fmt.Printf("%s%s [%s] parallel (%d branches)\n", prefix, connector, step.ID, len(step.Parallel))
			for j, b := range step.Parallel {
				isLastBranch := j == len(step.Parallel)-1
				bConn := "├──"
				bPfx := childPrefix + "│   "
				if isLastBranch {
					bConn = "└──"
					bPfx = childPrefix + "    "
				}
				child, err := nodes.Get(b.Node)
				if err != nil {
					fmt.Printf("%s%s branch %q → %s\n", childPrefix, bConn, b.ID, b.Node)
					continue
				}
				fmt.Printf("%s%s branch %q\n", childPrefix, bConn, b.ID)
				printGraph(child, nodes, depth+2, bPfx)
			}

		case step.Loop != nil:
			fmt.Printf("%s%s [%s] loop (max %d, until: %s)\n",
				prefix, connector, step.ID, step.Loop.MaxIterations, step.Loop.Until)
			for j, innerStep := range step.Loop.Steps {
				isLastInner := j == len(step.Loop.Steps)-1
				iConn := "├──"
				iPfx := childPrefix + "│   "
				if isLastInner {
					iConn = "└──"
					iPfx = childPrefix + "    "
				}
				if innerStep.Node != "" {
					child, err := nodes.Get(innerStep.Node)
					if err != nil {
						fmt.Printf("%s%s [%s] → %s\n", childPrefix, iConn, innerStep.ID, innerStep.Node)
						continue
					}
					fmt.Printf("%s%s [%s]\n", childPrefix, iConn, innerStep.ID)
					printGraph(child, nodes, depth+2, iPfx)
				}
			}

		case step.Map != nil:
			fmt.Printf("%s%s [%s] map over %s → %s (concurrency: %d)\n",
				prefix, connector, step.ID, step.Map.Over, step.Map.Node, step.Map.Concurrency)
			child, err := nodes.Get(step.Map.Node)
			if err == nil {
				printGraph(child, nodes, depth+2, childPrefix)
			}
		}
	}
}

func nodeIcon(t lcdata.NodeType) string {
	icons := map[lcdata.NodeType]string{
		lcdata.NodeTypeLLM:       "◆",
		lcdata.NodeTypeSTT:       "◎",
		lcdata.NodeTypeTTS:       "◉",
		lcdata.NodeTypeCommand:   "▶",
		lcdata.NodeTypeDatabase:  "▣",
		lcdata.NodeTypeHTTP:      "◈",
		lcdata.NodeTypeTransform: "◇",
		lcdata.NodeTypePipeline:  "◼",
		lcdata.NodeTypeSearch:    "⊕",
		lcdata.NodeTypeFile:      "▤",
	}
	if icon, ok := icons[t]; ok {
		return icon
	}
	return "○"
}

