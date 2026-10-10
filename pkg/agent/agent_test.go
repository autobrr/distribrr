package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/autobrr/distribrr/pkg/server/client"
	"github.com/autobrr/distribrr/pkg/stats"
	"github.com/autobrr/distribrr/pkg/task"

	"github.com/autobrr/go-qbittorrent"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			name:         "dir usage not measured yet is not room",
			rules:        []StorageRule{{Path: dir, MaxUsage: "1200GB"}},
			dirUsage:     func(string) (uint64, error) { return 0, errDirUsagePending },
			want:         []stats.NotReadyReason{stats.ReasonStorageMeasuring},
			wantHeadroom: new(uint64(0)),
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

func TestDirUsageCache(t *testing.T) {
	dir := t.TempDir()

	var c dirUsageCache

	// nothing is walked on get, so an unmeasured path is pending
	_, err := c.get(dir)
	assert.ErrorIs(t, err, errDirUsagePending)

	c.refresh(dir)
	before, err := c.get(dir)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "file"), make([]byte, 1<<20), 0o644))

	// get returns the last measured value until the next refresh
	cached, err := c.get(dir)
	require.NoError(t, err)
	assert.Equal(t, before, cached)

	c.refresh(dir)
	after, err := c.get(dir)
	require.NoError(t, err)
	assert.Greater(t, after, before)

	missing := filepath.Join(dir, "does-not-exist")
	c.refresh(missing)
	_, err = c.get(missing)
	assert.Error(t, err)
	assert.NotErrorIs(t, err, errDirUsagePending)
}

func TestDirUsageCache_run(t *testing.T) {
	dir := t.TempDir()

	var c dirUsageCache

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan struct{})
	go func() {
		c.run(ctx, []string{dir}, time.Hour)
		close(done)
	}()

	// the first walk runs right away, without waiting for the interval
	require.Eventually(t, func() bool {
		_, err := c.get(dir)
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop after ctx was cancelled")
	}
}

