package stats

import (
	"cmp"
	"math"
	"strings"

	"github.com/autobrr/go-qbittorrent"
)

// Score rates how well the client can take a new torrent; higher is better.
// It starts at 100, adds 20 when the client has no active downloads, and
// otherwise subtracts a penalty per active download weighted by its remaining
// progress and ETA.
func (c *ClientStats) Score() float64 {
	const (
		baseScore     = 100.0
		noActiveBonus = 20.0
	)

	if c.ActiveDownloadsCount == 0 {
		return baseScore + noActiveBonus
	}

	score := baseScore
	for _, torrent := range c.ActiveDownloads {
		score -= calculateTorrentPenalty(torrent)
	}

	return score
}

// CompareClients orders clients best first, for slices.SortFunc and
// slices.MinFunc: by higher Score, then by more storage headroom (no storage
// rules counts as unlimited), then by name. The agent adds a torrent to the
// first client in this order, and the scheduler scores a node by it.
func CompareClients(a, b ClientStats) int {
	return cmp.Or(
		cmp.Compare(b.Score(), a.Score()),
		cmp.Compare(b.headroom(), a.headroom()),
		strings.Compare(a.Name, b.Name),
	)
}

// headroom is StorageHeadroom with no storage rules as unlimited.
func (c *ClientStats) headroom() uint64 {
	if c.StorageHeadroom == nil {
		return math.MaxUint64
	}

	return *c.StorageHeadroom
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
		normalizedETA := min(float64(torrent.ETA), maxETASeconds) / maxETASeconds
		etaPenalty = normalizedETA * etaWeight * basePenalty
	}

	return progressPenalty + etaPenalty
}
