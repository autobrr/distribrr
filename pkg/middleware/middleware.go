package middleware

import (
	"context"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/autobrr/distribrr/pkg/logger"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/xid"
	"github.com/rs/zerolog"
)

func IsAuthenticated(apiKey string) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token := r.Header.Get("X-API-Token"); token != "" {
				// check header
				if token != apiKey {
					http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
					return
				}
				// add token to context
				ctx := context.WithValue(r.Context(), "token", token)
				r = r.WithContext(ctx)
			} else if token := r.Header.Get("Authorization"); token != "" {
				// check header
				if token != apiKey {
					http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
					return
				}

				// add token to context
				ctx := context.WithValue(r.Context(), "token", token)
				r = r.WithContext(ctx)

			} else if key := r.URL.Query().Get("apikey"); key != "" {
				// check query param lke ?apikey=TOKEN
				if key != apiKey {
					http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
					return
				}

				// add token to context
				ctx := context.WithValue(r.Context(), "token", key)
				r = r.WithContext(ctx)

			}

			next.ServeHTTP(w, r)
		})
	}
}

//// Key to use when setting the request ID.
//type ctxKeyRequestID int
//
//// Key to use when setting the trace id.
//type ctxTraceIdKey struct{}
//
//// RequestIDKey is the key that holds the unique request ID in a request context.
//const RequestIDKey ctxKeyRequestID = 0
//
//// RequestIDHeader is the name of the HTTP Header which contains the request id.
//// Exported so that it can be changed by developers
//var RequestIDHeader = "X-Request-Id"
//
//var RequestIDPrefix = ""
//
//// TraceID is a middleware that injects a trace ID into the context of each
//// request. A trace ID is a UUID string.
//func TraceID(next http.Handler) http.Handler {
//	fn := func(w http.ResponseWriter, r *http.Request) {
//		ctx := r.Context()
//		traceID := r.Header.Get(RequestIDHeader)
//		if traceID == "" {
//			traceID = uuid.New().String()
//		}
//		// set response header
//		w.Header().Set(RequestIDHeader, traceID)
//		// set request context
//		ctx = context.WithValue(ctx, ctxTraceIdKey{}, traceID)
//		next.ServeHTTP(w, r.WithContext(ctx))
//	}
//	return http.HandlerFunc(fn)
//}

// CorrelationIDHeader is the name of the HTTP Header which contains a correlation ID.
// Exported so that it can be changed by developers
var CorrelationIDHeader = "X-Correlation-ID"

// CorrelationIDCtxKey is the context key used.
var CorrelationIDCtxKey = "correlation_id"

// CorrelationID is a middleware that injects a correlation ID into the context of each request.
// A correlation ID is an alphanumeric string.
func CorrelationID(next http.Handler) http.Handler {
	fn := func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.Header.Get(CorrelationIDHeader)
		if id == "" {
			id = xid.New().String()
		}
		// set response header
		w.Header().Set(CorrelationIDHeader, id)

		// set request context
		//ctx = context.WithValue(ctx, ctxTraceIdKey{}, id)
		//ctx = context.WithValue(ctx, "correlation_id", id)
		ctx = context.WithValue(ctx, CorrelationIDCtxKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
	return http.HandlerFunc(fn)
}

type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func newLoggingResponseWriter(w http.ResponseWriter) *loggingResponseWriter {
	return &loggingResponseWriter{w, http.StatusOK}
}

func (lrw *loggingResponseWriter) WriteHeader(code int) {
	lrw.statusCode = code
	lrw.ResponseWriter.WriteHeader(code)
}

func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		l := logger.Get()

		//correlationID := xid.New().String()
		//
		//ctx := context.WithValue(r.Context(), "correlation_id", correlationID)

		//r = r.WithContext(ctx)

		//w.Header().Add("X-Correlation-ID", correlationID)
		//
		//l.UpdateContext(func(c zerolog.Context) zerolog.Context {
		//	return c.Str("correlation_id", correlationID)
		//})

		if id := r.Context().Value(CorrelationIDCtxKey).(string); id != "" {
			l.UpdateContext(func(c zerolog.Context) zerolog.Context {
				return c.Str(CorrelationIDCtxKey, id)
			})
		}

		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		//lrw := newLoggingResponseWriter(w)

		r = r.WithContext(l.WithContext(r.Context()))

		defer func() {
			t2 := time.Now()

			// Recover and record stack traces in case of a panic
			if rec := recover(); rec != nil {
				l.Error().
					Str("type", "error").
					Timestamp().
					Interface("recover_info", rec).
					Bytes("debug_stack", debug.Stack()).
					Msg("log system error")

				http.Error(ww, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}

			l.Trace().
				Str("type", "access").
				Str("method", r.Method).
				Str("url", r.URL.RequestURI()).
				Str("user_agent", r.UserAgent()).
				Str("remote_ip", r.RemoteAddr).
				Int("status_code", ww.Status()).
				Str("bytes_in", r.Header.Get("Content-Length")).
				Int64("bytes_in", r.ContentLength).
				Int("bytes_out", ww.BytesWritten()).
				Float64("latency_ms", float64(t2.Sub(start).Nanoseconds())/1000000.0).
				Dur("elapsed_ms", time.Since(start)).
				Msg("incoming request")
		}()

		next.ServeHTTP(ww, r)
	})
}
