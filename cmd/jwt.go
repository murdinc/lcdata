package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/murdinc/lcdata/internal/lcdata"
	"github.com/spf13/cobra"
)

var (
	jwtClient     string
	jwtAllowNodes []string
)

var jwtCmd = &cobra.Command{
	Use:   "generate-jwt",
	Short: "Generate a JWT token for a client service",
	Long: `Generate a signed JWT token for a client.

By default the token grants access to all nodes. Use --allow to restrict:
  lcdata generate-jwt --client my-service --allow "llm_chat,summarize"
  lcdata generate-jwt --client reader --allow "read_file"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := lcdata.LoadConfig()
		if err != nil {
			return err
		}

		if cfg.JWTSecret == "" || cfg.JWTSecret == "change-this-in-production" {
			return fmt.Errorf("set a real jwt_secret in lcdata.json before generating tokens")
		}

		claims := jwt.MapClaims{
			"iss":    "lcdata",
			"sub":    jwtClient,
			"client": jwtClient,
			"iat":    time.Now().Unix(),
		}

		if len(jwtAllowNodes) > 0 {
			// Flatten comma-separated values
			var nodes []string
			for _, n := range jwtAllowNodes {
				for _, part := range strings.Split(n, ",") {
					if t := strings.TrimSpace(part); t != "" {
						nodes = append(nodes, t)
					}
				}
			}
			claims["allowed_nodes"] = nodes
		}

		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		signed, err := token.SignedString([]byte(cfg.JWTSecret))
		if err != nil {
			return fmt.Errorf("failed to sign token: %w", err)
		}

		if len(jwtAllowNodes) > 0 {
			fmt.Printf("JWT token for client %q (restricted to: %s):\n%s\n",
				jwtClient, strings.Join(jwtAllowNodes, ", "), signed)
		} else {
			fmt.Printf("JWT token for client %q (all nodes):\n%s\n", jwtClient, signed)
		}
		return nil
	},
}

func init() {
	jwtCmd.Flags().StringVar(&jwtClient, "client", "default", "Client name to embed in the token")
	jwtCmd.Flags().StringArrayVar(&jwtAllowNodes, "allow", nil, "Node names this token may access (comma-separated, repeatable; default: all)")
}
