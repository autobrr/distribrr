package agent

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/autobrr/distribrr/pkg/middleware"
	"github.com/autobrr/distribrr/pkg/task"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_forwardsCorrelationID(t *testing.T) {
	var got string
	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(middleware.CorrelationIDHeader)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(agentSrv.Close)

	client := NewClient(agentSrv.URL, "node1", "token")

	// the server's webhook handler, calling the agent with the request ctx
	var sendErr error
	webhook := middleware.CorrelationID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sendErr = client.StartTask(r.Context(), &task.Event{})
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", nil)
	req.Header.Set(middleware.CorrelationIDHeader, "abc123")
	webhook.ServeHTTP(httptest.NewRecorder(), req)

	require.NoError(t, sendErr)
	assert.Equal(t, "abc123", got)
}
