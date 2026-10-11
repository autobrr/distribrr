package cmd

import (
	"fmt"

	"github.com/autobrr/distribrr/pkg/token"

	"github.com/spf13/cobra"
)

func CommandToken() *cobra.Command {
	var command = &cobra.Command{
		Use:          "token",
		Short:        "token subcommands",
		Example:      `  distribrr token generate`,
		SilenceUsage: false,
	}

	command.AddCommand(CommandTokenGenerate())

	return command
}

func CommandTokenGenerate() *cobra.Command {
	var command = &cobra.Command{
		Use:   "generate",
		Short: "Generate an API token for http.token",
		Long: `Generate a random API token. Set it as http.token in the server config and as
manager.token in every agent config. Each agent's own http.token needs a token too.`,
		Example:      `  distribrr token generate`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
	}

	command.Run = func(cmd *cobra.Command, args []string) {
		fmt.Fprintln(cmd.OutOrStdout(), token.Generate())
	}

	return command
}
