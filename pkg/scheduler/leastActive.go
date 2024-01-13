package scheduler

import (
	"context"

	"github.com/autobrr/distribrr/pkg/node"
	"github.com/autobrr/distribrr/pkg/task"

	"github.com/rs/zerolog/log"
)

type Scheduler interface {
	SelectCandidateNodes(t task.Task, nodes []*node.Node) []*node.Node
	Score(t task.Task, nodes []*node.Node) map[string]float64
	Pick(scores map[string]float64, candidates []*node.Node) []*node.Node
}

type LeastActive struct {
	Name       string
	LastWorker int
}

func (r *LeastActive) SelectCandidateNodes(t task.Task, nodes []*node.Node) []*node.Node {
	return nodes
}

func (r *LeastActive) Score(ctx context.Context, t task.Task, nodes []*node.Node) map[string]float64 {
	nodeScores := make(map[string]float64)

	for _, n := range nodes {
		n := n

		s, err := n.GetStats()
		if err != nil {
			log.Error().Err(err).Msgf("could not get stats for node %s", n.Name)
			continue
		}

		for _, v := range s.ClientStats {
			if v.ClientReady {
				nodeScores[n.Name] = 1.0
			}
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
