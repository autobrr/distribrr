package scheduler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/autobrr/distribrr/pkg/node"
	"github.com/autobrr/distribrr/pkg/stats"
	"github.com/autobrr/distribrr/pkg/task"
	"github.com/autobrr/go-qbittorrent"

	"github.com/c9s/goprocinfo/linux"
	"github.com/stretchr/testify/assert"
)

func Test_matchLabels(t *testing.T) {
	type args struct {
		taskLabels map[string]string
		nodeLabels map[string]string
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "",
			args: args{
				taskLabels: map[string]string{
					"disktype": "ssd",
				},
				nodeLabels: map[string]string{
					"disktype": "ssd",
				},
			},
			want: true,
		},
		{
			name: "",
			args: args{
				taskLabels: map[string]string{
					"disktype": "hdd",
				},
				nodeLabels: map[string]string{
					"disktype": "ssd",
				},
			},
			want: false,
		},
		{
			name: "",
			args: args{
				taskLabels: map[string]string{
					"disktype": "ssd",
					"region":   "us-west-1",
				},
				nodeLabels: map[string]string{
					"disktype": "ssd",
					"region":   "us-west-1",
				},
			},
			want: true,
		},
		{
			name: "",
			args: args{
				taskLabels: map[string]string{
					"disktype": "ssd",
					"region":   "us-west-1",
				},
				nodeLabels: map[string]string{
					"disktype": "ssd",
					"region":   "us-west-2",
				},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := checkLabels(tt.args.taskLabels, tt.args.nodeLabels); got != tt.want {
				t.Errorf("matchLabels() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLeastActive_Score(t *testing.T) {
	type args struct {
		ctx   context.Context
		t     task.Task
		nodes []*node.Node
	}
	tests := []struct {
		name string
		args args
		want map[string]float64
	}{
		{
			name: "test_1",
			want: map[string]float64{
				"node0": 120,
				"node1": 96.49774305555556,
				"node2": 120,
				"node3": 94.81565972222222,
			},
			args: args{
				ctx: context.Background(),
				t:   task.Task{},
				nodes: []*node.Node{
					testNode("node0", node.StatusReady, map[string]string{"disktype": "ssd", "region": "us-west-1"}, stats.Stats{
						ClientStats: map[string]stats.ClientStats{
							"node1": {
								ActiveDownloadsCount:      0,
								MaxActiveDownloadsAllowed: 1,
								Status:                    stats.ClientStatusReady,
								ActiveDownloads:           []qbittorrent.Torrent{},
							},
						},
					}),
					testNode("node1", node.StatusReady, map[string]string{"disktype": "ssd", "region": "us-west-1"}, stats.Stats{
						ClientStats: map[string]stats.ClientStats{
							"node1": {
								ActiveDownloadsCount:      1,
								MaxActiveDownloadsAllowed: 2,
								Status:                    stats.ClientStatusReady,
								ActiveDownloads: []qbittorrent.Torrent{
									{
										Progress: 0.5,
										ETA:      65,
									},
								},
							},
						},
					}),
					testNode("node2", node.StatusReady, map[string]string{"disktype": "ssd", "region": "us-west-1"}, stats.Stats{
						ClientStats: map[string]stats.ClientStats{
							"node1": {
								ActiveDownloadsCount:      0,
								MaxActiveDownloadsAllowed: 3,
								Status:                    stats.ClientStatusReady,
								ActiveDownloads:           []qbittorrent.Torrent{},
							},
						},
					}),
					testNode("node3", node.StatusReady, map[string]string{"disktype": "ssd", "region": "us-west-1"}, stats.Stats{
						ClientStats: map[string]stats.ClientStats{
							"node1": {
								ActiveDownloadsCount:      2,
								MaxActiveDownloadsAllowed: 3,
								Status:                    stats.ClientStatusReady,
								ActiveDownloads: []qbittorrent.Torrent{
									{
										Progress: 0.76,
										ETA:      25,
									},
									{
										Progress: 0.5,
										ETA:      100,
									},
								},
							},
						},
					}),
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &LeastActive{}

			got := r.Score(tt.args.ctx, tt.args.t, tt.args.nodes)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestLeastActive_PickN(t *testing.T) {
	newNodes := func() []*node.Node {
		return []*node.Node{{Name: "node0"}, {Name: "node1"}, {Name: "node2"}}
	}
	scores := map[string]float64{"node0": 100, "node1": 120, "node2": 90}

	r := &LeastActive{}

	tests := []struct {
		name   string
		number int
		want   int
	}{
		{name: "zero defaults to one", number: 0, want: 1},
		{name: "negative defaults to one", number: -1, want: 1},
		{name: "exact count", number: 2, want: 2},
		{name: "all nodes", number: 3, want: 3},
		{name: "more than available is clamped", number: 5, want: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.PickN(scores, newNodes(), tt.number)
			assert.Len(t, got, tt.want)
		})
	}

	t.Run("no candidates returns nil", func(t *testing.T) {
		assert.Nil(t, r.PickN(scores, nil, 2))
	})

	t.Run("picks the highest scoring node first", func(t *testing.T) {
		got := r.PickN(scores, newNodes(), 1)
		assert.Len(t, got, 1)
		assert.Equal(t, "node1", got[0].Name)
	})
}

func Test_formatReasons(t *testing.T) {
	assert.Equal(t, "not ready", formatReasons(nil))
	assert.Equal(t, "disk_full", formatReasons([]stats.NotReadyReason{stats.ReasonDiskFull}))
	assert.Equal(t, "max_downloads_reached, disk_full",
		formatReasons([]stats.NotReadyReason{stats.ReasonMaxDownloadsReached, stats.ReasonDiskFull}))
}

func TestLeastActive_SelectCandidateNodes_rejections(t *testing.T) {
	r := &LeastActive{}

	// both nodes are rejected without any network calls: "down" fails the
	// status gate, "mismatch" fails label matching using its cached labels.
	nodes := []*node.Node{
		testNode("down", node.StatusUnknown, map[string]string{}, stats.Stats{}),
		testNode("mismatch", node.StatusReady, map[string]string{"region": "eu"}, stats.Stats{}),
	}

	tsk := task.Task{Labels: map[string]string{"region": "us"}}

	candidates, rejected := r.SelectCandidateNodes(context.Background(), tsk, nodes)

	assert.Empty(t, candidates)
	assert.Len(t, rejected, 2)

	byNode := map[string][]string{}
	for _, rj := range rejected {
		byNode[rj.Node] = rj.Reasons
	}

	assert.Contains(t, byNode, "down")
	assert.Contains(t, byNode["down"][0], "node not ready")
	assert.Contains(t, byNode, "mismatch")
	assert.Contains(t, byNode["mismatch"][0], "labels do not match")
}

func Test_hasRoomFor(t *testing.T) {
	const gb = 1_000_000_000

	full := stats.ClientStats{Name: "full", StorageHeadroom: new(uint64(1 * gb))}
	roomy := stats.ClientStats{Name: "roomy", StorageHeadroom: new(uint64(100 * gb))}
	noRules := stats.ClientStats{Name: "norules"}

	assert.True(t, hasRoomFor(map[string]stats.ClientStats{"full": full}, 0), "unknown size always fits")
	assert.False(t, hasRoomFor(map[string]stats.ClientStats{"full": full}, 50*gb))
	assert.True(t, hasRoomFor(map[string]stats.ClientStats{"full": full, "roomy": roomy}, 50*gb), "one client with room is enough")
	assert.True(t, hasRoomFor(map[string]stats.ClientStats{"norules": noRules}, 50*gb), "no storage rules means nothing to check")
	assert.False(t, hasRoomFor(map[string]stats.ClientStats{}, 50*gb))
}

func TestLeastActive_SelectCandidateNodes_size(t *testing.T) {
	const gb = 1_000_000_000

	// a fake agent whose only client is ready with 10GB of storage headroom
	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(stats.Stats{
			MemStats:  &linux.MemInfo{},
			DiskStats: &linux.Disk{},
			ClientStats: map[string]stats.ClientStats{
				"qbit": {Name: "qbit", Status: stats.ClientStatusReady, StorageHeadroom: new(uint64(10 * gb))},
			},
		})
	}))
	defer agentSrv.Close()

	newNode := func() *node.Node {
		n := node.NewNode("node1", agentSrv.URL, "token", "worker")
		n.SetStatus(node.StatusReady)
		n.SetLabels(map[string]string{})
		return n
	}

	r := &LeastActive{}

	candidates, rejected := r.SelectCandidateNodes(t.Context(), task.Task{Size: 5 * gb}, []*node.Node{newNode()})
	assert.Len(t, candidates, 1)
	assert.Empty(t, rejected)

	candidates, rejected = r.SelectCandidateNodes(t.Context(), task.Task{Size: 50 * gb}, []*node.Node{newNode()})
	assert.Empty(t, candidates)
	assert.Equal(t, []NodeRejection{{Node: "node1", Reasons: []string{"no ready client has room for 50 GB"}}}, rejected)
}

// testNode builds a node with cached labels and stats, so the scheduler does
// not call out to an agent for them.
func testNode(name string, status node.Status, labels map[string]string, st stats.Stats) *node.Node {
	n := node.NewNode(name, "http://127.0.0.1:0", "token", "worker")
	n.SetStatus(status)
	n.SetLabels(labels)
	n.SetStats(st)

	return n
}

func TestLeastActive_SelectCandidateNodes_partiallyReady(t *testing.T) {
	const gb = 1_000_000_000

	newAgent := func(clients map[string]stats.ClientStats) *node.Node {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(stats.Stats{MemStats: &linux.MemInfo{}, DiskStats: &linux.Disk{}, ClientStats: clients})
		}))
		t.Cleanup(srv.Close)

		return testNodeAt(srv.URL, "node1")
	}

	busy := stats.ClientStats{Name: "busy", Status: stats.ClientStatusNotReady, Reasons: []stats.NotReadyReason{stats.ReasonMaxDownloadsReached}}
	down := stats.ClientStats{Name: "down", Status: stats.ClientStatusNotReady, Reasons: []stats.NotReadyReason{stats.ReasonClientUnreachable}}
	small := stats.ClientStats{Name: "small", Status: stats.ClientStatusReady, StorageHeadroom: new(uint64(1 * gb))}
	idle := stats.ClientStats{Name: "idle", Status: stats.ClientStatusReady}

	r := &LeastActive{}

	t.Run("one ready client is enough", func(t *testing.T) {
		n := newAgent(map[string]stats.ClientStats{"busy": busy, "idle": idle})

		candidates, rejected := r.SelectCandidateNodes(t.Context(), task.Task{}, []*node.Node{n})
		assert.Len(t, candidates, 1)
		assert.Empty(t, rejected)
	})

	t.Run("no ready client lists every client's reasons", func(t *testing.T) {
		n := newAgent(map[string]stats.ClientStats{"busy": busy, "down": down})

		candidates, rejected := r.SelectCandidateNodes(t.Context(), task.Task{}, []*node.Node{n})
		assert.Empty(t, candidates)
		assert.Equal(t, []NodeRejection{{Node: "node1", Reasons: []string{
			"client busy: max_downloads_reached",
			"client down: client_unreachable",
		}}}, rejected)
	})

	t.Run("room only counts on ready clients", func(t *testing.T) {
		// "busy" has no storage rules, so it would fit anything, but it is not ready
		n := newAgent(map[string]stats.ClientStats{"busy": busy, "small": small})

		candidates, rejected := r.SelectCandidateNodes(t.Context(), task.Task{Size: 5 * gb}, []*node.Node{n})
		assert.Empty(t, candidates)
		assert.Equal(t, []NodeRejection{{Node: "node1", Reasons: []string{"no ready client has room for 5.0 GB"}}}, rejected)
	})
}

func testNodeAt(addr string, name string) *node.Node {
	n := node.NewNode(name, addr, "token", "worker")
	n.SetStatus(node.StatusReady)
	n.SetLabels(map[string]string{})

	return n
}

func TestRoundRobin_PickN(t *testing.T) {
	names := func(nodes []*node.Node) []string {
		out := make([]string, len(nodes))
		for i, n := range nodes {
			out[i] = n.Name
		}
		return out
	}
	nodes := func(names ...string) []*node.Node {
		out := make([]*node.Node, len(names))
		for i, name := range names {
			out[i] = &node.Node{Name: name}
		}
		return out
	}

	t.Run("takes turns in name order", func(t *testing.T) {
		r := &RoundRobin{}

		var got []string
		for range 4 {
			got = append(got, names(r.PickN(nil, nodes("node2", "node0", "node1"), 1))...)
		}

		assert.Equal(t, []string{"node0", "node1", "node2", "node0"}, got)
	})

	t.Run("replicas continue the turn and wrap", func(t *testing.T) {
		r := &RoundRobin{}

		assert.Equal(t, []string{"node0", "node1"}, names(r.PickN(nil, nodes("node0", "node1", "node2"), 2)))
		assert.Equal(t, []string{"node2", "node0"}, names(r.PickN(nil, nodes("node0", "node1", "node2"), 2)))
	})

	t.Run("a missing node does not reset the turn", func(t *testing.T) {
		r := &RoundRobin{}

		assert.Equal(t, []string{"node0"}, names(r.PickN(nil, nodes("node0", "node1", "node2"), 1)))
		assert.Equal(t, []string{"node2"}, names(r.PickN(nil, nodes("node0", "node2"), 1)))
		assert.Equal(t, []string{"node0"}, names(r.PickN(nil, nodes("node0", "node1", "node2"), 1)))
	})

	t.Run("more than available is clamped", func(t *testing.T) {
		r := &RoundRobin{}

		assert.Len(t, r.PickN(nil, nodes("node0", "node1"), 5), 2)
	})

	t.Run("no candidates returns nil", func(t *testing.T) {
		r := &RoundRobin{}

		assert.Nil(t, r.PickN(nil, nil, 1))
	})
}
