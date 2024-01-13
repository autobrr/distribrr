package cmd

import (
	"github.com/autobrr/distribrr/pkg/server"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

func CommandServer() *cobra.Command {
	var command = &cobra.Command{
		Use:          "server",
		Short:        "server subcommands",
		Example:      `  distribrr server`,
		SilenceUsage: false,
	}

	command.AddCommand(CommandServerRun())

	return command
}

func CommandServerRun() *cobra.Command {
	var command = &cobra.Command{
		Use:          "run",
		Short:        "Run server",
		Example:      `  distribrr server run`,
		SilenceUsage: false,
	}

	var configPath string

	cfg := server.NewConfig()

	command.Flags().StringVar(&configPath, "config-file", "", "Path to config file")
	command.Flags().StringVar(&cfg.Http.Host, "http-host", "", "HTTP Host. Default: localhost")
	command.Flags().StringVar(&cfg.Http.Port, "http-port", "7422", "HTTP port. Default: 7422")
	command.Flags().StringVar(&cfg.Http.Token, "http-api-token", "", "API token")

	command.Run = func(cmd *cobra.Command, args []string) {
		if err := cfg.LoadFromFile(configPath); err != nil {
			log.Fatal().Err(err).Msgf("could not load config from file: %s", configPath)
		}

		app := server.NewService(cfg)
		app.Run()
	}

	return command
}
