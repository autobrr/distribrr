package scenarios

import (
	"net/http"
	"testing"
	"time"

	"github.com/autobrr/distribrr/internal/testharness"
	"github.com/autobrr/distribrr/internal/testharness/fakeqbit"
	"github.com/autobrr/distribrr/pkg/agent"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An agent adds a task to one client, the best ranked of its ready clients
// with room: least active first, then most storage headroom, then by name.
func TestClientPlacement(t *testing.T) {
	t.Parallel()

	t.Run("idle client takes the torrent over a busy one", func(t *testing.T) {
		e := newEnv(t)

		// "busy" sorts first by name, so only the score can pick "idle"
		busy, idle := fakeqbit.New(t), fakeqbit.New(t)
		busy.SetActiveDownloads(downloading("a", 0.1, 3600))

		a := e.AddAgent(t, testharness.AgentSpec{
			Name: "node1",
			Clients: []testharness.ClientSpec{
				{Name: "busy", Qbit: busy},
				{Name: "idle", Qbit: idle},
			},
		})
		e.WaitReady(t, a)

		status, body := e.Webhook(t, e.release("Idle.Release-GRP", 10*mb))
		require.Equal(t, http.StatusCreated, status, body)
		assert.Empty(t, busy.Added())
		assert.Len(t, idle.Added(), 1)
	})

	// "idle" would rank first, but the torrent does not fit its storage rule
	for _, sizeKnown := range []bool{false, true} {
		name := "size unknown"
		if sizeKnown {
			name = "size known"
		}

		t.Run("best client without room is passed over: "+name, func(t *testing.T) {
			e := newEnv(t)

			busy, idle := fakeqbit.New(t), fakeqbit.New(t)
			busy.SetActiveDownloads(downloading("a", 0.1, 3600))

			a := e.AddAgent(t, testharness.AgentSpec{
				Name: "node1",
				Clients: []testharness.ClientSpec{
					{Name: "busy", Qbit: busy},
					{Name: "idle", Qbit: idle, Storage: []agent.StorageRule{{Path: t.TempDir(), MaxUsage: "20 MB"}}},
				},
			})
			e.WaitReady(t, a)

			tk := e.release("NoRoom.Release-GRP", 50*mb)
			if sizeKnown {
				tk.Size = 50 * mb
			}

			status, body := e.Webhook(t, tk)
			require.Equal(t, http.StatusCreated, status, body)
			assert.Empty(t, idle.Added())
			assert.Len(t, busy.Added(), 1)
		})
	}

	t.Run("failed add falls back to the next client", func(t *testing.T) {
		e := newEnv(t)

		best, next := fakeqbit.New(t), fakeqbit.New(t)
		best.FailAdd(true)
		next.SetActiveDownloads(downloading("a", 0.1, 3600))

		a := e.AddAgent(t, testharness.AgentSpec{
			Name: "node1",
			Clients: []testharness.ClientSpec{
				{Name: "best", Qbit: best},
				{Name: "next", Qbit: next},
			},
		})
		e.WaitReady(t, a)

		status, body := e.Webhook(t, e.release("Fallback.Release-GRP", 10*mb))
		require.Equal(t, http.StatusCreated, status, body)
		assert.Equal(t, 1, best.Requests("torrents/add"), "the best client should be tried first")
		assert.Empty(t, best.Added())
		assert.Len(t, next.Added(), 1)
		assert.Equal(t, 1, e.tracker.Downloads("Fallback.Release-GRP"), "the fallback reuses the fetched .torrent")

		// only the client that took the torrent re-announces it
		require.Eventually(t, func() bool {
			return next.Requests("torrents/trackers") > 0
		}, testharness.WaitTimeout, 10*time.Millisecond)
		assert.Zero(t, best.Requests("torrents/trackers"))
	})
}
