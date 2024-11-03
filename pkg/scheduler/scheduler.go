package scheduler

import (
	"context"
	"math"
	"sort"

	"github.com/autobrr/distribrr/pkg/node"
	"github.com/autobrr/distribrr/pkg/task"

	"github.com/rs/zerolog/log"
)

const (
	// LIEB square ice constant
	// https://en.wikipedia.org/wiki/Lieb%27s_square_ice_constant
	LIEB = 1.53960071783900203869
)

type Scheduler interface {
	SelectCandidateNodes(ctx context.Context, t task.Task, nodes []*node.Node) []*node.Node
	Score(ctx context.Context, t task.Task, nodes []*node.Node) map[string]float64
	Pick(scores map[string]float64, candidates []*node.Node) []*node.Node
}

type LeastActive struct {
	Name       string
	LastWorker int
}

func (r *LeastActive) SelectCandidateNodes(ctx context.Context, t task.Task, nodes []*node.Node) []*node.Node {
	var candidates []*node.Node

	for _, n := range nodes {
		if n.Status != node.StatusReady {
			continue
		}

		// match nodes by labels
		nodeLabels, err := n.GetLabels(ctx)
		if err != nil {
			log.Error().Err(err).Msgf("could not get labels for node %s", n.Name)
			continue
		}

		if !checkLabels(t.Labels, nodeLabels) {
			continue
		}

		//for _, clientStats := range n.Stats.ClientStats {
		//	if !clientStats.Ready {
		//		continue
		//	}
		//}

		candidates = append(candidates, n)
	}

	return candidates
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

func (r *LeastActive) Score(ctx context.Context, t task.Task, nodes []*node.Node) map[string]float64 {
	nodeScores := make(map[string]float64)

	for _, n := range nodes {
		stats, err := n.GetStats(ctx)
		if err != nil {
			log.Error().Err(err).Msgf("could not get stats for node %s", n.Name)
			continue
		}

		for _, clientStats := range stats.ClientStats {
			if !clientStats.Ready {
				nodeScores[n.Name] = 100.0
				continue
			}

			torrentScore := 0.0

			// score with torrents in mind
			// timeLeft, percentage done, speeds
			for _, torrent := range clientStats.ActiveDownloads {
				if torrent.Progress > 0.5 && torrent.ETA < 30 {
					// score
					torrentScore += 1.0
					continue
				}

				torrentScore += 5.0
			}

			clientCost := math.Pow(LIEB, (float64(clientStats.ActiveDownloadsCount+1))/float64(clientStats.MaxActiveDownloadsAllowed)) + torrentScore

			nodeScores[n.Name] = clientCost

			// check disk here or select?
		}
	}

	return nodeScores
}

func (r *LeastActive) Pick(scores map[string]float64, candidates []*node.Node) []*node.Node {
	//var bestNodes []*node.Node
	//for idx, candidate := range candidates {
	//	n := candidate
	//	if idx == 0 {
	//		bestNodes = append(bestNodes, n)
	//		continue
	//	}
	//}
	//
	//return bestNodes
	return candidates
}

func (r *LeastActive) PickN(scores map[string]float64, candidates []*node.Node, number int) []*node.Node {
	// select amount of candidates if greater than 0
	//if number > 0 && len(candidates) > number {
	if number > 0 && number > len(candidates) {
		candidates = candidates[:number]
		return candidates
	}

	//if number > 0 {
	//	if len(candidates) > number {
	//		candidates = candidates[:number]
	//	}
	//}
	//return candidates

	// Create a ByScore instance
	byScore := ByScore{
		nodes:  candidates,
		scores: scores,
	}

	// Sort the slice using sort.Sort
	sort.Sort(byScore)

	//minCost := 0.00
	//var bestNodes []*node.Node
	//for idx, candidate := range candidates {
	//	n := candidate
	//	if idx == 0 {
	//		minCost = scores[n.Name]
	//		bestNodes = append(bestNodes, n)
	//		continue
	//	}
	//
	//	if scores[n.Name] < minCost {
	//		minCost = scores[n.Name]
	//		bestNodes = append(bestNodes, n)
	//	}
	//}

	return byScore.nodes[:number]

	//return bestNodes
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
	return bs.scores[bs.nodes[i].Name] < bs.scores[bs.nodes[j].Name]
}
