package scenarios

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/autobrr/distribrr/internal/testharness"
	"github.com/autobrr/distribrr/internal/testharness/fakeqbit"
	"github.com/autobrr/distribrr/pkg/agent"
	"github.com/autobrr/distribrr/pkg/stats"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func maxActive(n int) func(*testharness.ClientSpec) {
	return func(c *testharness.ClientSpec) { c.MaxActiveDownloads = n }
}

func storage(rules ...agent.StorageRule) func(*testharness.ClientSpec) {
	return func(c *testharness.ClientSpec) { c.Storage = rules }
}

func downloading(hash string, progress float64, eta int64) fakeqbit.Torrent {
	return fakeqbit.Torrent{Hash: hash, Name: hash, Size: 10 * mb, AmountLeft: int64((1 - progress) * 10 * mb), Progress: progress, ETA: eta}
}

// waitReason waits until every client of a reports reason, so the agent has
// settled (storage measured) before the webhook.
func waitReason(t *testing.T, a *testharness.Agent, reason stats.NotReadyReason) {
	t.Helper()

	a.WaitClients(t, func(cs stats.ClientStats) bool {
		for _, r := range cs.Reasons {
			if r == reason {
				return true
			}
		}
		return false
	})
}

func TestMaxActiveDownloads(t *testing.T) {
	t.Parallel()

	t.Run("agent at its limit is skipped for one with room", func(t *testing.T) {
		e := newEnv(t)

		full, fullQbit := e.agent(t, "full", nil, maxActive(2))
		free, freeQbit := e.agent(t, "free", nil, maxActive(2))

		fullQbit.SetActiveDownloads(downloading("a", 0.1, 3600), downloading("b", 0.2, 3600))
		e.WaitReady(t, free)
		e.WaitNode(t, "full", "READY", testharness.WaitTimeout)
		waitReason(t, full, stats.ReasonMaxDownloadsReached)

		tk := e.release("Limit.Release-GRP", 10*mb)
		tk.MaxAllowedReplicas = 2

		status, body := e.Webhook(t, tk)
		require.Equal(t, http.StatusCreated, status, body)
		assert.Empty(t, fullQbit.Added())
		assert.Len(t, freeQbit.Added(), 1)
	})

	t.Run("every agent at its limit is rejected", func(t *testing.T) {
		e := newEnv(t)

		var qbits []*fakeqbit.Server
		for _, name := range []string{"node1", "node2"} {
			a, q := e.agent(t, name, nil, maxActive(1))
			q.SetActiveDownloads(downloading("a", 0.1, 3600))
			e.WaitNode(t, name, "READY", testharness.WaitTimeout)
			waitReason(t, a, stats.ReasonMaxDownloadsReached)
			qbits = append(qbits, q)
		}

		status, body := e.Webhook(t, e.release("AllFull.Release-GRP", 10*mb))
		assert.Equal(t, http.StatusBadGateway, status)
		assert.Contains(t, body, "node1 (client qbit: max_downloads_reached)")
		assert.Contains(t, body, "node2 (client qbit: max_downloads_reached)")
		assert.Zero(t, totalAdded(qbits...))
	})

	t.Run("unset limit is unlimited", func(t *testing.T) {
		e := newEnv(t)

		a, q := e.agent(t, "node1", nil)

		var active []fakeqbit.Torrent
		for i := range 20 {
			active = append(active, downloading(fmt.Sprintf("t%d", i), 0.1, 3600))
		}
		q.SetActiveDownloads(active...)
		e.WaitReady(t, a)

		status, body := e.Webhook(t, e.release("Unlimited.Release-GRP", 10*mb))
		require.Equal(t, http.StatusCreated, status, body)
		assert.Len(t, q.Added(), 1)
	})

	// at exactly the limit, a download that is nearly done (>= 75% and ETA
	// <= 60s) counts as a free slot
	tests := []struct {
		name     string
		progress float64
		eta      int64
		accepted bool
	}{
		{name: "nearly done frees a slot", progress: 0.8, eta: 30, accepted: true},
		{name: "exactly 75% and 60s frees a slot", progress: 0.75, eta: 60, accepted: true},
		{name: "far along but slow does not", progress: 0.8, eta: 120},
		{name: "fast but early does not", progress: 0.5, eta: 30},
	}

	for _, tt := range tests {
		t.Run("at the limit: "+tt.name, func(t *testing.T) {
			e := newEnv(t)

			a, q := e.agent(t, "node1", nil, maxActive(2))
			q.SetActiveDownloads(downloading("slow", 0.1, 3600), downloading("last", tt.progress, tt.eta))
			e.WaitNode(t, "node1", "READY", testharness.WaitTimeout)

			if tt.accepted {
				e.WaitReady(t, a)
			} else {
				waitReason(t, a, stats.ReasonMaxDownloadsReached)
			}

			status, body := e.Webhook(t, e.release("NearlyDone.Release-GRP", 10*mb))
			if !tt.accepted {
				assert.Equal(t, http.StatusBadGateway, status)
				assert.Contains(t, body, "max_downloads_reached")
				assert.Empty(t, q.Added())
				return
			}

			require.Equal(t, http.StatusCreated, status, body)
			assert.Len(t, q.Added(), 1)
		})
	}
}

