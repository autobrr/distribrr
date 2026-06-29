package scheduler

import (
	"context"
	"testing"

	"github.com/autobrr/distribrr/pkg/node"
	"github.com/autobrr/distribrr/pkg/stats"
	"github.com/autobrr/distribrr/pkg/task"
	"github.com/autobrr/go-qbittorrent"

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
	type fields struct {
		Name       string
		LastWorker int
	}
	type args struct {
		ctx   context.Context
		t     task.Task
		nodes []*node.Node
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   map[string]float64
	}{
		{
			name: "test_1",
			fields: fields{
				Name:       "leastActive",
				LastWorker: 0,
			},
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
					{
						Name:   "node0",
						Labels: map[string]string{"disktype": "ssd", "region": "us-west-1"},
						Status: node.StatusReady,
						Stats: stats.Stats{
							ClientStats: map[string]stats.ClientStats{
								"node1": {
									ActiveDownloadsCount:      0,
									MaxActiveDownloadsAllowed: 1,
									Status:                    stats.ClientStatusReady,
									ActiveDownloads:           []qbittorrent.Torrent{},
								},
							},
						},
					},
					{
						Name:   "node1",
						Labels: map[string]string{"disktype": "ssd", "region": "us-west-1"},
						Status: node.StatusReady,
						Stats: stats.Stats{
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
						},
					},
					{
						Name:   "node2",
						Labels: map[string]string{"disktype": "ssd", "region": "us-west-1"},
						Status: node.StatusReady,
						Stats: stats.Stats{
							ClientStats: map[string]stats.ClientStats{
								"node1": {
									ActiveDownloadsCount:      0,
									MaxActiveDownloadsAllowed: 3,
									Status:                    stats.ClientStatusReady,
									ActiveDownloads:           []qbittorrent.Torrent{},
								},
							},
						},
					},
					{
						Name:   "node3",
						Labels: map[string]string{"disktype": "ssd", "region": "us-west-1"},
						Status: node.StatusReady,
						Stats: stats.Stats{
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
						},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &LeastActive{
				Name:       tt.fields.Name,
				LastWorker: tt.fields.LastWorker,
			}

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
		{Name: "down", Status: node.StatusUnknown, Labels: map[string]string{}},
		{Name: "mismatch", Status: node.StatusReady, Labels: map[string]string{"region": "eu"}},
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
