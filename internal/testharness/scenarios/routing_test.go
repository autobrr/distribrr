package scenarios

import (
	"net/http"
	"slices"
	"testing"

	"github.com/autobrr/distribrr/internal/testharness"
	"github.com/autobrr/distribrr/internal/testharness/fakeqbit"
	"github.com/autobrr/distribrr/pkg/stats"

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

// Down clients report no active downloads, but the agent will not add to
// them, so they must not make a busy agent look idle.
func TestScoring_ignoresUnreachableClients(t *testing.T) {
	e := newEnv(t)

	busyQbit, down1, down2 := fakeqbit.New(t), fakeqbit.New(t), fakeqbit.New(t)
	flaky := e.AddAgent(t, testharness.AgentSpec{
		Name: "flaky",
		Clients: []testharness.ClientSpec{
			{Name: "busy", Qbit: busyQbit},
			{Name: "down1", Qbit: down1},
			{Name: "down2", Qbit: down2},
		},
	})
	idle, idleQbit := e.agent(t, "idle", nil)
	e.WaitReady(t, flaky, idle)

	busyQbit.SetActiveDownloads(downloading("a", 0.1, 3600))
	down1.SetDown(true)
	down2.SetDown(true)
	flaky.WaitClients(t, func(cs stats.ClientStats) bool {
		return cs.Name == "busy" || slices.Contains(cs.Reasons, stats.ReasonClientUnreachable)
	})

	for range 3 {
		status, body := e.Webhook(t, e.release("Unreachable.Release-GRP", 10*mb))
		require.Equal(t, http.StatusCreated, status, body)
	}

	assert.Empty(t, busyQbit.Added())
	assert.Len(t, idleQbit.Added(), 3)
}

func TestTask_categoryAndTagsReachQbit(t *testing.T) {
	e := newEnv(t)

	a, q := e.agent(t, "node1", nil)
	e.WaitReady(t, a)

	u, hash := e.tracker.Add("Opts.Release-GRP", 10*mb)
	tk := taskFor(u, "Opts.Release-GRP")
	tk.Category = "race"
	tk.Tags = "race,distribrr"

	status, body := e.Webhook(t, tk)
	require.Equal(t, http.StatusCreated, status, body)

	added := q.Added()
	require.Len(t, added, 1)
	assert.Equal(t, hash, added[0].Hash)
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

	t.Run("one of two clients on an agent fails is still added", func(t *testing.T) {
		e := newEnv(t)

		badQbit, goodQbit := fakeqbit.New(t), fakeqbit.New(t)
		badQbit.FailAdd(true)

		a := e.AddAgent(t, testharness.AgentSpec{
			Name: "node1",
			Clients: []testharness.ClientSpec{
				{Name: "bad", Qbit: badQbit},
				{Name: "good", Qbit: goodQbit},
			},
		})
		e.WaitReady(t, a)

		status, body := e.Webhook(t, e.release("PartialAdd.Release-GRP", 10*mb))
		require.Equal(t, http.StatusCreated, status, body)
		assert.Empty(t, badQbit.Added())
		assert.Len(t, goodQbit.Added(), 1)
	})

	t.Run("every client on an agent fails", func(t *testing.T) {
		e := newEnv(t)

		q1, q2 := fakeqbit.New(t), fakeqbit.New(t)
		q1.FailAdd(true)
		q2.FailAdd(true)

		a := e.AddAgent(t, testharness.AgentSpec{
			Name: "node1",
			Clients: []testharness.ClientSpec{
				{Name: "q1", Qbit: q1},
				{Name: "q2", Qbit: q2},
			},
		})
		e.WaitReady(t, a)

		status, body := e.Webhook(t, e.release("AllFail.Release-GRP", 10*mb))
		assert.Equal(t, http.StatusBadGateway, status)
		assert.Contains(t, body, "node1")
		assert.Zero(t, totalAdded(q1, q2))
	})
}

func TestSchedulerType(t *testing.T) {
	t.Parallel()

	t.Run("round robin takes turns even when one agent is busier", func(t *testing.T) {
		e := newEnv(t)

		busy, busyQbit := e.agent(t, "busy", nil)
		idle, idleQbit := e.agent(t, "idle", nil)

		// least active would send every task to idle
		busyQbit.SetActiveDownloads(fakeqbit.Torrent{Hash: "a", Name: "Busy", Size: 100 * mb, AmountLeft: 90 * mb, Progress: 0.1, ETA: 3600})
		e.WaitReady(t, busy, idle)

		for range 4 {
			tk := e.release("RoundRobin.Release-GRP", 10*mb)
			tk.SchedulerType = "round_robin"

			status, body := e.Webhook(t, tk)
			require.Equal(t, http.StatusCreated, status, body)
		}

		assert.Len(t, busyQbit.Added(), 2)
		assert.Len(t, idleQbit.Added(), 2)
	})

	for _, schedulerType := range []string{"", "least_active"} {
		t.Run("least active accepts '"+schedulerType+"'", func(t *testing.T) {
			e := newEnv(t)

			a, q := e.agent(t, "node1", nil)
			e.WaitReady(t, a)

			tk := e.release("LeastActive.Release-GRP", 10*mb)
			tk.SchedulerType = schedulerType

			status, body := e.Webhook(t, tk)
			require.Equal(t, http.StatusCreated, status, body)
			assert.Len(t, q.Added(), 1)
		})
	}

	t.Run("unknown type is rejected", func(t *testing.T) {
		e := newEnv(t)

		a, q := e.agent(t, "node1", nil)
		e.WaitReady(t, a)

		tk := e.release("Unknown.Release-GRP", 10*mb)
		tk.SchedulerType = "greedy"

		status, body := e.Webhook(t, tk)
		assert.Equal(t, http.StatusBadRequest, status, body)
		assert.Contains(t, body, "unknown scheduler type")
		assert.Empty(t, q.Added())
	})
}
