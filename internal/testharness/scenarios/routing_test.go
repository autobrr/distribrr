package scenarios

import (
	"net/http"
	"testing"

	"github.com/autobrr/distribrr/internal/testharness/fakeqbit"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplicas(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		agents   int
		replicas int
		want     int
	}{
		{name: "absent defaults to one", agents: 2, replicas: 0, want: 1},
		{name: "one", agents: 2, replicas: 1, want: 1},
		{name: "two on two agents", agents: 2, replicas: 2, want: 2},
		{name: "more than agents is clamped", agents: 2, replicas: 5, want: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)

			var qbits []*fakeqbit.Server
			for _, name := range []string{"node1", "node2"}[:tt.agents] {
				a, q := e.agent(t, name, nil)
				e.WaitReady(t, a)
				qbits = append(qbits, q)
			}

			tk := e.release("Replicas.Release-GRP", 10*mb)
			tk.MaxAllowedReplicas = tt.replicas

			status, body := e.Webhook(t, tk)
			require.Equal(t, http.StatusCreated, status, body)

			assert.Equal(t, tt.want, totalAdded(qbits...))
			for _, q := range qbits {
				assert.LessOrEqual(t, len(q.Added()), 1, "a node got the task twice")
			}
		})
	}
}

func TestLabels(t *testing.T) {
	t.Parallel()

	t.Run("task labels route only to the matching agent", func(t *testing.T) {
		e := newEnv(t)

		eu, euQbit := e.agent(t, "eu", map[string]string{"region": "eu", "disk": "ssd"})
		us, usQbit := e.agent(t, "us", map[string]string{"region": "us", "disk": "ssd"})
		e.WaitReady(t, eu, us)

		for range 3 {
			tk := e.release("Labels.Release-GRP", 10*mb)
			tk.Labels = map[string]string{"region": "us"}
			tk.MaxAllowedReplicas = 2

			status, body := e.Webhook(t, tk)
			require.Equal(t, http.StatusCreated, status, body)
		}

		assert.Empty(t, euQbit.Added())
		assert.Len(t, usQbit.Added(), 3)
	})

	t.Run("task without labels may go anywhere", func(t *testing.T) {
		e := newEnv(t)

		a, q := e.agent(t, "node1", map[string]string{"region": "eu"})
		e.WaitReady(t, a)

		status, body := e.Webhook(t, e.release("NoLabels.Release-GRP", 10*mb))
		require.Equal(t, http.StatusCreated, status, body)
		assert.Len(t, q.Added(), 1)
	})

	t.Run("no matching agent is rejected", func(t *testing.T) {
		e := newEnv(t)

		a, q := e.agent(t, "node1", map[string]string{"region": "eu"})
		e.WaitReady(t, a)

		tk := e.release("NoMatch.Release-GRP", 10*mb)
		tk.Labels = map[string]string{"region": "us"}

		status, body := e.Webhook(t, tk)
		assert.Equal(t, http.StatusBadGateway, status)
		assert.Contains(t, body, "node1 (labels do not match task)")
		assert.Empty(t, q.Added())
		assert.Zero(t, e.tracker.Downloads("NoMatch.Release-GRP"))
	})
}

func TestScoring_prefersIdleAgent(t *testing.T) {
	e := newEnv(t)

	busy, busyQbit := e.agent(t, "busy", nil)
	idle, idleQbit := e.agent(t, "idle", nil)

	busyQbit.SetActiveDownloads(fakeqbit.Torrent{Hash: "a", Name: "Busy", Size: 100 * mb, AmountLeft: 90 * mb, Progress: 0.1, ETA: 3600})
	e.WaitReady(t, busy, idle)

	for range 5 {
		tk := e.release("Score.Release-GRP", 10*mb)
		tk.MaxAllowedReplicas = 1

		status, body := e.Webhook(t, tk)
		require.Equal(t, http.StatusCreated, status, body)
	}

	assert.Empty(t, busyQbit.Added())
	assert.Len(t, idleQbit.Added(), 5)
}

func TestTask_categoryAndTagsReachQbit(t *testing.T) {
	e := newEnv(t)

	a, q := e.agent(t, "node1", nil)
	e.WaitReady(t, a)

	tk := e.release("Opts.Release-GRP", 10*mb)
	tk.Category = "race"
	tk.Tags = "race,distribrr"

	status, body := e.Webhook(t, tk)
	require.Equal(t, http.StatusCreated, status, body)

	added := q.Added()
	require.Len(t, added, 1)
	assert.Equal(t, tk.DownloadURL, added[0].URL)
	assert.Equal(t, "race", added[0].Category)
	assert.Equal(t, "race,distribrr", added[0].Tags)
}

func TestTask_badDownloadURL(t *testing.T) {
	e := newEnv(t)

	a, q := e.agent(t, "node1", nil)
	e.WaitReady(t, a)

	u := e.tracker.AddStatus("Missing.Release-GRP", http.StatusNotFound)

	status, body := e.Webhook(t, taskFor(u, "Missing.Release-GRP"))
	assert.Equal(t, http.StatusBadGateway, status)
	assert.Contains(t, body, "404")
	assert.Empty(t, q.Added())
	assert.Equal(t, 1, e.tracker.Downloads("Missing.Release-GRP"))
}

func TestTask_qbitRejectsAdd(t *testing.T) {
	t.Parallel()

	t.Run("only agent fails", func(t *testing.T) {
		e := newEnv(t)

		a, q := e.agent(t, "node1", nil)
		q.FailAdd(true)
		e.WaitReady(t, a)

		status, body := e.Webhook(t, e.release("FailAdd.Release-GRP", 10*mb))
		assert.Equal(t, http.StatusBadGateway, status)
		assert.Contains(t, body, "node1")
		assert.Empty(t, q.Added())
	})

	t.Run("one of two replicas fails is still scheduled", func(t *testing.T) {
		e := newEnv(t)

		bad, badQbit := e.agent(t, "bad", nil)
		good, goodQbit := e.agent(t, "good", nil)
		badQbit.FailAdd(true)
		e.WaitReady(t, bad, good)

		tk := e.release("BestEffort.Release-GRP", 10*mb)
		tk.MaxAllowedReplicas = 2

		status, body := e.Webhook(t, tk)
		require.Equal(t, http.StatusCreated, status, body)
		assert.Empty(t, badQbit.Added())
		assert.Len(t, goodQbit.Added(), 1)
	})
}
