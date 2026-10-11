package scheduler

import (
	"context"
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/autobrr/distribrr/pkg/node"
	"github.com/autobrr/distribrr/pkg/stats"
	"github.com/autobrr/distribrr/pkg/task"

	"github.com/dustin/go-humanize"
	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
)

// Scheduler types a task can ask for in task.Task.SchedulerType.
const (
	TypeLeastActive = "least_active"
	TypeRoundRobin  = "round_robin"
)

var ErrUnknownType = errors.New("unknown scheduler type")

type Scheduler interface {
	SelectCandidateNodes(ctx context.Context, t task.Task, nodes []*node.Node) ([]*node.Node, []NodeRejection)
	Score(ctx context.Context, t task.Task, nodes []*node.Node) map[string]float64
	PickN(scores map[string]float64, candidates []*node.Node, number int) []*node.Node
}

// NodeRejection records why a node was not selected as a candidate, so callers
// can explain to the user why a task could not be scheduled anywhere.
type NodeRejection struct {
	Node    string
	Reasons []string
}

// LeastActive picks the nodes with the fewest and furthest along active
// downloads.
type LeastActive struct{}

func (r *LeastActive) SelectCandidateNodes(ctx context.Context, t task.Task, nodes []*node.Node) ([]*node.Node, []NodeRejection) {
	return selectCandidates(ctx, t, nodes)
}

// selectCandidates keeps the nodes that can take the task and records why the
// others cannot. Every scheduler shares it. Nodes are checked concurrently, so
// a slow node delays the task by at most ctx's deadline, not once per node.
func selectCandidates(ctx context.Context, t task.Task, nodes []*node.Node) ([]*node.Node, []NodeRejection) {
	reasons := make([][]string, len(nodes))

	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Go(func() {
			reasons[i] = checkNode(ctx, t, n)
		})
	}
	wg.Wait()

	var candidates []*node.Node
	var rejected []NodeRejection

	for i, n := range nodes {
		if reasons[i] != nil {
			rejected = append(rejected, NodeRejection{Node: n.Name, Reasons: reasons[i]})
			continue
		}

		candidates = append(candidates, n)
	}

	return candidates, rejected
}

// checkNode returns why n cannot take the task, or nil if it can.
func checkNode(ctx context.Context, t task.Task, n *node.Node) []string {
	if status := n.Status(); status != node.StatusReady {
		return []string{fmt.Sprintf("node not ready (%s)", status)}
	}

	// match nodes by labels
	nodeLabels, err := n.GetLabels(ctx)
	if err != nil {
		log.Error().Err(err).Msgf("could not get labels for node %s", n.Name)
		return []string{"could not fetch labels"}
	}

	if !checkLabels(t.Labels, nodeLabels) {
		return []string{"labels do not match task"}
	}

	stat, err := n.GetStats(ctx)
	if err != nil {
		log.Error().Err(err).Msgf("could not get stats for node %s", n.Name)
		return []string{"could not fetch stats"}
	}

	// the agent adds the torrent to one of its ready clients with room, so
	// the node needs one such client, not all of them
	ready := readyClients(stat.ClientStats)
	if len(ready) == 0 {
		var clientReasons []string
		for _, clientStats := range stat.ClientStats {
			clientReasons = append(clientReasons, fmt.Sprintf("client %s: %s", clientStats.Name, formatReasons(clientStats.Reasons)))
		}
		if len(clientReasons) == 0 {
			clientReasons = []string{"no clients"}
		}
		slices.Sort(clientReasons)

		return clientReasons
	}

	if !hasRoomFor(ready, t.Size) {
		return []string{fmt.Sprintf("no ready client has room for %s", humanize.Bytes(t.Size))}
	}

	return nil
}

// readyClients returns the clients that report READY.
func readyClients(clients map[string]stats.ClientStats) map[string]stats.ClientStats {
	ready := make(map[string]stats.ClientStats, len(clients))
	for name, c := range clients {
		if c.Status == stats.ClientStatusReady {
			ready[name] = c
		}
	}

	return ready
}

// usableClients returns the ready clients with room for a torrent of size
// bytes, the ones the agent picks from.
func usableClients(clients map[string]stats.ClientStats, size uint64) map[string]stats.ClientStats {
	usable := readyClients(clients)
	maps.DeleteFunc(usable, func(_ string, c stats.ClientStats) bool {
		return !c.HasRoomFor(size)
	})

	return usable
}

// hasRoomFor reports whether at least one client has storage room for a torrent
// of size bytes. An unknown size (0) always fits.
func hasRoomFor(clients map[string]stats.ClientStats, size uint64) bool {
	if size == 0 {
		return true
	}

	for _, c := range clients {
		if c.HasRoomFor(size) {
			return true
		}
	}

	return false
}

