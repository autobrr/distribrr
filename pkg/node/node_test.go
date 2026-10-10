package node

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/autobrr/distribrr/pkg/stats"

	"github.com/c9s/goprocinfo/linux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNode_SetHealth_keepsRemoved(t *testing.T) {
	n := NewNode("node1", "http://127.0.0.1:0", "token", "worker")

	n.SetHealth(StatusReady)
	assert.Equal(t, Status(StatusReady), n.Status())

	n.SetStatus(StatusRemoved)

	// a health check that started before the deregister must not revive it
	n.SetHealth(StatusReady)
	assert.Equal(t, Status(StatusRemoved), n.Status())

	n.SetHealth(StatusUnknown)
	assert.Equal(t, Status(StatusRemoved), n.Status())
}

func TestNode_MarshalJSON(t *testing.T) {
	n := NewNode("node1", "http://agent:7430", "secret", "worker")
	n.SetStatus(StatusReady)
	n.SetLabels(map[string]string{"region": "eu"})

	data, err := json.Marshal(n)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(data, &got))

	assert.Equal(t, "node1", got["name"])
	assert.Equal(t, "http://agent:7430", got["addr"])
	assert.Equal(t, "READY", got["status"])
	assert.Equal(t, map[string]any{"region": "eu"}, got["labels"])
	assert.NotContains(t, string(data), "secret")
}

func TestNode_concurrent(t *testing.T) {
	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(stats.Stats{
			MemStats:    &linux.MemInfo{},
			DiskStats:   &linux.Disk{},
			ClientStats: map[string]stats.ClientStats{"qbit": {Name: "qbit"}},
		})
	}))
	defer agentSrv.Close()

	n := NewNode("node1", agentSrv.URL, "token", "worker")

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 50 {
				n.Update(agentSrv.URL, "token", map[string]string{"region": "eu"})
				n.SetHealth(StatusReady)
			}
		})
		wg.Go(func() {
			for range 50 {
				_, _ = n.GetStats(t.Context())
				_ = n.HealthCheck(t.Context())
			}
		})
		wg.Go(func() {
			for range 50 {
				_ = n.Status()
				_ = n.Addr()
				_ = n.Labels()
				_ = n.Stats().ClientStats
				_, _ = json.Marshal(n)
			}
		})
	}
	wg.Wait()
}

func TestNode_GetLabels_cachesFetchedLabels(t *testing.T) {
	var fetches atomic.Int32
	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		json.NewEncoder(w).Encode(map[string]string{"region": "eu"})
	}))
	defer agentSrv.Close()

	n := NewNode("node1", agentSrv.URL, "token", "worker")

	for range 3 {
		labels, err := n.GetLabels(t.Context())
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"region": "eu"}, labels)
	}

	assert.Equal(t, int32(1), fetches.Load())
}
