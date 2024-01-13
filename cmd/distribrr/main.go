package main

import (
	"os"
	"time"

	"github.com/autobrr/distribrr/cmd"
	"github.com/autobrr/distribrr/pkg/version"

	"github.com/blang/semver"
	"github.com/rhysd/go-github-selfupdate/selfupdate"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/rs/zerolog/pkgerrors"
	"github.com/spf13/cobra"
)

//var k = koanf.New(".")

const usage = `
·▄▄▄▄  ▪  .▄▄ · ▄▄▄▄▄▄▄▄  ▪  ▄▄▄▄· ▄▄▄  ▄▄▄  
██▪ ██ ██ ▐█ ▀. •██  ▀▄ █·██ ▐█ ▀█▪▀▄ █·▀▄ █·
▐█· ▐█▌▐█·▄▀▀▀█▄ ▐█.▪▐▀▀▄ ▐█·▐█▀▀█▄▐▀▀▄ ▐▀▀▄ 
██. ██ ▐█▌▐█▄▪▐█ ▐█▌·▐█•█▌▐█▌██▄▪▐█▐█•█▌▐█•█▌
▀▀▀▀▀• ▀▀▀ ▀▀▀▀  ▀▀▀ .▀  ▀▀▀▀·▀▀▀▀ .▀  ▀.▀  ▀

A companion tool for autobrr to distribute downloads.
`

func main() {
	// setup logger
	zerolog.TimeFieldFormat = time.RFC3339
	zerolog.ErrorStackMarshaler = pkgerrors.MarshalStack
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})

	var rootCmd = &cobra.Command{
		Use:   "distribrr",
		Short: "distribrr",
		Long:  usage,
	}

	rootCmd.AddCommand(cmd.CommandServer())
	rootCmd.AddCommand(cmd.CommandAgent())

	rootCmd.AddCommand(CmdVersion())
	rootCmd.AddCommand(CmdUpdate())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func CmdVersion() *cobra.Command {
	var output string
	var command = &cobra.Command{
		Use:   "version",
		Short: "Print version info",
		Example: `  distribrr version
  distribrr version --output json`,
		SilenceUsage: false,
	}

	command.Flags().StringVar(&output, "output", "text", "Print as [text, json]")

	command.Run = func(cmd *cobra.Command, args []string) {
		version.Info.Print(output)
	}

	return command
}

func CmdUpdate() *cobra.Command {
	var command = &cobra.Command{
		Use:          "update",
		Short:        "Update distribrr to latest version",
		Example:      `  distribrr update`,
		SilenceUsage: false,
	}

	var verbose bool

	command.Flags().BoolVar(&verbose, "verbose", false, "Verbose output: Print changelog")

	command.Run = func(cmd *cobra.Command, args []string) {
		v, err := semver.ParseTolerant(version.Version)
		if err != nil {
			log.Error().Err(err).Msgf("could not parse version: %s", version.Version)
			return
		}

		latest, err := selfupdate.UpdateSelf(v, "autobrr/distribrr")
		if err != nil {
			log.Error().Err(err).Msg("Binary update failed")
			return
		}

		if latest.Version.Equals(v) {
			// latest version is the same as current version. It means current binary is up-to-date.
			log.Info().Msgf("Current binary is the latest version: %s", version.Version)
		} else {
			log.Info().Msgf("Successfully updated to version: %s", latest.Version)

			if verbose {
				log.Info().Msgf("Release note: %s", latest.ReleaseNotes)
			}
		}

		return
	}

	return command
}
