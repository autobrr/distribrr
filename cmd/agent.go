package cmd

import (
	"github.com/autobrr/distribrr/pkg/agent"

	"github.com/pkg/errors"
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
		SilenceUsage: true,
	}

	var configPath string

	cfg := agent.NewConfig()

	command.Flags().StringVar(&configPath, "config-file", "", "Path to config file")
	command.Flags().StringVar(&cfg.Http.Host, "http-host", "", "HTTP host to listen on; empty listens on all interfaces")
	command.Flags().StringVar(&cfg.Http.Port, "http-port", "7430", "HTTP port")
	command.Flags().StringVar(&cfg.Http.Token, "http-api-token", "", "API token, see distribrr token generate")

	command.RunE = func(cmd *cobra.Command, args []string) error {
		if err := cfg.LoadFromFile(configPath); err != nil {
			return errors.Wrapf(err, "could not load config from file: %s", configPath)
		}

		if err := cfg.Validate(); err != nil {
			return errors.Wrap(err, "invalid config")
		}

		app := agent.NewService(cfg)
		if err := app.Run(); err != nil {
			return errors.Wrap(err, "could not run agent")
		}

		return nil
	}

	return command
}
