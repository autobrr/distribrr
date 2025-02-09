package scheduler

import (
	"context"
	"math"
	"sort"

	"github.com/autobrr/distribrr/pkg/node"
	"github.com/autobrr/distribrr/pkg/stats"
	"github.com/autobrr/distribrr/pkg/task"

	"github.com/autobrr/go-qbittorrent"
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

nodeLoop:
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

		// TODO check available disk

		stat, err := n.GetStats(ctx)
		if err != nil {
			log.Error().Err(err).Msgf("could not get stats for node %s", n.Name)
			continue
		}

		for _, clientStats := range stat.ClientStats {
			if clientStats.Status != stats.ClientStatusReady {
				continue nodeLoop
			}
		}

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
	baseScore := 100.0    // Start with a high base score
	noActiveBonus := 20.0 // Bonus for having no active downloads

	for _, n := range nodes {
		score := baseScore

		for _, clientStats := range n.Stats.ClientStats {
			if clientStats.ActiveDownloadsCount == 0 {
				// Bonus for having no active downloads
				score += noActiveBonus
				continue
			}

			// Calculate penalties for each active download
			for _, torrent := range clientStats.ActiveDownloads {
				penalty := calculateTorrentPenalty(torrent)
				score -= penalty
			}
		}

		nodeScores[n.Name] = score
	}

	return nodeScores
}

// calculateTorrentPenalty determines the penalty for a single torrent based on its progress and ETA
func calculateTorrentPenalty(torrent qbittorrent.Torrent) float64 {
	const (
		// 24 hours in seconds as max ETA
		maxETASeconds  = 24 * 60 * 60
		basePenalty    = 10.0
		progressWeight = 0.7 // Weight for progress contribution
		etaWeight      = 0.3 // Weight for ETA contribution
	)

	// Progress penalty (less penalty for higher progress)
	// Progress is between 0 and 1 (0% to 100%)
	progressPenalty := (1 - torrent.Progress) * progressWeight * basePenalty

	// ETA penalty (more penalty for higher ETA)
	etaPenalty := 0.0
	if torrent.ETA > 0 {
		// Normalize ETA (in seconds) against maxETASeconds
		normalizedETA := math.Min(float64(torrent.ETA), float64(maxETASeconds)) / float64(maxETASeconds)
		etaPenalty = normalizedETA * etaWeight * basePenalty
	}

	return progressPenalty + etaPenalty
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
	if len(candidates) == 0 {
		return nil
	}

	// select amount of candidates if greater than 0
	//if number > 0 && len(candidates) > number {
	if number > 0 && number > len(candidates) {
		candidates = candidates[:number]
		return candidates
	}

	// Create a ByScore instance
	byScore := ByScore{
		nodes:  candidates,
		scores: scores,
	}

	// Sort the slice using sort.Sort
	sort.Sort(byScore)

	return byScore.nodes[:number]
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
