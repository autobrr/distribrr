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
									Ready:                     true,
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
									Ready:                     true,
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
									Ready:                     true,
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
									Ready:                     true,
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
			candidates := r.SelectCandidateNodes(tt.args.ctx, tt.args.t, tt.args.nodes)

			got := r.Score(tt.args.ctx, tt.args.t, candidates)
			assert.Equal(t, tt.want, got)

			//nodes := r.PickN(got, candidates, 2)
			//assert.Equal(t, 2, len(nodes))
		})
	}
}