func TestService_dirUsagePaths(t *testing.T) {
	s := &Service{clients: map[string]*QbitClient{
		"a": {Rules: ClientRules{Storage: []StorageRule{
			{Path: "/data", MaxUsage: "1TB"},
			{Path: "/fast", MinFree: "50GB"},
		}}},
		"b": {Rules: ClientRules{Storage: []StorageRule{
			{Path: "/data", MaxUsage: "2TB"},
		}}},
	}}

	// only maxUsage paths need a walk, and a shared path is walked once
	assert.Equal(t, []string{"/data"}, s.dirUsagePaths())
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

func TestService_readyClients(t *testing.T) {
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

			clients, err := s.readyClients(t.Context(), tt.size)
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

// newReloadService writes content as the agent config file and returns a
// service built from it, the way cmd/agent.go does.
func newReloadService(t *testing.T, content string) (*Service, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, path, content)

	cfg := NewConfig()
	require.NoError(t, cfg.LoadFromFile(path))
	require.NoError(t, cfg.Validate())

	return NewService(cfg), path
}

func clientConfig(name string, extra string) string {
	return `
http:
  token: secret
clients:
  ` + name + `:
    host: http://127.0.0.1:1
` + extra
}

func TestService_reload_swapsClients(t *testing.T) {
	s, path := newReloadService(t, clientConfig("a", ""))
	require.Contains(t, s.getClients(), "a")

	writeFile(t, path, clientConfig("b", `
    rules:
      torrents:
        maxActiveDownloads: 5
`))
	require.NoError(t, s.reload(t.Context()))

	clients := s.getClients()
	require.Len(t, clients, 1)
	require.Contains(t, clients, "b")
	assert.Equal(t, "b", clients["b"].Name)
	assert.NotNil(t, clients["b"].Client)
	assert.Equal(t, 5, clients["b"].Rules.Torrents.MaxActiveDownloads)
	assert.Contains(t, s.config().Clients, "b")
}

func TestService_reload_invalidKeepsConfig(t *testing.T) {
	tests := []struct {
		name  string
		write func(t *testing.T, path string)
	}{
		{name: "unparsable yaml", write: func(t *testing.T, path string) { writeFile(t, path, "clients: [unclosed") }},
		{name: "fails validation", write: func(t *testing.T, path string) {
			writeFile(t, path, "http:\n  token: secret\nclients:\n  b:\n    user: admin\n")
		}},
		{name: "file removed", write: func(t *testing.T, path string) { require.NoError(t, os.Remove(path)) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, path := newReloadService(t, clientConfig("a", ""))
			cfg, clients := s.config(), s.getClients()

			tt.write(t, path)
			assert.Error(t, s.reload(t.Context()))

			assert.Same(t, cfg, s.config())
			assert.Equal(t, clients, s.getClients())
		})
	}
}

func TestService_reload_withoutConfigFile(t *testing.T) {
	s := NewService(NewConfig())
	assert.Error(t, s.reload(t.Context()))
}

func TestService_reload_keepsHttp(t *testing.T) {
	s, path := newReloadService(t, `
http:
  host: 127.0.0.1
  port: 7430
  token: secret
agent:
  labels:
    disktype: hdd
`)

	writeFile(t, path, `
http:
  host: 0.0.0.0
  port: 9999
  token: other
agent:
  labels:
    disktype: ssd
`)
	require.NoError(t, s.reload(t.Context()))

	assert.Equal(t, Http{Host: "127.0.0.1", Port: "7430", Token: "secret"}, s.config().Http)
	// the rest of the file still applies
	assert.Equal(t, map[string]string{"disktype": "ssd"}, s.GetLabels())
}

func TestService_reload_restartsDirUsage(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()

	storage := func(dir string) string {
		return `
    rules:
      storage:
        - path: ` + dir + `
          maxUsage: 1TB
`
	}

	s, path := newReloadService(t, clientConfig("a", storage(dirA)))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	s.reloadM.Lock()
	s.watchDirUsage(ctx, s.dirUsagePaths())
	s.reloadM.Unlock()

	writeFile(t, path, clientConfig("a", storage(dirB)))
	require.NoError(t, s.reload(ctx))

	assert.Equal(t, []string{dirB}, s.dirUsageWatching)
	require.Eventually(t, func() bool {
		_, err := s.dirUsage.get(dirB)
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)
}

// TestService_reload_concurrentReads runs request-path readers while the config
// is reloaded. Run with -race.
func TestService_reload_concurrentReads(t *testing.T) {
	s, path := newReloadService(t, clientConfig("a", ""))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for ctx.Err() == nil {
				for _, c := range s.getClients() {
					_ = c.Rules.Torrents.MaxActiveDownloads
				}
				_ = s.GetLabels()
				_ = s.dirUsagePaths()
			}
		})
	}

	for i := range 50 {
		writeFile(t, path, clientConfig("c"+strconv.Itoa(i), ""))
		require.NoError(t, s.reload(ctx))
	}

	cancel()
	wg.Wait()

	assert.Contains(t, s.getClients(), "c49")
}

// fakeManager records the join and deregister requests an agent sends.
type fakeManager struct {
	*httptest.Server

	m           sync.Mutex
	joins       []serverclient.JoinRequest
	deregisters []serverclient.DeregisterRequest
}

