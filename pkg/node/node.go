package node

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
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
	StatusRemoved  = "REMOVED"
)

// Node is the server-side handle for an agent. Health checks, scheduling,
// registration and config reload use the same Node concurrently, so the
// fields that change after creation are unexported and guarded by m.
type Node struct {
	Name            string    `json:"name"`
	Ip              string    `json:"ip"`
	Api             string    `json:"-"`
	MemoryAllocated int64     `json:"-"`
	Role            string    `json:"role"`
	TaskCount       int       `json:"task_count"`
	DateCreated     time.Time `json:"date_created"`

	m      sync.RWMutex
	addr   string
	token  string
	memory int64
	stats  stats.Stats
	status Status
	labels map[string]string
	client *agent.Client
}

func NewNode(name string, clientAddr string, token string, role string) *Node {
	return &Node{
		Name:        name,
		addr:        clientAddr,
		token:       token,
		Role:        role,
		status:      StatusNotReady,
		client:      agent.NewClient(clientAddr, name, token),
		DateCreated: time.Now().UTC(),
	}
}

// MarshalJSON keeps the shape the exported fields used to have.
func (n *Node) MarshalJSON() ([]byte, error) {
	n.m.RLock()
	defer n.m.RUnlock()

	return json.Marshal(struct {
		Name        string            `json:"name"`
		Addr        string            `json:"addr"`
		Ip          string            `json:"ip"`
		Role        string            `json:"role"`
		TaskCount   int               `json:"task_count"`
		DateCreated time.Time         `json:"date_created"`
		Status      Status            `json:"status"`
		Labels      map[string]string `json:"labels"`
	}{
		Name:        n.Name,
		Addr:        n.addr,
		Ip:          n.Ip,
		Role:        n.Role,
		TaskCount:   n.TaskCount,
		DateCreated: n.DateCreated,
		Status:      n.status,
		Labels:      n.labels,
	})
}

func (n *Node) Addr() string {
	n.m.RLock()
	defer n.m.RUnlock()

	return n.addr
}

func (n *Node) Token() string {
	n.m.RLock()
	defer n.m.RUnlock()

	return n.token
}

func (n *Node) Status() Status {
	n.m.RLock()
	defer n.m.RUnlock()

	return n.status
}

func (n *Node) SetStatus(status Status) {
	n.m.Lock()
	defer n.m.Unlock()

	n.status = status
}

// SetHealth records a health check result. It never overrides REMOVED: a
// check that started before a deregister must not bring the node back.
func (n *Node) SetHealth(status Status) {
	n.m.Lock()
	defer n.m.Unlock()

	if n.status == StatusRemoved {
		return
	}

	n.status = status
}

// Labels returns the labels the agent registered with. The map is replaced,
// never changed, so callers must not modify it.
func (n *Node) Labels() map[string]string {
	n.m.RLock()
	defer n.m.RUnlock()

	return n.labels
}

func (n *Node) SetLabels(labels map[string]string) {
	n.m.Lock()
	defer n.m.Unlock()

	n.labels = labels
}

// Stats returns the stats from the last GetStats call.
func (n *Node) Stats() stats.Stats {
	n.m.RLock()
	defer n.m.RUnlock()

	return n.stats
}

func (n *Node) SetStats(st stats.Stats) {
	n.m.Lock()
	defer n.m.Unlock()

	n.stats = st
}

// Update points the node at a new address and token and replaces its labels.
// The agent client is bound to addr and token, so it is rebuilt.
// Status is left untouched; callers decide whether the node is ready.
func (n *Node) Update(addr string, token string, labels map[string]string) {
	client := agent.NewClient(addr, n.Name, token)

	n.m.Lock()
	defer n.m.Unlock()

	n.addr = addr
	n.token = token
	n.labels = labels
	n.client = client
}

func (n *Node) getClient() *agent.Client {
	n.m.RLock()
	defer n.m.RUnlock()

	return n.client
}

func (n *Node) StartTask(ctx context.Context, te *task.Event) error {
	if err := n.getClient().StartTask(ctx, te); err != nil {
		return err
	}

	return nil
}

func (n *Node) HealthCheck(ctx context.Context) error {
	return n.getClient().HealthCheck(ctx)
}

func (n *Node) VerifyToken(ctx context.Context) error {
	return n.getClient().VerifyToken(ctx)
}

// GetStats fetches fresh stats from the agent and stores them. The returned
// value belongs to the caller.
func (n *Node) GetStats(ctx context.Context) (*stats.Stats, error) {
	nodeStats, err := n.getClient().GetStats(ctx)
	if err != nil {
		return nil, err
	}

	if nodeStats.MemStats == nil {
		return nil, fmt.Errorf("error getting stats from node %s", n.Name)
	}

	n.m.Lock()
	n.memory = int64(nodeStats.MemTotalKb())
	n.stats = *nodeStats
	n.m.Unlock()

	return nodeStats, nil
}

func (n *Node) GetLabels(ctx context.Context) (map[string]string, error) {
	if labels := n.Labels(); labels != nil {
		return labels, nil
	}

	labels, err := n.getClient().GetLabels(ctx)
	if err != nil {
		return nil, err
	}

	return labels, nil
}
