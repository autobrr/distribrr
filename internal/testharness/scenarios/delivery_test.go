package scenarios

import (
	"net/http"
	"testing"
	"time"

	"github.com/autobrr/distribrr/internal/testharness"
	"github.com/autobrr/distribrr/internal/testharness/fakeqbit"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The server waits longer for an agent than the agent spends on a task, so a
// slow tracker fails on the agent and is reported, instead of the server
// giving up while the agent still adds the torrent.
func TestTaskDelivery(t *testing.T) {
	t.Parallel()

	// singleAgent starts one agent with taskTimeout and waits until it is ready.
	singleAgent := func(t *testing.T, e *env, taskTimeout time.Duration) *fakeqbit.Server {
		t.Helper()

		qbit := fakeqbit.New(t)
		a := e.AddAgent(t, testharness.AgentSpec{
			Name:        "node1",
			Clients:     []testharness.ClientSpec{{Name: "qbit", Qbit: qbit}},
			TaskTimeout: taskTimeout,
		})
		e.WaitReady(t, a)

		return qbit
	}

	t.Run("slow tracker within the deadlines succeeds", func(t *testing.T) {
		e := newEnv(t)
		qbit := singleAgent(t, e, 0)

		tk := e.release("Slow.Release-GRP", 10*mb)
		e.tracker.SetDelay("Slow.Release-GRP", 300*time.Millisecond)

		status, body := e.Webhook(t, tk)
		require.Equal(t, http.StatusCreated, status, body)
		assert.Len(t, qbit.Added(), 1)
	})

	t.Run("tracker slower than the agent's deadline fails on the agent", func(t *testing.T) {
		e := newEnv(t, testharness.WithTaskTimeout(5*time.Second))
		qbit := singleAgent(t, e, 500*time.Millisecond)

		tk := e.release("TooSlow.Release-GRP", 10*mb)
		e.tracker.SetDelay("TooSlow.Release-GRP", 10*time.Second)

		start := time.Now()
		status, body := e.Webhook(t, tk)
		elapsed := time.Since(start)

		assert.Equal(t, http.StatusBadGateway, status)
		assert.Contains(t, body, "node1")
		assert.Less(t, elapsed, 3*time.Second, "the agent's deadline, not the server's, should end the request")
		assert.Empty(t, qbit.Added())
	})

	t.Run("agent does not add after the server stopped waiting", func(t *testing.T) {
		e := newEnv(t, testharness.WithTaskTimeout(400*time.Millisecond))
		qbit := singleAgent(t, e, 10*time.Second)

		tk := e.release("Abandoned.Release-GRP", 10*mb)
		e.tracker.SetDelay("Abandoned.Release-GRP", time.Second)

		status, body := e.Webhook(t, tk)
		assert.Equal(t, http.StatusBadGateway, status, body)

		// past the tracker's delay: an agent that kept going would have added it by now
		time.Sleep(1500 * time.Millisecond)

		assert.Empty(t, qbit.Added())
		assert.Equal(t, 1, e.tracker.Downloads("Abandoned.Release-GRP"), "the agent should stop, not retry")
	})

	t.Run("brief tracker errors are retried quickly", func(t *testing.T) {
		e := newEnv(t)
		qbit := singleAgent(t, e, 0)

		tk := e.release("Flaky.Release-GRP", 10*mb)
		e.tracker.FailFirst("Flaky.Release-GRP", 2, http.StatusServiceUnavailable)

		start := time.Now()
		status, body := e.Webhook(t, tk)

		require.Equal(t, http.StatusCreated, status, body)
		assert.Len(t, qbit.Added(), 1)
		assert.Equal(t, 3, e.tracker.Downloads("Flaky.Release-GRP"))
		assert.Less(t, time.Since(start), 5*time.Second)
	})
}
