package node

import (
	"context"
	"fmt"
	"time"

	"github.com/autobrr/distribrr/pkg/agent"
	"github.com/autobrr/distribrr/pkg/stats"
	"github.com/autobrr/distribrr/pkg/task"
)

type Status string

const (
	StatusReady    = "READY"
	StatusNotReady = "NOT_READY"
	StatusUnknown  = "UNKNOWN"
)

type Node struct {
	Name            string            `json:"name"`
	Addr            string            `json:"addr"`
	Ip              string            `json:"ip"`
	Api             string            `json:"-"`
	Token           string            `json:"-"`
	Memory          int64             `json:"-"`
	MemoryAllocated int64             `json:"-"`
	Disk            int64             `json:"-"`
	DiskAllocated   int64             `json:"-"`
	Stats           stats.Stats       `json:"-"`
	Role            string            `json:"role"`
	TaskCount       int               `json:"task_count"`
	DateCreated     time.Time         `json:"date_created"`
	Status          Status            `json:"status"`
	Labels          map[string]string `json:"labels"`

	client *agent.Client
}

func NewNode(name string, clientAddr string, token string, role string) *Node {
	return &Node{
		Name:        name,
		Addr:        clientAddr,
		Token:       token,
		Role:        role,
		Status:      StatusNotReady,
		client:      agent.NewClient(clientAddr, name, token),
		DateCreated: time.Now().UTC(),
	}
}

func (n *Node) StartTask(ctx context.Context, te *task.Event) error {
	err := n.client.StartTask(ctx, te)
	if err != nil {
		return err
	}

	return nil
}

func (n *Node) HealthCheck(ctx context.Context) error {
	return n.client.HealthCheck(ctx)
}

func (n *Node) GetStats(ctx context.Context) (*stats.Stats, error) {
	nodeStats, err := n.client.GetStats(ctx)
	if err != nil {
		return nil, err
	}

	if nodeStats.MemStats == nil || nodeStats.DiskStats == nil {
		return nil, fmt.Errorf("error getting stats from node %s", n.Name)
	}

	n.Memory = int64(nodeStats.MemTotalKb())
	n.Disk = int64(nodeStats.DiskTotal())
	n.Stats = *nodeStats

	return &n.Stats, nil
}

func (n *Node) GetLabels(ctx context.Context) (map[string]string, error) {
	if n.Labels != nil {
		return n.Labels, nil
	}

	labels, err := n.client.GetLabels(ctx)
	if err != nil {
		return nil, err
	}

	return labels, nil
}
