package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
)

func TestRequestLogger_correlationID(t *testing.T) {
	var buf bytes.Buffer

	global := log.Logger
	log.Logger = zerolog.New(&buf)
	t.Cleanup(func() { log.Logger = global })

	var gotID string
	handler := CorrelationID(RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = CorrelationIDFromContext(r.Context())
		log.Ctx(r.Context()).Info().Msg("handled")
	})))

	t.Run("incoming id is kept and logged", func(t *testing.T) {
		buf.Reset()

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(CorrelationIDHeader, "abc123")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		assert.Equal(t, "abc123", gotID)
		assert.Equal(t, "abc123", rec.Header().Get(CorrelationIDHeader))
		assert.Contains(t, buf.String(), `"correlation_id":"abc123"`)
	})

	t.Run("missing id is generated", func(t *testing.T) {
		buf.Reset()

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

		assert.NotEmpty(t, gotID)
		assert.Equal(t, gotID, rec.Header().Get(CorrelationIDHeader))
		assert.Contains(t, buf.String(), `"correlation_id":"`+gotID+`"`)
	})
}

func TestRequestLogger_withoutCorrelationID(t *testing.T) {
	// used to panic on the unchecked type assertion
	handler := RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	assert.NotPanics(t, func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	})
}
