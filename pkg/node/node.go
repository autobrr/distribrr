package node

import (
	"context"
	"fmt"
	"time"

	"github.com/autobrr/distribrr/pkg/agent"
	"github.com/autobrr/distribrr/pkg/stats"
	"github.com/autobrr/distribrr/pkg/task"
)

type Node struct {
	Name            string
	Addr            string
	Ip              string
	Api             string
	Token           string
	Memory          int64
	MemoryAllocated int64
	Disk            int64
	DiskAllocated   int64
	Stats           stats.Stats
	Role            string
	TaskCount       int
	DateCreated     time.Time

	Client *agent.Client
}

func NewNode(name string, api string, role string) *Node {
	return &Node{
		Name:   name,
		Api:    api,
		Role:   role,
		Client: agent.NewClient(api, name, ""),
	}
}

func (n *Node) StartTask(ctx context.Context, te *task.Event) error {
	err := n.Client.StartTask(ctx, te)
	if err != nil {
		return err
	}

	return nil
}

func (n *Node) HealthCheck(ctx context.Context) error {
	return n.Client.HealthCheck(ctx)
}

func (n *Node) GetStats() (*stats.Stats, error) {
	//var resp *http.Response
	//var err error

	s, err := n.Client.GetStats(context.Background())
	if err != nil {
		return nil, err
	}

	//url := fmt.Sprintf("%s/s", n.Api)
	//resp, err = utils.HTTPWithRetry(http.Get, url)
	//if err != nil {
	//	msg := fmt.Sprintf("Unable to connect to %v. Permanent failure.\n", n.Api)
	//	log.Println(msg)
	//	return nil, errors.New(msg)
	//}
	//
	//if resp.StatusCode != 200 {
	//	msg := fmt.Sprintf("Error retrieving s from %v: %v", n.Api, err)
	//	log.Println(msg)
	//	return nil, errors.New(msg)
	//}
	//
	//defer resp.Body.Close()
	//body, err := io.ReadAll(resp.Body)
	//if err != nil {
	//	return nil, err
	//}
	//
	//var s s.Stats
	//err = json.Unmarshal(body, &s)
	//if err != nil {
	//	msg := fmt.Sprintf("error decoding message while getting s for node %s", n.Name)
	//	log.Println(msg)
	//	return nil, errors.New(msg)
	//}

	if s.MemStats == nil || s.DiskStats == nil {
		return nil, fmt.Errorf("error getting stats from node %s", n.Name)
	}

	n.Memory = int64(s.MemTotalKb())
	n.Disk = int64(s.DiskTotal())
	n.Stats = *s

	return &n.Stats, nil
}
