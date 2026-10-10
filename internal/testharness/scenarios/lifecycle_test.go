package scenarios

import (
	"net/http"
	"testing"
	"time"

	"github.com/autobrr/distribrr/internal/testharness"
	"github.com/autobrr/distribrr/internal/testharness/fakeqbit"
	"github.com/autobrr/distribrr/pkg/server"
	serverclient "github.com/autobrr/distribrr/pkg/server/client"
	"github.com/autobrr/distribrr/pkg/stats"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLifecycle(t *testing.T) {
	t.Parallel()

	t.Run("gracefully stopped agent is removed and skipped", func(t *testing.T) {
		e := newEnv(t)

		gone, goneQbit := e.agent(t, "gone", nil)
		stays, staysQbit := e.agent(t, "stays", nil)
		e.WaitReady(t, gone, stays)

		gone.Stop()

		// deregister is synchronous, no health check needed
		n, ok := e.Node(t, "gone")
		require.True(t, ok, "a removed node stays listed")
		assert.Equal(t, "REMOVED", n.Status)

		tk := e.release("Stopped.Release-GRP", 10*mb)
		tk.MaxAllowedReplicas = 2

		status, body := e.Webhook(t, tk)
		require.Equal(t, http.StatusCreated, status, body)
		assert.Empty(t, goneQbit.Added())
		assert.Len(t, staysQbit.Added(), 1)

		// and it stays REMOVED across health checks
		e.WaitNode(t, "stays", "READY", testharness.WaitTimeout)
		n, _ = e.Node(t, "gone")
		assert.Equal(t, "REMOVED", n.Status)
	})

	t.Run("crashed agent goes unknown and is skipped", func(t *testing.T) {
		e := newEnv(t)

		crashed, crashedQbit := e.agent(t, "crashed", nil)
		stays, staysQbit := e.agent(t, "stays", nil)
		e.WaitReady(t, crashed, stays)

		crashed.Kill()
		e.WaitNode(t, "crashed", "UNKNOWN", testharness.WaitTimeout)

		tk := e.release("Crashed.Release-GRP", 10*mb)
		tk.MaxAllowedReplicas = 2

		status, body := e.Webhook(t, tk)
		require.Equal(t, http.StatusCreated, status, body)
		assert.Empty(t, crashedQbit.Added())
		assert.Len(t, staysQbit.Added(), 1)
	})

	t.Run("only agent crashed is reported as not ready", func(t *testing.T) {
		e := newEnv(t)

		a, _ := e.agent(t, "node1", nil)
		e.WaitReady(t, a)

		a.Kill()
		e.WaitNode(t, "node1", "UNKNOWN", testharness.WaitTimeout)

		status, body := e.Webhook(t, e.release("NoNodes.Release-GRP", 10*mb))
		assert.Equal(t, http.StatusBadGateway, status)
		assert.Contains(t, body, "node1 (node not ready (UNKNOWN))")
	})

	for _, crash := range []bool{false, true} {
		name := "agent restarted on a new port after a stop is one node again"
		if crash {
			name = "agent restarted on a new port after a crash is one node again"
		}

		t.Run(name, func(t *testing.T) {
			e := newEnv(t)

			a, q := e.agent(t, "node1", nil)
			e.WaitReady(t, a)
			oldURL := a.URL()

			if crash {
				a.Kill()
				e.WaitNode(t, "node1", "UNKNOWN", testharness.WaitTimeout)
			} else {
				a.Stop()
			}

			a.Restart()
			require.NotEqual(t, oldURL, a.URL())

			e.WaitReady(t, a)

			nodes := e.Nodes(t)
			require.Len(t, nodes, 1)
			assert.Equal(t, a.URL(), nodes[0].Addr)

			status, body := e.Webhook(t, e.release("Restarted.Release-GRP", 10*mb))
			require.Equal(t, http.StatusCreated, status, body)
			assert.Len(t, q.Added(), 1)

			// the server persisted the new address, once
			cfg := server.NewConfig()
			require.NoError(t, cfg.LoadFromFile(e.ConfigFile()))
			require.Len(t, cfg.Nodes, 1)
			assert.Equal(t, a.URL(), cfg.Nodes[0].Addr)
		})
	}
}

func TestReload(t *testing.T) {
	t.Parallel()

	t.Run("agent label change routes later tasks to it", func(t *testing.T) {
		e := newEnv(t)

		a, q := e.agent(t, "node1", map[string]string{"tier": "slow"})
		e.WaitReady(t, a)

		tk := e.release("Relabel.Release-GRP", 10*mb)
		tk.Labels = map[string]string{"tier": "fast"}

		status, body := e.Webhook(t, tk)
		require.Equal(t, http.StatusBadGateway, status)
		assert.Contains(t, body, "labels do not match task")

		a.Reload(func(s *testharness.AgentSpec) {
			s.Labels = map[string]string{"tier": "fast"}
		})

		e.WaitNodeMatch(t, "node1", func(n testharness.NodeInfo) bool {
			return n.Labels["tier"] == "fast"
		})
		e.WaitReady(t, a)

		status, body = e.Webhook(t, tk)
		require.Equal(t, http.StatusCreated, status, body)
		assert.Len(t, q.Added(), 1)
	})

	t.Run("server reload adds a node from its config file", func(t *testing.T) {
		e := newEnv(t)

		registered, registeredQbit := e.agent(t, "registered", nil)
		e.WaitReady(t, registered)

		// an agent that cannot reach the server, so it never registers
		manualQbit := fakeqbit.New(t)
		manual := e.AddAgent(t, testharness.AgentSpec{
			Name:         "manual",
			Clients:      []testharness.ClientSpec{{Name: "qbit", Qbit: manualQbit}},
			Unregistered: true,
		})

		_, ok := e.Node(t, "manual")
		require.False(t, ok)

		e.ReloadServer(t, func(cfg *server.Config) {
			cfg.Nodes = append(cfg.Nodes, &server.AgentNode{Name: "manual", Addr: manual.URL(), Token: manual.Token()})
		})

		e.WaitReady(t, manual)

		tk := e.release("FromFile.Release-GRP", 10*mb)
		tk.MaxAllowedReplicas = 2

		status, body := e.Webhook(t, tk)
		require.Equal(t, http.StatusCreated, status, body)
		assert.Len(t, registeredQbit.Added(), 1)
		assert.Len(t, manualQbit.Added(), 1)
	})
}

func TestAuth(t *testing.T) {
	e := newEnv(t)

	a, q := e.agent(t, "node1", nil)
	e.WaitReady(t, a)

	tk := e.release("Auth.Release-GRP", 10*mb)

	for _, token := range []string{"", "wrong-token"} {
		status, _ := e.WebhookWithToken(t, token, tk)
		assert.Equal(t, http.StatusUnauthorized, status, "token %q", token)
	}

	assert.Empty(t, q.Added())
	assert.Zero(t, e.tracker.Downloads("Auth.Release-GRP"))

	status, body := e.Webhook(t, tk)
	require.Equal(t, http.StatusCreated, status, body)
}

// Health checks are an hour apart here, so a node only takes tasks if joining
// or a server start makes it READY without waiting for the next one.
func TestReadyWithoutWaitingForHealthCheck(t *testing.T) {
	t.Parallel()

	t.Run("a joined agent takes tasks right away", func(t *testing.T) {
		e := newEnv(t, testharness.WithHealthCheckInterval(time.Hour))

		a, q := e.agent(t, "node1", nil)
		a.WaitClients(t, func(cs stats.ClientStats) bool { return cs.Status == stats.ClientStatusReady })
		e.WaitNodeMatch(t, "node1", func(testharness.NodeInfo) bool { return true })

		status, body := e.Webhook(t, e.release("Joined.Release-GRP", 10*mb))
		require.Equal(t, http.StatusCreated, status, body)
		assert.Len(t, q.Added(), 1)
	})

	t.Run("a restarted server takes tasks right away", func(t *testing.T) {
		e := newEnv(t, testharness.WithHealthCheckInterval(time.Hour))

		a, q := e.agent(t, "node1", nil)
		a.WaitClients(t, func(cs stats.ClientStats) bool { return cs.Status == stats.ClientStatusReady })
		e.WaitNodeMatch(t, "node1", func(testharness.NodeInfo) bool { return true })

		e.RestartServer(t)
		e.WaitNode(t, "node1", "READY", testharness.WaitTimeout)

		status, body := e.Webhook(t, e.release("Restarted.Release-GRP", 10*mb))
		require.Equal(t, http.StatusCreated, status, body)
		assert.Len(t, q.Added(), 1)
	})
}

func TestHungAgent(t *testing.T) {
	t.Parallel()

	const checkTimeout = 300 * time.Millisecond

	t.Run("hung agent does not stall the webhook", func(t *testing.T) {
		// no health check after the first, so the hung agent stays READY like
		// one that went dark since the last check
		e := newEnv(t, testharness.WithHealthCheckInterval(time.Hour), testharness.WithNodeCheckTimeout(checkTimeout))

		hung, hungQbit := e.agent(t, "hung", nil)
		ok, okQbit := e.agent(t, "ok", nil)
		e.WaitReady(t, hung, ok)

		hung.Hang()

		tk := e.release("Hung.Release-GRP", 10*mb)
		tk.MaxAllowedReplicas = 2

		start := time.Now()
		status, body := e.Webhook(t, tk)
		require.Equal(t, http.StatusCreated, status, body)

		assert.Less(t, time.Since(start), 5*checkTimeout, "the hung agent delayed the webhook")
		assert.Empty(t, hungQbit.Added())
		assert.Len(t, okQbit.Added(), 1)
	})

	t.Run("hung agent does not delay health checks of the others", func(t *testing.T) {
		e := newEnv(t, testharness.WithNodeCheckTimeout(checkTimeout))

		hung, _ := e.agent(t, "hung", nil)
		crashed, _ := e.agent(t, "crashed", nil)
		e.WaitReady(t, hung, crashed)

		// let a health check round start waiting on the hung agent
		hung.Hang()
		time.Sleep(100 * time.Millisecond)

		crashed.Kill()

		e.WaitNode(t, "crashed", "UNKNOWN", 3*checkTimeout)
		e.WaitNode(t, "hung", "UNKNOWN", 3*checkTimeout)
	})
}

func TestJoinError(t *testing.T) {
	t.Parallel()

	t.Run("unreachable client_addr is reported to the agent", func(t *testing.T) {
		e := newEnv(t)

		err := serverclient.NewClient(e.URL(), testharness.Token).JoinRequest(t.Context(), serverclient.JoinRequest{
			NodeName:   "unreachable",
			ClientAddr: "http://127.0.0.1:1",
			Token:      "unreachable-token",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "could not reach agent at http://127.0.0.1:1 to verify its token")
	})
}
