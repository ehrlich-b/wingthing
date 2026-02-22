package main

import (
	"fmt"

	"github.com/ehrlich-b/wingthing/internal/relay"
	"github.com/spf13/cobra"
)

func keygenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "keygen",
		Short: "Generate a JWT signing key (EC P-256)",
		Long:  "Generates an ECDSA P-256 private key for JWT signing and prints it as base64-DER.\nUse with: fly secrets set WT_JWT_KEY=<output>",
		RunE: func(cmd *cobra.Command, args []string) error {
			key, encoded, err := relay.GenerateECKey()
			if err != nil {
				return err
			}

			pubKey, err := relay.MarshalECPublicKey(&key.PublicKey)
			if err != nil {
				return err
			}

			fmt.Println(encoded)
			fmt.Fprintf(cmd.ErrOrStderr(), "\npublic key: %s\n", pubKey)
			return nil
		},
	}
}
