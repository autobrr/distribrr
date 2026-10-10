package faketracker

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/autobrr/distribrr/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTracker_Add(t *testing.T) {
	tr := New(t)

	const size = 3*PieceLength + 123
	url, hash := tr.Add("Some.Release-GRP", size)

	rel := domain.NewRelease(url, "Some.Release-GRP", "fake")
	require.NoError(t, rel.DownloadTorrentFile(context.Background()))

	assert.Equal(t, hash, rel.Hash)
	assert.Equal(t, uint64(size), rel.Size)
	assert.Equal(t, 1, tr.Downloads("Some.Release-GRP"))

	// same name and size give the same infohash
	_, hash2 := tr.Add("Some.Release-GRP", size)
	assert.Equal(t, hash, hash2)

	_, other := tr.Add("Other.Release-GRP", size)
	assert.NotEqual(t, hash, other)
}

func TestTracker_AddStatus(t *testing.T) {
	tr := New(t)

	url := tr.AddStatus("missing", http.StatusNotFound)

	start := time.Now()
	err := domain.NewRelease(url, "missing", "fake").DownloadTorrentFile(context.Background())
	require.Error(t, err)

	// 404 is unrecoverable: one attempt, no 3s retry delay
	assert.Less(t, time.Since(start), 2*time.Second)
	assert.Equal(t, 1, tr.Downloads("missing"))
}

func TestTracker_Unknown(t *testing.T) {
	tr := New(t)

	resp, err := http.Get(tr.URL() + "/torrent/nope.torrent")
	require.NoError(t, err)
	resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, 1, tr.Downloads("nope"))
}
