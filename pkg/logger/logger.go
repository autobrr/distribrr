package logger

import (
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/rs/zerolog/pkgerrors"
)

// Setup makes the global logger write to stdout at the level from LOG_LEVEL
// (trace, debug, info, warn, error; default info). log.Ctx falls back to it
// when a context carries no request logger.
func Setup() {
	zerolog.TimeFieldFormat = time.RFC3339
	zerolog.ErrorStackMarshaler = pkgerrors.MarshalStack

	log.Logger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}).With().Timestamp().Logger()
	zerolog.DefaultContextLogger = &log.Logger

	level := zerolog.InfoLevel
	if env := os.Getenv("LOG_LEVEL"); env != "" {
		parsed, err := zerolog.ParseLevel(env)
		if err != nil {
			log.Warn().Err(err).Msgf("invalid LOG_LEVEL '%s', using %s", env, level)
		} else {
			level = parsed
		}
	}

	zerolog.SetGlobalLevel(level)
}
