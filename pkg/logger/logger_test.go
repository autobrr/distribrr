package logger

import (
	"testing"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
)

func TestSetup_level(t *testing.T) {
	global, level, def := log.Logger, zerolog.GlobalLevel(), zerolog.DefaultContextLogger
	t.Cleanup(func() {
		log.Logger, zerolog.DefaultContextLogger = global, def
		zerolog.SetGlobalLevel(level)
	})

	tests := []struct {
		env  string
		want zerolog.Level
	}{
		{env: "", want: zerolog.InfoLevel},
		{env: "debug", want: zerolog.DebugLevel},
		{env: "TRACE", want: zerolog.TraceLevel},
		{env: "warn", want: zerolog.WarnLevel},
		{env: "verbose", want: zerolog.InfoLevel},
	}

	for _, tt := range tests {
		t.Run("LOG_LEVEL="+tt.env, func(t *testing.T) {
			t.Setenv("LOG_LEVEL", tt.env)

			Setup()

			assert.Equal(t, tt.want, zerolog.GlobalLevel())
		})
	}
}