func newFakeManager(t *testing.T) *fakeManager {
	t.Helper()

	f := &fakeManager{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.m.Lock()
		defer f.m.Unlock()

		switch r.URL.Path {
		case "/api/v1/node/register":
			var req serverclient.JoinRequest
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			f.joins = append(f.joins, req)
			w.WriteHeader(http.StatusCreated)
		case "/api/v1/node/deregister":
			var req serverclient.DeregisterRequest
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			f.deregisters = append(f.deregisters, req)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)

	return f
}

func (f *fakeManager) requests() ([]serverclient.JoinRequest, []serverclient.DeregisterRequest) {
	f.m.Lock()
	defer f.m.Unlock()

	return slices.Clone(f.joins), slices.Clone(f.deregisters)
}

func TestService_reload_rejoins(t *testing.T) {
	manager := newFakeManager(t)

	agentConfig := func(nodeName, disktype string) string {
		return `
http:
  token: secret
agent:
  nodeName: ` + nodeName + `
  clientAddr: http://agent:7430
  labels:
    disktype: ` + disktype + `
manager:
  addr: ` + manager.URL + `
  token: secret
`
	}

	s, path := newReloadService(t, agentConfig("node1", "hdd"))
	require.NoError(t, s.registerAgentWithServer(t.Context(), time.Second))

	// changed labels: join again under the same name
	writeFile(t, path, agentConfig("node1", "ssd"))
	require.NoError(t, s.reload(t.Context()))

	require.Eventually(t, s.joined, 5*time.Second, 10*time.Millisecond)
	joins, deregisters := manager.requests()
	require.Len(t, joins, 2)
	assert.Equal(t, map[string]string{"disktype": "ssd"}, joins[1].Labels)
	assert.Empty(t, deregisters)

	// renamed node: drop the old registration, then join under the new name
	writeFile(t, path, agentConfig("node2", "ssd"))
	require.NoError(t, s.reload(t.Context()))

	require.Eventually(t, s.joined, 5*time.Second, 10*time.Millisecond)
	joins, deregisters = manager.requests()
	require.Len(t, joins, 3)
	assert.Equal(t, "node2", joins[2].NodeName)
	require.Len(t, deregisters, 1)
	assert.Equal(t, "node1", deregisters[0].NodeName)

	// clients only: no new join
	writeFile(t, path, agentConfig("node2", "ssd")+`
clients:
  a:
    host: http://127.0.0.1:1
`)
	require.NoError(t, s.reload(t.Context()))
	joins, _ = manager.requests()
	assert.Len(t, joins, 3)
}

// serve runs s.Serve on a random local port and returns its base url, the
// reload channel and a func that cancels ctx and returns Serve's error. The
// test cleanup stops it too.
func serve(t *testing.T, s *Service) (string, chan<- struct{}, func() error) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	reload := make(chan struct{})

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.Serve(ctx, ln, reload)
	}()

	stop := sync.OnceValue(func() error {
		cancel()

		select {
		case err := <-errCh:
			return err
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after ctx was cancelled")
			return nil
		}
	})
	t.Cleanup(func() { stop() })

	return "http://" + ln.Addr().String(), reload, stop
}

func TestService_Serve(t *testing.T) {
	s := NewService(NewConfig())

	url, _, stop := serve(t, s)

	resp, err := http.Get(url + "/api/v1/healthz/liveness")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	require.NoError(t, stop())

	// the listener is closed
	_, err = http.Get(url + "/api/v1/healthz/liveness")
	assert.Error(t, err)
}

func TestService_Serve_reload(t *testing.T) {
	s, path := newReloadService(t, clientConfig("a", ""))

	_, reload, stop := serve(t, s)

	writeFile(t, path, clientConfig("b", ""))
	reload <- struct{}{}

	require.Eventually(t, func() bool {
		_, ok := s.getClients()["b"]
		return ok
	}, 5*time.Second, 10*time.Millisecond)

	require.NoError(t, stop())
}

func TestService_Serve_deregistersOnShutdown(t *testing.T) {
	var (
		m     sync.Mutex
		calls []string
	)

	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "server-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		m.Lock()
		calls = append(calls, r.URL.Path)
		m.Unlock()

		switch r.URL.Path {
		case "/api/v1/node/register":
			w.WriteHeader(http.StatusCreated)
		case "/api/v1/node/deregister":
			var req serverclient.DeregisterRequest
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			assert.Equal(t, "agent1", req.NodeName)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(manager.Close)

	cfg := NewConfig()
	cfg.Agent.NodeName = "agent1"
	cfg.Manager = Manager{Addr: manager.URL, Token: "server-token"}

	s := NewService(cfg)
	s.RegisterInterval = 50 * time.Millisecond

	_, _, stop := serve(t, s)

	require.Eventually(t, s.joined, 5*time.Second, 10*time.Millisecond)

	require.NoError(t, stop())

	m.Lock()
	defer m.Unlock()
	assert.Equal(t, []string{"/api/v1/node/register", "/api/v1/node/deregister"}, calls)
}