func TestQbitDown(t *testing.T) {
	e := newEnv(t)

	a, q := e.agent(t, "node1", nil)
	e.WaitReady(t, a)

	q.SetDown(true)
	waitReason(t, a, stats.ReasonClientUnreachable)

	status, body := e.Webhook(t, e.release("Down.Release-GRP", 10*mb))
	assert.Equal(t, http.StatusBadGateway, status)
	assert.Contains(t, body, "node1 (client qbit: client_unreachable)")
	assert.Zero(t, e.tracker.Downloads("Down.Release-GRP"))

	// and it recovers once qbit is back
	q.SetDown(false)
	e.WaitReady(t, a)

	status, body = e.Webhook(t, e.release("Up.Release-GRP", 10*mb))
	require.Equal(t, http.StatusCreated, status, body)
	assert.Len(t, q.Added(), 1)
}

func TestStorage(t *testing.T) {
	t.Parallel()

	t.Run("minFree that cannot be met reports disk_full", func(t *testing.T) {
		e := newEnv(t)

		a, q := e.agent(t, "node1", nil, storage(agent.StorageRule{Path: t.TempDir(), MinFree: "1000 PB"}))
		e.WaitNode(t, "node1", "READY", testharness.WaitTimeout)
		waitReason(t, a, stats.ReasonDiskFull)

		status, body := e.Webhook(t, e.release("DiskFull.Release-GRP", 10*mb))
		assert.Equal(t, http.StatusBadGateway, status)
		assert.Contains(t, body, "node1 (client qbit: disk_full)")
		assert.Empty(t, q.Added())
	})

	t.Run("minFree that is met accepts", func(t *testing.T) {
		e := newEnv(t)

		a, q := e.agent(t, "node1", nil, storage(agent.StorageRule{Path: t.TempDir(), MinFree: "1 KB"}))
		e.WaitReady(t, a)

		status, body := e.Webhook(t, e.release("Fits.Release-GRP", 10*mb))
		require.Equal(t, http.StatusCreated, status, body)
		assert.Len(t, q.Added(), 1)
	})

	t.Run("known size over headroom skips the node without fetching the torrent", func(t *testing.T) {
		e := newEnv(t)

		small, smallQbit := e.agent(t, "small", nil, storage(agent.StorageRule{Path: t.TempDir(), MaxUsage: "20 MB"}))
		e.WaitReady(t, small)

		tk := e.release("TooBig.Release-GRP", 50*mb)
		tk.Size = 50 * mb

		status, body := e.Webhook(t, tk)
		assert.Equal(t, http.StatusBadGateway, status)
		assert.Contains(t, body, "small (no ready client has room for 50 MB)")
		assert.Empty(t, smallQbit.Added())
		assert.Zero(t, e.tracker.Downloads("TooBig.Release-GRP"), "no agent may fetch the .torrent")

		// a node with room takes it instead
		big, bigQbit := e.agent(t, "big", nil, storage(agent.StorageRule{Path: t.TempDir(), MaxUsage: "1 GB"}))
		e.WaitReady(t, big)

		tk.MaxAllowedReplicas = 2

		status, body = e.Webhook(t, tk)
		require.Equal(t, http.StatusCreated, status, body)
		assert.Empty(t, smallQbit.Added())
		assert.Len(t, bigQbit.Added(), 1)
		assert.Equal(t, 1, e.tracker.Downloads("TooBig.Release-GRP"), "only the node with room fetches the .torrent")
	})

	t.Run("unknown size over headroom is rejected by the agent after fetching", func(t *testing.T) {
		e := newEnv(t)

		a, q := e.agent(t, "node1", nil, storage(agent.StorageRule{Path: t.TempDir(), MaxUsage: "20 MB"}))
		e.WaitReady(t, a)

		status, body := e.Webhook(t, e.release("Unknown.Release-GRP", 50*mb))
		assert.Equal(t, http.StatusBadGateway, status)
		assert.Contains(t, body, "507")
		assert.Contains(t, body, "insufficient storage")
		assert.Empty(t, q.Added())
		assert.Equal(t, 1, e.tracker.Downloads("Unknown.Release-GRP"))
	})

	// 100 MB maxUsage with 60 MB still to download leaves ~40 MB unless qbit
	// preallocates, in which case that space is already on disk
	for _, prealloc := range []bool{false, true} {
		name := "active downloads reserve space without preallocation"
		if prealloc {
			name = "active downloads reserve nothing with preallocation"
		}

		t.Run(name, func(t *testing.T) {
			e := newEnv(t)

			a, q := e.agent(t, "node1", nil, storage(agent.StorageRule{Path: t.TempDir(), MaxUsage: "100 MB"}))
			q.SetPreallocateAll(prealloc)
			q.SetActiveDownloads(fakeqbit.Torrent{Hash: "a", Name: "Active", Size: 80 * mb, AmountLeft: 60 * mb, Progress: 0.25, ETA: 600})
			e.WaitReady(t, a)

			tk := e.release("Prealloc.Release-GRP", 50*mb)
			tk.Size = 50 * mb

			status, body := e.Webhook(t, tk)
			if !prealloc {
				assert.Equal(t, http.StatusBadGateway, status)
				assert.Contains(t, body, "no ready client has room for 50 MB")
				assert.Empty(t, q.Added())
				return
			}

			require.Equal(t, http.StatusCreated, status, body)
			assert.Len(t, q.Added(), 1)
		})
	}
}