// formatReasons renders client not-ready reasons for human-readable output.
func formatReasons(reasons []stats.NotReadyReason) string {
	if len(reasons) == 0 {
		return "not ready"
	}

	parts := make([]string, len(reasons))
	for i, reason := range reasons {
		parts[i] = string(reason)
	}

	return strings.Join(parts, ", ")
}

// checkLabels match nodes by labels
func checkLabels(taskLabels map[string]string, nodeLabels map[string]string) bool {
	for key, value := range taskLabels {
		v, ok := nodeLabels[key]
		if !ok || v != value {
			return false
		}
	}

	return true
}

// Score gives each node the score of the client the agent will add the
// torrent to: its best usable client by stats.CompareClients.
func (r *LeastActive) Score(ctx context.Context, t task.Task, nodes []*node.Node) map[string]float64 {
	nodeScores := make(map[string]float64, len(nodes))

	for _, n := range nodes {
		best, ok := bestClient(n.Stats().ClientStats, t.Size)
		if !ok {
			// candidate selection drops such nodes; rank one last if it slips through
			nodeScores[n.Name] = math.Inf(-1)
			continue
		}

		nodeScores[n.Name] = best.Score()
	}

	return nodeScores
}

// bestClient returns the usable client the agent picks for a torrent of size
// bytes, and false when there is none.
func bestClient(clients map[string]stats.ClientStats, size uint64) (stats.ClientStats, bool) {
	usable := slices.Collect(maps.Values(usableClients(clients, size)))
	if len(usable) == 0 {
		return stats.ClientStats{}, false
	}

	return slices.MinFunc(usable, stats.CompareClients), true
}

func (r *LeastActive) PickN(scores map[string]float64, candidates []*node.Node, number int) []*node.Node {
	if len(candidates) == 0 {
		return nil
	}

	number = replicas(number, len(candidates))

	// Create a ByScore instance
	byScore := ByScore{
		nodes:  candidates,
		scores: scores,
	}

	// Sort the slice using sort.Sort
	sort.Sort(byScore)

	return byScore.nodes[:number]
}

// replicas defaults an unset replica count to one and clamps it to the
// candidates available.
func replicas(number, candidates int) int {
	if number <= 0 {
		number = 1
	}

	return min(number, candidates)
}

// RoundRobin takes turns across nodes in name order. It remembers the last
// node it picked, so it must be shared between tasks.
type RoundRobin struct {
	m    sync.Mutex
	last string
}

func (r *RoundRobin) SelectCandidateNodes(ctx context.Context, t task.Task, nodes []*node.Node) ([]*node.Node, []NodeRejection) {
	return selectCandidates(ctx, t, nodes)
}

// Score gives every node the same score; the turn order decides.
func (r *RoundRobin) Score(ctx context.Context, t task.Task, nodes []*node.Node) map[string]float64 {
	scores := make(map[string]float64, len(nodes))
	for _, n := range nodes {
		scores[n.Name] = 0
	}

	return scores
}

// PickN picks number nodes, starting after the last node picked. The turn
// is kept by name rather than index, so nodes coming and going do not reset
// it or skip a node.
func (r *RoundRobin) PickN(scores map[string]float64, candidates []*node.Node, number int) []*node.Node {
	if len(candidates) == 0 {
		return nil
	}

	number = replicas(number, len(candidates))

	sorted := slices.Clone(candidates)
	slices.SortFunc(sorted, func(a, b *node.Node) int {
		return strings.Compare(a.Name, b.Name)
	})

	r.m.Lock()
	defer r.m.Unlock()

	start := slices.IndexFunc(sorted, func(n *node.Node) bool {
		return n.Name > r.last
	})
	if start < 0 {
		start = 0
	}

	picked := make([]*node.Node, 0, number)
	for i := range number {
		picked = append(picked, sorted[(start+i)%len(sorted)])
	}

	r.last = picked[len(picked)-1].Name

	return picked
}

// ByScore implements sort.Interface based on the score map
type ByScore struct {
	nodes  []*node.Node
	scores map[string]float64
}

func (bs ByScore) Len() int {
	return len(bs.nodes)
}

func (bs ByScore) Swap(i, j int) {
	bs.nodes[i], bs.nodes[j] = bs.nodes[j], bs.nodes[i]
}

func (bs ByScore) Less(i, j int) bool {
	//return bs.scores[bs.nodes[i].Name] < bs.scores[bs.nodes[j].Name]
	return bs.scores[bs.nodes[i].Name] > bs.scores[bs.nodes[j].Name]
}
