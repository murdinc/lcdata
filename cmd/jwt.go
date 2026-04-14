package cmd

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/murdinc/lcdata/internal/lcdata"
	"github.com/spf13/cobra"
)

var jwtClient string

var jwtCmd = &cobra.Command{
	Use:   "generate-jwt",
	Short: "Generate a JWT token for a client service",
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

		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		signed, err := token.SignedString([]byte(cfg.JWTSecret))
		if err != nil {
			return fmt.Errorf("failed to sign token: %w", err)
		}

		fmt.Printf("JWT token for client %q:\n%s\n", jwtClient, signed)
		return nil
	},
}

func init() {
	jwtCmd.Flags().StringVar(&jwtClient, "client", "default", "Client name to embed in the token")
}
