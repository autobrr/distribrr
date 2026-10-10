package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/autobrr/distribrr/pkg/stats"
	"github.com/autobrr/distribrr/pkg/task"

	"github.com/autobrr/go-qbittorrent"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

// TestService_GetStatsFull_concurrent guards against the shared-state race
// (CONC-3) that previously crashed the agent when two /stats requests
// overlapped: GetStatsFull reassigned and map-wrote a shared Service.stats.
// Each call now builds its own *stats.Stats. Run with -race to exercise it.
func TestService_GetStatsFull_concurrent(t *testing.T) {
	s := &Service{clients: map[string]*QbitClient{}}

	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if st := s.GetStatsFull(context.Background()); st == nil {
				t.Error("GetStatsFull returned nil")
			}
		})
	}
	wg.Wait()
}

func TestCheckStorage(t *testing.T) {
	const (
		mb = 1_000_000
		gb = 1_000_000_000
	)

	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist")

	tests := []struct {
		name     string
		rules    []StorageRule
		reserved uint64
		dirUsage func(path string) (uint64, error)
		want     []stats.NotReadyReason
		// wantHeadroom is checked when set; wantNoHeadroom expects nil
		wantHeadroom   *uint64
		wantNoHeadroom bool
	}{
		{name: "no rules", rules: nil, want: nil, wantNoHeadroom: true},
		{name: "readable path within limits", rules: []StorageRule{{Path: dir, MinFree: "1B"}}, want: nil},
		{name: "readable path below minFree", rules: []StorageRule{{Path: dir, MinFree: "1000PB"}}, want: []stats.NotReadyReason{stats.ReasonDiskFull}},
		{name: "missing path is unavailable, not full", rules: []StorageRule{{Path: missing, MinFree: "1B"}}, want: []stats.NotReadyReason{stats.ReasonStorageUnavailable}, wantHeadroom: new(uint64(0))},
		{
			name:  "full and unavailable reported together",
			rules: []StorageRule{{Path: dir, MinFree: "1000PB"}, {Path: missing, MinFree: "1B"}},
			want:  []stats.NotReadyReason{stats.ReasonDiskFull, stats.ReasonStorageUnavailable},
		},
		{
			name:     "dir usage below maxUsage",
			rules:    []StorageRule{{Path: dir, MaxUsage: "1200GB"}},
			dirUsage: func(string) (uint64, error) { return 1000 * gb, nil },
			want:     nil,
		},
		{
			name:     "dir usage above maxUsage",
			rules:    []StorageRule{{Path: dir, MaxUsage: "1200GB"}},
			dirUsage: func(string) (uint64, error) { return 1300 * gb, nil },
			want:     []stats.NotReadyReason{stats.ReasonDiskFull},
		},
		{
			name:     "dir usage that cannot be measured is unavailable",
			rules:    []StorageRule{{Path: dir, MaxUsage: "1200GB"}},
			dirUsage: func(string) (uint64, error) { return 0, errors.New("walk failed") },
			want:     []stats.NotReadyReason{stats.ReasonStorageUnavailable},
		},
		{
			name:         "reserved bytes count toward maxUsage",
			rules:        []StorageRule{{Path: dir, MaxUsage: "10MB"}},
			reserved:     5 * mb,
			dirUsage:     func(string) (uint64, error) { return 4 * mb, nil },
			want:         nil,
			wantHeadroom: new(uint64(1 * mb)),
		},
		{
			name:         "reserved bytes push the path over maxUsage",
			rules:        []StorageRule{{Path: dir, MaxUsage: "10MB"}},
			reserved:     7 * mb,
			dirUsage:     func(string) (uint64, error) { return 4 * mb, nil },
			want:         []stats.NotReadyReason{stats.ReasonDiskFull},
			wantHeadroom: new(uint64(0)),
		},
		{
			name:     "reserved bytes larger than free space break minFree",
			rules:    []StorageRule{{Path: dir, MinFree: "1B"}},
			reserved: math.MaxUint64,
			want:     []stats.NotReadyReason{stats.ReasonDiskFull},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dirUsage := tt.dirUsage
			if dirUsage == nil {
				dirUsage = func(string) (uint64, error) {
					t.Error("dirUsage called without a maxUsage rule")
					return 0, nil
				}
			}

			st := stats.GetStats()

			got, headroom := checkStorage(zerolog.Nop(), tt.rules, tt.reserved, st.DiskPathStats, dirUsage)
			assert.Equal(t, tt.want, got)
			assert.NotContains(t, st.DiskPathStats, missing)

			if tt.wantNoHeadroom {
				assert.Nil(t, headroom)
			} else {
				assert.NotNil(t, headroom)
			}
			if tt.wantHeadroom != nil {
				assert.Equal(t, *tt.wantHeadroom, *headroom)
			}
		})
	}
}

