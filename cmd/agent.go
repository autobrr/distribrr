package cmd

import (
	"github.com/autobrr/distribrr/pkg/agent"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

func CommandAgent() *cobra.Command {
	var command = &cobra.Command{
		Use:          "agent",
		Short:        "agent subcommands",
		Example:      `  distribrr agent`,
		SilenceUsage: false,
	}

	command.AddCommand(CommandAgentRun())

	return command
}

func CommandAgentRun() *cobra.Command {
	var command = &cobra.Command{
		Use:          "run",
		Short:        "Run agent",
		Example:      `  distribrr agent run`,
		SilenceUsage: false,
	}

	//cfg := config.New()
	var configPath string

	cfg := agent.NewConfig()

	command.Flags().StringVar(&configPath, "config-file", "", "Path to config file")
	command.Flags().StringVar(&cfg.Http.Host, "http-host", "", "HTTP Host. Default: localhost")
	command.Flags().StringVar(&cfg.Http.Port, "http-port", "7430", "HTTP port. Default: 7422")
	command.Flags().StringVar(&cfg.Http.Token, "http-api-token", "", "Api token")

	command.Run = func(cmd *cobra.Command, args []string) {
		if err := cfg.LoadFromFile(configPath); err != nil {
			log.Fatal().Err(err).Msgf("could not load config from file: %s", configPath)
		}

		app := agent.NewService(cfg)
		app.Run()
	}

	return command
}
