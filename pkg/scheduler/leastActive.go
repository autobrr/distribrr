package scheduler

import (
	"context"

	"github.com/autobrr/distribrr/pkg/node"
	"github.com/autobrr/distribrr/pkg/task"

	"github.com/rs/zerolog/log"
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
	for _, n := range nodes {
		// match nodes by labels
		nodeLabels, err := n.GetLabels(ctx)
		if err != nil {
			log.Error().Err(err).Msgf("could not get labels for node %s", n.Name)
			continue
		}

		if !checkLabels(t.Labels, nodeLabels) {
			continue
		}
	}

	return nodes
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
			if clientStats.Ready {
				nodeScores[n.Name] = 1.0
			}

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
