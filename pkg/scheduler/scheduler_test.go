package scheduler

import (
	"context"
	"encoding/json"
	"math"
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

// Only the clients the agent picks from count towards the score: a client
// that is not ready, or has no room for the torrent, is skipped by the agent.
func TestLeastActive_Score_onlyUsableClients(t *testing.T) {
	idle := stats.ClientStats{Name: "idle", Status: stats.ClientStatusReady}
	busy := stats.ClientStats{
		Name:                 "busy",
		Status:               stats.ClientStatusReady,
		ActiveDownloadsCount: 1,
		ActiveDownloads:      []qbittorrent.Torrent{{Progress: 0.1, ETA: 3600}},
	}
	unreachable := stats.ClientStats{Name: "unreachable", Status: stats.ClientStatusNotReady, Reasons: []stats.NotReadyReason{stats.ReasonClientUnreachable}}
	full := stats.ClientStats{
		Name:                 "full",
		Status:               stats.ClientStatusNotReady,
		Reasons:              []stats.NotReadyReason{stats.ReasonMaxDownloadsReached},
		ActiveDownloadsCount: 1,
		ActiveDownloads:      []qbittorrent.Torrent{{Progress: 0.1, ETA: 3600}},
	}
	headroom := uint64(5 * 1000 * 1000)
	noRoom := stats.ClientStats{Name: "noroom", Status: stats.ClientStatusReady, StorageHeadroom: &headroom}

	nodeWith := func(name string, clients ...stats.ClientStats) *node.Node {
		cs := make(map[string]stats.ClientStats, len(clients))
		for _, c := range clients {
			cs[c.Name] = c
		}
		return testNode(name, node.StatusReady, map[string]string{}, stats.Stats{ClientStats: cs})
	}

	r := &LeastActive{}
	busyScore := r.Score(context.Background(), task.Task{}, []*node.Node{nodeWith("n", busy)})["n"]

	tests := []struct {
		name string
		size uint64
		node *node.Node
		want float64
	}{
		{name: "unreachable client earns no idle bonus", node: nodeWith("n", idle, unreachable), want: 120},
		{name: "not ready client's downloads are not penalized", node: nodeWith("n", idle, full), want: 120},
		{name: "client without room earns no idle bonus", size: 10 * 1000 * 1000, node: nodeWith("n", busy, noRoom), want: busyScore},
		{name: "client without room counts when size is unknown", node: nodeWith("n", busy, noRoom), want: 120},
		{name: "unreachable clients do not lift a busy node", node: nodeWith("n", busy, unreachable), want: busyScore},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.Score(context.Background(), task.Task{Size: tt.size}, []*node.Node{tt.node})
			assert.Equal(t, tt.want, got["n"])
		})
	}
}

// The agent adds the torrent to one client, its best usable one, so a node
// scores as that client, not as the sum of its clients.
func TestLeastActive_Score_bestClient(t *testing.T) {
	idle := stats.ClientStats{Name: "idle", Status: stats.ClientStatusReady}
	idle2 := stats.ClientStats{Name: "idle2", Status: stats.ClientStatusReady}
	busy := stats.ClientStats{
		Name:                 "busy",
		Status:               stats.ClientStatusReady,
		ActiveDownloadsCount: 1,
		ActiveDownloads:      []qbittorrent.Torrent{{Progress: 0.1, ETA: 3600}},
	}
	nearlyDone := stats.ClientStats{
		Name:                 "nearlydone",
		Status:               stats.ClientStatusReady,
		ActiveDownloadsCount: 1,
		ActiveDownloads:      []qbittorrent.Torrent{{Progress: 0.9, ETA: 60}},
	}
	unreachable := stats.ClientStats{Name: "unreachable", Status: stats.ClientStatusNotReady, Reasons: []stats.NotReadyReason{stats.ReasonClientUnreachable}}

	nodeWith := func(clients ...stats.ClientStats) *node.Node {
		cs := make(map[string]stats.ClientStats, len(clients))
		for _, c := range clients {
			cs[c.Name] = c
		}
		return testNode("n", node.StatusReady, map[string]string{}, stats.Stats{ClientStats: cs})
	}

	tests := []struct {
		name string
		node *node.Node
		want float64
	}{
		{name: "two idle clients score as one", node: nodeWith(idle, idle2), want: 120},
		{name: "idle client is not dragged down by a busy one", node: nodeWith(idle, busy), want: 120},
		{name: "busy clients score as the least busy", node: nodeWith(busy, nearlyDone), want: nearlyDone.Score()},
		{name: "no usable client ranks last", node: nodeWith(unreachable), want: math.Inf(-1)},
		{name: "no clients ranks last", node: nodeWith(), want: math.Inf(-1)},
	}

	r := &LeastActive{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.Score(t.Context(), task.Task{}, []*node.Node{tt.node})
			assert.Equal(t, tt.want, got["n"])
		})
	}

	t.Run("an extra idle client does not lift a node", func(t *testing.T) {
		// a sum over clients would rank "two" first, though either node adds
		// the torrent to one idle client
		one := testNode("one", node.StatusReady, map[string]string{}, stats.Stats{ClientStats: map[string]stats.ClientStats{"idle": idle}})
		two := testNode("two", node.StatusReady, map[string]string{}, stats.Stats{ClientStats: map[string]stats.ClientStats{"idle": idle, "idle2": idle2}})

		scores := r.Score(t.Context(), task.Task{}, []*node.Node{one, two})
		assert.Equal(t, scores["one"], scores["two"])
	})
}