func TestMultiClientAgent_addsOnlyToClientsWithRoom(t *testing.T) {
	t.Parallel()

	for _, sizeKnown := range []bool{false, true} {
		name := "size unknown"
		if sizeKnown {
			name = "size known"
		}

		t.Run(name, func(t *testing.T) {
			e := newEnv(t)

			small, roomy, unlimited := fakeqbit.New(t), fakeqbit.New(t), fakeqbit.New(t)

			a := e.AddAgent(t, testharness.AgentSpec{
				Name: "node1",
				Clients: []testharness.ClientSpec{
					{Name: "small", Qbit: small, Storage: []agent.StorageRule{{Path: t.TempDir(), MaxUsage: "20 MB"}}},
					{Name: "roomy", Qbit: roomy, Storage: []agent.StorageRule{{Path: t.TempDir(), MaxUsage: "1 GB"}}},
					{Name: "unlimited", Qbit: unlimited},
				},
			})
			e.WaitReady(t, a)

			tk := e.release("Multi.Release-GRP", 50*mb)
			if sizeKnown {
				tk.Size = 50 * mb
			}

			status, body := e.Webhook(t, tk)
			require.Equal(t, http.StatusCreated, status, body)

			assert.Empty(t, small.Added())
			assert.Len(t, roomy.Added(), 1)
			assert.Len(t, unlimited.Added(), 1)
		})
	}
}

func TestMultiClientAgent_partiallyReady(t *testing.T) {
	t.Parallel()

	// clientState waits until the named clients report the given readiness.
	clientState := func(t *testing.T, a *testharness.Agent, ready map[string]bool) {
		t.Helper()

		a.WaitClients(t, func(cs stats.ClientStats) bool {
			return (cs.Status == stats.ClientStatusReady) == ready[cs.Name]
		})
	}

	t.Run("client at its limit is skipped for the ready one", func(t *testing.T) {
		e := newEnv(t)

		busy, idle := fakeqbit.New(t), fakeqbit.New(t)
		busy.SetActiveDownloads(downloading("a", 0.1, 3600))

		a := e.AddAgent(t, testharness.AgentSpec{
			Name: "node1",
			Clients: []testharness.ClientSpec{
				{Name: "busy", Qbit: busy, MaxActiveDownloads: 1},
				{Name: "idle", Qbit: idle, MaxActiveDownloads: 1},
			},
		})
		e.WaitNode(t, "node1", "READY", testharness.WaitTimeout)
		clientState(t, a, map[string]bool{"idle": true})

		status, body := e.Webhook(t, e.release("Partial.Release-GRP", 10*mb))
		require.Equal(t, http.StatusCreated, status, body)
		assert.Empty(t, busy.Added())
		assert.Len(t, idle.Added(), 1)
	})

	t.Run("unreachable client is skipped for the ready one", func(t *testing.T) {
		e := newEnv(t)

		down, up := fakeqbit.New(t), fakeqbit.New(t)
		down.SetDown(true)

		a := e.AddAgent(t, testharness.AgentSpec{
			Name: "node1",
			Clients: []testharness.ClientSpec{
				{Name: "down", Qbit: down},
				{Name: "up", Qbit: up},
			},
		})
		e.WaitNode(t, "node1", "READY", testharness.WaitTimeout)
		clientState(t, a, map[string]bool{"up": true})

		status, body := e.Webhook(t, e.release("Unreachable.Release-GRP", 10*mb))
		require.Equal(t, http.StatusCreated, status, body)
		assert.Empty(t, down.Added())
		assert.Len(t, up.Added(), 1)
	})

	t.Run("no ready client rejects the node with every reason", func(t *testing.T) {
		e := newEnv(t)

		busy, down := fakeqbit.New(t), fakeqbit.New(t)
		busy.SetActiveDownloads(downloading("a", 0.1, 3600))
		down.SetDown(true)

		a := e.AddAgent(t, testharness.AgentSpec{
			Name: "node1",
			Clients: []testharness.ClientSpec{
				{Name: "busy", Qbit: busy, MaxActiveDownloads: 1},
				{Name: "down", Qbit: down},
			},
		})
		e.WaitNode(t, "node1", "READY", testharness.WaitTimeout)
		clientState(t, a, map[string]bool{})

		status, body := e.Webhook(t, e.release("NoneReady.Release-GRP", 10*mb))
		assert.Equal(t, http.StatusBadGateway, status)
		assert.Contains(t, body, "node1 (client busy: max_downloads_reached; client down: client_unreachable)")
		assert.Zero(t, totalAdded(busy, down))
	})
}