func TestDirUsageCache_get(t *testing.T) {
	dir := t.TempDir()

	var c dirUsageCache

	before, err := c.get(dir)
	assert.NoError(t, err)

	assert.NoError(t, os.WriteFile(filepath.Join(dir, "file"), make([]byte, 1<<20), 0o644))

	// within the TTL the cached value is returned, not a fresh walk
	cached, err := c.get(dir)
	assert.NoError(t, err)
	assert.Equal(t, before, cached)

	_, err = c.get(filepath.Join(dir, "does-not-exist"))
	assert.Error(t, err)
}

// fakeQbit serves the two qBittorrent endpoints clientStats calls: one active
// download with amountLeft bytes left, and the preallocate_all preference.
func fakeQbit(t *testing.T, amountLeft int64, preallocate bool) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/torrents/info":
			json.NewEncoder(w).Encode([]qbittorrent.Torrent{{Hash: "abc", State: qbittorrent.TorrentStateDownloading, AmountLeft: amountLeft}})
		case "/api/v2/app/preferences":
			json.NewEncoder(w).Encode(qbittorrent.AppPreferences{PreallocateAll: preallocate})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	return srv
}

// newRoomService returns an agent with one client whose only storage rule
// leaves about room bytes free on a temp dir, before reserving active downloads.
func newRoomService(t *testing.T, room uint64, amountLeft int64, preallocate bool) *Service {
	t.Helper()

	dir := t.TempDir()

	disk, err := stats.GetDiskInfoByPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	if disk.Free < 2*room {
		t.Skipf("not enough free space in %s for the test", dir)
	}

	srv := fakeQbit(t, amountLeft, preallocate)

	cfg := NewConfig()
	cfg.Clients["qbit"] = &QbitClient{
		Host: srv.URL,
		Rules: ClientRules{
			Torrents: TorrentRules{MaxActiveDownloads: 10},
			Storage:  []StorageRule{{Path: dir, MinFree: strconv.FormatUint(disk.Free-room, 10)}},
		},
	}

	return NewService(cfg)
}

func TestService_clientsWithRoom(t *testing.T) {
	const mb = 1_000_000

	tests := []struct {
		name        string
		amountLeft  int64
		preallocate bool
		size        uint64
		wantErr     bool
	}{
		{name: "unknown size always fits", size: 0},
		{name: "fits in free room", size: 100 * mb},
		{name: "larger than room", size: 1000 * mb, wantErr: true},
		{name: "active download reserves the room", amountLeft: 450 * mb, size: 100 * mb, wantErr: true},
		{name: "preallocated download reserves nothing", amountLeft: 450 * mb, preallocate: true, size: 100 * mb},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 500MB of room leaves slack for other writes to the temp filesystem
			s := newRoomService(t, 500*mb, tt.amountLeft, tt.preallocate)

			clients, err := s.clientsWithRoom(t.Context(), tt.size)
			if tt.wantErr {
				assert.ErrorIs(t, err, ErrInsufficientStorage)
				assert.Empty(t, clients)
				return
			}

			assert.NoError(t, err)
			assert.Len(t, clients, 1)
		})
	}
}

func TestAPIServer_tasks_insufficientStorage(t *testing.T) {
	const mb = 1_000_000

	s := newRoomService(t, 500*mb, 0, false)
	s.cfg.Http.Token = "secret"

	te := task.NewEvent()
	// the size check runs before the .torrent is fetched, so this is never dialed
	te.Task.DownloadURL = "http://127.0.0.1:0/never.torrent"
	te.Task.Size = 1000 * mb

	body, err := json.Marshal(te)
	assert.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/", bytes.NewReader(body))
	req.Header.Set("X-API-Token", "secret")

	rec := httptest.NewRecorder()
	NewAPIServer(s.cfg, s).Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInsufficientStorage, rec.Code)
	assert.Contains(t, rec.Body.String(), "no client has room for 1.0 GB")
}
