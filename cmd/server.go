package cmd

import (
	"github.com/autobrr/distribrr/pkg/server"

	"github.com/pkg/errors"
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
		SilenceUsage: true,
	}

	var configPath string

	cfg := server.NewConfig()

	command.Flags().StringVar(&configPath, "config-file", "", "Path to config file")
	command.Flags().StringVar(&cfg.Http.Host, "http-host", "", "HTTP Host. Default: localhost")
	command.Flags().StringVar(&cfg.Http.Port, "http-port", "7422", "HTTP port. Default: 7422")
	command.Flags().StringVar(&cfg.Http.Token, "http-api-token", "", "API token")

	command.RunE = func(cmd *cobra.Command, args []string) error {
		if err := cfg.LoadFromFile(configPath); err != nil {
			return errors.Wrapf(err, "could not load config from file: %s", configPath)
		}

		if cfg.Http.Token == "" {
			return errors.New("http.token must be set; refusing to start without an API token")
		}

		app := server.NewService(cfg)
		if err := app.Run(); err != nil {
			return errors.Wrap(err, "could not run server")
		}

		return nil
	}

	return command
}
