package stats

import (
	"slices"
	"testing"

	"github.com/autobrr/go-qbittorrent"
	"github.com/stretchr/testify/assert"
)

func TestClientStats_Score(t *testing.T) {
	tests := []struct {
		name   string
		client ClientStats
		want   float64
	}{
		{name: "idle gets the bonus", client: ClientStats{}, want: 120},
		{
			name: "one torrent halfway",
			client: ClientStats{
				ActiveDownloadsCount: 1,
				ActiveDownloads:      []qbittorrent.Torrent{{Progress: 0.5, ETA: 65}},
			},
			want: 96.49774305555556,
		},
		{
			name: "every active torrent is penalized",
			client: ClientStats{
				ActiveDownloadsCount: 2,
				ActiveDownloads:      []qbittorrent.Torrent{{Progress: 0.76, ETA: 25}, {Progress: 0.5, ETA: 100}},
			},
			want: 94.81565972222222,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.client.Score())
		})
	}
}

func Test_calculateTorrentPenalty(t *testing.T) {
	tests := []struct {
		name    string
		torrent qbittorrent.Torrent
		want    float64
	}{
		{name: "just started, no ETA", torrent: qbittorrent.Torrent{Progress: 0}, want: 7},
		{name: "done", torrent: qbittorrent.Torrent{Progress: 1}, want: 0},
		{name: "ETA is capped at a day", torrent: qbittorrent.Torrent{Progress: 1, ETA: 8640000}, want: 3},
		{name: "half a day left", torrent: qbittorrent.Torrent{Progress: 0.5, ETA: 12 * 60 * 60}, want: 3.5 + 1.5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.InDelta(t, tt.want, calculateTorrentPenalty(tt.torrent), 1e-9)
		})
	}
}

func TestCompareClients(t *testing.T) {
	const gb = 1_000_000_000

	busy := ClientStats{Name: "busy", ActiveDownloadsCount: 1, ActiveDownloads: []qbittorrent.Torrent{{Progress: 0.1, ETA: 3600}}}
	nearlyDone := ClientStats{Name: "nearlydone", ActiveDownloadsCount: 1, ActiveDownloads: []qbittorrent.Torrent{{Progress: 0.9, ETA: 60}}}
	idleSmall := ClientStats{Name: "a-small", StorageHeadroom: new(uint64(10 * gb))}
	idleBig := ClientStats{Name: "b-big", StorageHeadroom: new(uint64(100 * gb))}
	idleNoRules := ClientStats{Name: "c-norules"}
	idleNoRules2 := ClientStats{Name: "d-norules"}

	names := func(clients []ClientStats) []string {
		out := make([]string, len(clients))
		for i, c := range clients {
			out[i] = c.Name
		}
		return out
	}

	tests := []struct {
		name    string
		clients []ClientStats
		want    []string
	}{
		{name: "higher score first", clients: []ClientStats{busy, nearlyDone, idleSmall}, want: []string{"a-small", "nearlydone", "busy"}},
		{name: "same score: more headroom first", clients: []ClientStats{idleSmall, idleBig}, want: []string{"b-big", "a-small"}},
		{name: "no storage rules counts as unlimited", clients: []ClientStats{idleBig, idleNoRules, idleSmall}, want: []string{"c-norules", "b-big", "a-small"}},
		{name: "same score and headroom: by name", clients: []ClientStats{idleNoRules2, idleNoRules}, want: []string{"c-norules", "d-norules"}},
		{name: "score beats headroom", clients: []ClientStats{busy, idleSmall}, want: []string{"a-small", "busy"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sorted := slices.Clone(tt.clients)
			slices.SortFunc(sorted, CompareClients)
			assert.Equal(t, tt.want, names(sorted))

			// order of the input does not matter
			slices.Reverse(sorted)
			slices.SortFunc(sorted, CompareClients)
			assert.Equal(t, tt.want, names(sorted))
		})
	}

	assert.Zero(t, CompareClients(idleBig, idleBig))
}
