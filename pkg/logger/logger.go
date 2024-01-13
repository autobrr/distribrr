package logger

import (
	"context"
	"io"
	"os"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/pkgerrors"
	//"gopkg.in/natefinch/lumberjack.v2"
)

var once sync.Once

var log zerolog.Logger

func Get() zerolog.Logger {
	once.Do(func() {
		zerolog.ErrorStackMarshaler = pkgerrors.MarshalStack
		zerolog.TimeFieldFormat = time.RFC3339Nano

		logLevel, err := zerolog.ParseLevel(os.Getenv("LOG_LEVEL"))
		if err != nil {
			logLevel = zerolog.InfoLevel // default to INFO
		}

		var output io.Writer = zerolog.ConsoleWriter{
			Out:        os.Stdout,
			TimeFormat: time.RFC3339,
		}

		//if os.Getenv("APP_ENV") != "development" {
		//	fileLogger := &lumberjack.Logger{
		//		Filename:   "distribrr.log",
		//		MaxSize:    5, //
		//		MaxBackups: 10,
		//		MaxAge:     14,
		//		Compress:   true,
		//	}
		//
		//	output = zerolog.MultiLevelWriter(os.Stderr, fileLogger)
		//}

		log = zerolog.New(output).Level(logLevel).With().Timestamp().Logger()
	})

	return log
}

const CorrelationIDCtxKey = "correlation_id"

func GetWithCtx(ctx context.Context) zerolog.Logger {
	if ctx == nil {
		return log
	}

	l := log.With().Ctx(ctx).Logger()
	//l := Get().With().Logger()

	//if id := ctx.Value(CorrelationIDCtxKey).(string); id != "" {
	//	l.UpdateContext(func(c zerolog.Context) zerolog.Context {
	//		return c.Str(CorrelationIDCtxKey, id)
	//	})
	//}

	return l
}
