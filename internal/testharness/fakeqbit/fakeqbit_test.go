package fakeqbit

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/autobrr/distribrr/internal/testharness/faketracker"

	"github.com/autobrr/go-qbittorrent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newClient builds a client the way the agent's buildClients does.
func newClient(s *Server) *qbittorrent.Client {
	return qbittorrent.NewClient(qbittorrent.Config{
		Host:          s.URL(),
		Username:      s.Username(),
		Password:      s.Password(),
		TLSSkipVerify: true,
		Timeout:       60,
	})
}

func TestServer_Login(t *testing.T) {
	s := New(t)
	ctx := context.Background()

	require.NoError(t, newClient(s).LoginCtx(ctx))

	bad := qbittorrent.NewClient(qbittorrent.Config{Host: s.URL(), Username: "admin", Password: "wrong"})
	assert.ErrorIs(t, bad.LoginCtx(ctx), qbittorrent.ErrBadCredentials)
}

func TestServer_RequiresSession(t *testing.T) {
	s := New(t)

	resp, err := http.Get(s.URL() + "/api/v2/torrents/info")
	require.NoError(t, err)
	resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestServer_ActiveDownloads(t *testing.T) {
	s := New(t)
	c := newClient(s)
	ctx := context.Background()

	got, err := c.GetTorrentsActiveDownloadsCtx(ctx)
	require.NoError(t, err)
	assert.Empty(t, got)

	s.SetActiveDownloads(
		Torrent{Hash: "aaa", Name: "a", Size: 1000, AmountLeft: 250, Progress: 0.75, ETA: 30},
		Torrent{Hash: "bbb", Name: "b", Size: 2000, AmountLeft: 2000, State: "stalledDL"},
		Torrent{Hash: "ccc", Name: "c", Size: 3000, State: "uploading"},
		Torrent{Hash: "ddd", Name: "d", Size: 4000, State: "pausedDL"},
	)

	got, err = c.GetTorrentsActiveDownloadsCtx(ctx)
	require.NoError(t, err)
	require.Len(t, got, 2)

	assert.Equal(t, "aaa", got[0].Hash)
	assert.Equal(t, "a", got[0].Name)
	assert.Equal(t, int64(1000), got[0].Size)
	assert.Equal(t, int64(250), got[0].AmountLeft)
	assert.InDelta(t, 0.75, got[0].Progress, 1e-9)
	assert.Equal(t, int64(30), got[0].ETA)
	assert.Equal(t, qbittorrent.TorrentStateDownloading, got[0].State)
	assert.Equal(t, qbittorrent.TorrentStateStalledDl, got[1].State)

	assert.Equal(t, 1, s.Requests("auth/login"))
	assert.Equal(t, 2, s.Requests("torrents/info"))
}

func TestServer_Preferences(t *testing.T) {
	s := New(t)
	c := newClient(s)
	ctx := context.Background()

	prefs, err := c.GetAppPreferencesCtx(ctx)
	require.NoError(t, err)
	assert.False(t, prefs.PreallocateAll)

	s.SetPreallocateAll(true)

	prefs, err = c.GetAppPreferencesCtx(ctx)
	require.NoError(t, err)
	assert.True(t, prefs.PreallocateAll)
}

func TestServer_AddTorrentFromUrl(t *testing.T) {
	s := New(t)
	c := newClient(s)
	ctx := context.Background()

	res, err := c.AddTorrentFromUrlCtx(ctx, "http://tracker.test/one.torrent", map[string]string{"category": "tv", "tags": "distribrr"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), res.SuccessCount)

	added := s.WaitAdded(t, 1, time.Second)
	require.Len(t, added, 1)
	assert.Equal(t, "http://tracker.test/one.torrent", added[0].URL)
	assert.Equal(t, "tv", added[0].Category)
	assert.Equal(t, "distribrr", added[0].Tags)

	// adding does not make it an active download by default
	active, err := c.GetTorrentsActiveDownloadsCtx(ctx)
	require.NoError(t, err)
	assert.Empty(t, active)

	s.FailAdd(true)

	_, err = c.AddTorrentFromUrlCtx(ctx, "http://tracker.test/two.torrent", map[string]string{})
	assert.ErrorIs(t, err, qbittorrent.ErrTorrentAddFailed)
	assert.Len(t, s.Added(), 1)
	assert.Equal(t, 2, s.Requests("torrents/add"))
}

func TestServer_AddedBecomeActive(t *testing.T) {
	tr := faketracker.New(t)
	url, hash := tr.Add("Some.Release-GRP", 10<<20)

	s := New(t, WithAddedBecomeActive())
	c := newClient(s)
	ctx := context.Background()

	_, err := c.AddTorrentFromUrlCtx(ctx, url, map[string]string{})
	require.NoError(t, err)

	added := s.Added()
	require.Len(t, added, 1)
	assert.Equal(t, hash, added[0].Hash)
	assert.Equal(t, int64(10<<20), added[0].Size)

	active, err := c.GetTorrentsActiveDownloadsCtx(ctx)
	require.NoError(t, err)
	require.Len(t, active, 1)
	assert.Equal(t, hash, active[0].Hash)
	assert.Equal(t, int64(10<<20), active[0].AmountLeft)
	assert.Equal(t, 1, tr.Downloads("Some.Release-GRP"))
}

func TestServer_Reannounce(t *testing.T) {
	s := New(t)
	c := newClient(s)

	start := time.Now()
	err := c.ReannounceTorrentWithRetry(context.Background(), "aaa", &qbittorrent.ReannounceOptions{Interval: 1, MaxAttempts: 3})
	require.NoError(t, err)

	// the library sleeps one interval before the first check; working trackers end it there
	assert.Less(t, time.Since(start), 2*time.Second)
	assert.Equal(t, 1, s.Requests("torrents/trackers"))
	assert.Equal(t, 0, s.Requests("torrents/reannounce"))
}

func TestServer_ReannounceNotWorking(t *testing.T) {
	s := New(t)
	s.SetTrackersWorking(false)
	c := newClient(s)

	err := c.ReannounceTorrentWithRetry(context.Background(), "aaa", &qbittorrent.ReannounceOptions{Interval: 1, MaxAttempts: 1})
	require.NoError(t, err)
	assert.Equal(t, 1, s.Requests("torrents/reannounce"))
}

func TestServer_ExpireSessions(t *testing.T) {
	s := New(t)
	c := newClient(s)
	ctx := context.Background()

	_, err := c.GetTorrentsActiveDownloadsCtx(ctx)
	require.NoError(t, err)

	s.ExpireSessions()

	// the client gets 403, logs in again and retries
	_, err = c.GetTorrentsActiveDownloadsCtx(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, s.Requests("auth/login"))
}

func TestServer_SetDown(t *testing.T) {
	s := New(t)
	ctx := context.Background()

	// logged in before going down
	c := newClient(s)
	_, err := c.GetTorrentsActiveDownloadsCtx(ctx)
	require.NoError(t, err)

	s.SetDown(true)

	start := time.Now()
	_, err = c.GetTorrentsActiveDownloadsCtx(ctx)
	require.Error(t, err)

	// never logged in
	_, err = newClient(s).GetTorrentsActiveDownloadsCtx(ctx)
	require.Error(t, err)

	_, err = c.AddTorrentFromUrlCtx(ctx, "http://tracker.test/one.torrent", map[string]string{})
	require.Error(t, err)
	assert.Less(t, time.Since(start), time.Second)
	assert.Empty(t, s.Added())

	s.SetDown(false)

	_, err = c.GetTorrentsActiveDownloadsCtx(ctx)
	require.NoError(t, err)
}

func TestServer_AddTorrentFromMemory(t *testing.T) {
	tr := faketracker.New(t)
	_, hash := tr.Add("Memory.Release-GRP", 10<<20)

	s := New(t, WithAddedBecomeActive())
	c := newClient(s)
	ctx := context.Background()

	_, err := c.AddTorrentFromMemoryCtx(ctx, tr.Torrent("Memory.Release-GRP"), map[string]string{"category": "tv"})
	require.NoError(t, err)

	added := s.Added()
	require.Len(t, added, 1)
	assert.Empty(t, added[0].URL)
	assert.Equal(t, hash, added[0].Hash)
	assert.Equal(t, "Memory.Release-GRP", added[0].Name)
	assert.Equal(t, int64(10<<20), added[0].Size)
	assert.Equal(t, "tv", added[0].Category)

	active, err := c.GetTorrentsActiveDownloadsCtx(ctx)
	require.NoError(t, err)
	require.Len(t, active, 1)
	assert.Equal(t, hash, active[0].Hash)
	assert.Zero(t, tr.Downloads("Memory.Release-GRP"), "an uploaded file is not fetched")

	_, err = c.AddTorrentFromMemoryCtx(ctx, []byte("not a torrent"), map[string]string{})
	assert.ErrorIs(t, err, qbittorrent.ErrTorrentAddFailed)
	assert.Len(t, s.Added(), 1)
}
