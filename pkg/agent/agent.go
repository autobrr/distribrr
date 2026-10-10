package agent

import (
	"context"
	"fmt"
	"maps"
	"math"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/autobrr/distribrr/internal/domain"
	"github.com/autobrr/distribrr/pkg/server/client"
	"github.com/autobrr/distribrr/pkg/stats"
	"github.com/autobrr/distribrr/pkg/task"

	"github.com/autobrr/go-qbittorrent"
	"github.com/c9s/goprocinfo/linux"
	"github.com/dustin/go-humanize"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/sync/errgroup"
)

type Service struct {
	cfg *Config

	controlNode *controlNode
	clients     map[string]*QbitClient
	dirUsage    dirUsageCache

	serverClient *serverclient.Client
}

type controlNode struct {
	Addr  string
	Token string
}

func NewService(cfg *Config) *Service {
	s := &Service{
		cfg:     cfg,
		clients: map[string]*QbitClient{},
	}

	s.initClients()

	if s.cfg.Manager.Addr != "" && s.cfg.Manager.Token != "" {
		s.serverClient = serverclient.NewClient(s.cfg.Manager.Addr, s.cfg.Manager.Token)
	}

	return s
}

func (s *Service) Run() {
	srv := NewAPIServer(s.cfg, s)

	// register agent with server
	go s.Register()

	// keep dir usage for maxUsage rules measured in the background
	go s.dirUsage.run(context.Background(), s.dirUsagePaths(), dirUsageInterval)

	errorChannel := make(chan error)
	go func() {
		errorChannel <- srv.Open()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGKILL, syscall.SIGTERM)

	for sig := range sigCh {
		log.Info().Msgf("got signal %q, shutting down server", sig)

		if err := s.Deregister(); err != nil {
			os.Exit(1)
		}

		os.Exit(0)
	}
}

func (s *Service) initClients() {
	for name, c := range s.cfg.Clients {
		c := c
		client := qbittorrent.NewClient(qbittorrent.Config{
			Host:          c.Host,
			Username:      c.User,
			Password:      c.Pass,
			TLSSkipVerify: true,
			BasicUser:     c.BasicUser,
			BasicPass:     c.BasicPass,
			Timeout:       60,
			Log:           nil,
		})
		c.Name = name
		c.Client = client
		s.clients[name] = c
	}
}

func (s *Service) Register() {
	tickerDuration := time.Second * 10
	if err := s.registerAgentWithServer(tickerDuration); err != nil {
		log.Error().Err(err).Msgf("could not register agent and join server: %s", s.cfg.Manager.Addr)
	}

	ticker := time.NewTicker(tickerDuration) //
	defer ticker.Stop()

	for s.controlNode == nil {
		select {
		case <-ticker.C:
			if err := s.registerAgentWithServer(tickerDuration); err != nil {
				log.Error().Err(err).Msgf("could not register agent and join server: %s", s.cfg.Manager.Addr)
			}
		}
	}
}

func (s *Service) registerAgentWithServer(tickerDuration time.Duration) error {
	log.Debug().Msgf("preparing to register agent and join server: %s", s.cfg.Manager.Addr)

	// Create a new context that will be done after tickerDuration
	ctx, cancel := context.WithTimeout(context.Background(), tickerDuration)
	defer cancel()
	if err := s.Join(ctx, s.cfg.Manager.Addr, s.cfg.Manager.Token, s.cfg.Agent, s.cfg.Http.Token); err != nil {
		return err
	}

	return nil
}

func (s *Service) Deregister() error {
	ctx := context.Background()

	log.Info().Msgf("deregister node with server")

	req := serverclient.DeregisterRequest{
		NodeName:   s.cfg.Agent.NodeName,
		ClientAddr: s.cfg.Agent.ClientAddr,
	}

	if err := s.serverClient.DeregisterRequest(ctx, req); err != nil {
		log.Error().Err(err).Msg("could not deregister node")
		return err
	}

	return nil
}

// Join worker to manager
func (s *Service) Join(ctx context.Context, addr string, token string, agent Agent, agentToken string) error {
	log.Info().Msgf("sending join request to: %s", addr)

	if s.serverClient == nil {
		s.serverClient = serverclient.NewClient(addr, token)
	}

	nodeName := agent.NodeName
	if nodeName == "" {
		h, err := os.Hostname()
		if err != nil {
			log.Error().Err(err).Msg("could not get hostname")
		}

		if h != "" {
			nodeName = h
		}
	}

	//s.serverClient = serverclient.NewClient(addr, token)

	joinReq := serverclient.JoinRequest{
		NodeName:   nodeName,
		ClientAddr: agent.ClientAddr,
		Labels:     agent.Labels,
		Token:      agentToken,
	}

	if err := s.serverClient.JoinRequest(ctx, joinReq); err != nil {
		return err
	}

	log.Info().Msgf("successfully joined manager: %s", addr)

	// set controlNode
	s.controlNode = &controlNode{
		Addr:  addr,
		Token: token,
	}

	return nil
}

func (s *Service) Healthcheck(ctx context.Context) error {
	// TODO check client(s) and report status
	return nil
}

func (s *Service) GetTasks() {
	// get tasks
}

func (s *Service) AddTask(t task.Task) {

}

func (s *Service) RunTasks() {
	// for queue runTask
}

func (s *Service) runTask(t task.Task) error {
	if err := s.StartTask(t); err != nil {
		return err
	}

	return nil
}

func (s *Service) StartTask(t task.Task) error {
	sender := errgroup.Group{}
	//downloads := 0

	ctx := context.Background()

	opts := map[string]string{}

	if t.Category != "" {
		opts["category"] = t.Category
	}

	if t.Tags != "" {
		opts["tags"] = t.Tags
	}

	// when the caller sent a size, reject before fetching the .torrent if no
	// client has room for it
	if t.Size > 0 {
		if _, err := s.clientsWithRoom(ctx, t.Size); err != nil {
			return err
		}
	}

	rel := domain.NewRelease(t.DownloadURL, t.Name, t.Indexer)
	if err := rel.DownloadTorrentFile(ctx); err != nil {
		return err
	}

	// the size from the .torrent is exact; fall back to the caller's
	size := rel.Size
	if size == 0 {
		size = t.Size
	}

	clients, err := s.clientsWithRoom(ctx, size)
	if err != nil {
		return err
	}

	for _, client := range clients {
		sender.Go(func() error {
			log.Debug().Msgf("add torrent %s to client %s", t.Name, client.Name)

			// TODO read from memory

			// send downloads
			if _, err := client.Client.AddTorrentFromUrlCtx(ctx, t.DownloadURL, opts); err != nil {
				log.Error().Err(err).Msgf("error adding torrent from file %s to qbit: %s", t.Name, client.Name)
				return err
			}

			// handle reannounce
			if rel.Hash != "" {
				go func(clientName string, hash string, name string) {
					log.Debug().Msgf("trying to re-announce torrent: %s %s", hash, name)

					// reannounce
					options := qbittorrent.ReannounceOptions{
						Interval:        7,
						MaxAttempts:     50,
						DeleteOnFailure: false,
					}

					if err := client.Client.ReannounceTorrentWithRetry(context.Background(), hash, &options); err != nil {
						log.Error().Err(err).Msgf("error re-announcing torrent %s on qbit: %s", hash, clientName)
						return
					}

					log.Debug().Msgf("successfully re-announced torrent: %s %s", hash, name)
				}(client.Name, rel.Hash, rel.Name)
			}

			//downloads++

			log.Debug().Msgf("successfully added torrent: %s", t.Name)

			return nil
		})
	}

	if err := sender.Wait(); err != nil {
		log.Error().Err(err).Msg("error adding torrent to client")
		return err
	}

	return nil
}

// ErrInsufficientStorage is returned by StartTask when no client has room for
// the torrent under its storage rules.
var ErrInsufficientStorage = errors.New("insufficient storage")

// clientsWithRoom returns the clients whose storage rules have room for a
// torrent of size bytes. With an unknown size (0) every client is returned. It
// returns ErrInsufficientStorage when no client has room.
func (s *Service) clientsWithRoom(ctx context.Context, size uint64) ([]*QbitClient, error) {
	if size == 0 {
		return slices.Collect(maps.Values(s.clients)), nil
	}

	var clients []*QbitClient
	var skipped []string

	for _, client := range s.clients {
		ct := s.clientStats(ctx, client, map[string]*linux.Disk{})

		if !ct.HasRoomFor(size) {
			log.Debug().Msgf("client %s has no room for %s (headroom %s)", client.Name, humanize.Bytes(size), humanize.Bytes(*ct.StorageHeadroom))
			skipped = append(skipped, fmt.Sprintf("%s: %s room", client.Name, humanize.Bytes(*ct.StorageHeadroom)))
			continue
		}

		clients = append(clients, client)
	}

	if len(clients) == 0 {
		return nil, errors.Wrapf(ErrInsufficientStorage, "no client has room for %s (%s)", humanize.Bytes(size), strings.Join(skipped, ", "))
	}

	return clients, nil
}

func (s *Service) StopTask(t task.Task) {

}

func (s *Service) InspectTask(t task.Task) {

}

func (s *Service) UpdateTasks(t task.Task) {
	for {
		s.updateTasks()
		time.Sleep(15 * time.Second)
	}
}

func (s *Service) updateTasks() {

}

func (s *Service) CollectStats() {
	for {
		log.Trace().Msg("collecting stats")
		//s.stats = stats.GetStats()
		//
		////for _, client := range s.clients {
		////	s.stats.GetActiveDownloads(client.Name, stats.ClientStatsReader())
		////}
		//s.taskCount = s.stats.TaskCount
		s.GetStats()
		time.Sleep(15 * time.Second)
	}
}

func (s *Service) GetStatsFull(ctx context.Context) *stats.Stats {
	st := stats.GetStats()
	s.collectClientStats(ctx, st)
	return st
}

func (s *Service) GetStats() *stats.Stats {
	log.Trace().Msg("collecting stats")
	return stats.GetStats()
}

// collectClientStats fills st with per-client torrent and storage status. It
// operates only on the passed-in *stats.Stats (never on shared Service state),
// so concurrent /stats requests cannot race on the same maps.
func (s *Service) collectClientStats(ctx context.Context, st *stats.Stats) {
	log.Trace().Msg("collecting client stats")

	// TODO use errgroup
	for name, client := range s.clients {
		st.ClientStats[name] = s.clientStats(ctx, client, st.DiskPathStats)
	}
}

// clientStats reports the torrent and storage status of one client. Disk stats
// for its storage paths are recorded in diskPaths.
func (s *Service) clientStats(ctx context.Context, client *QbitClient, diskPaths map[string]*linux.Disk) stats.ClientStats {
	l := log.With().Str("client", client.Name).Logger()

	l.Trace().Msg("get active torrents for client")

	activeDownloads, err := client.Client.GetTorrentsActiveDownloadsCtx(ctx)
	if err != nil {
		l.Error().Err(err).Msgf("could not load active torrents for client")

		// can't reach the client: report it as not ready so the scheduler
		// skips it and the reason is visible, instead of silently dropping it.
		storageReasons, headroom := checkStorage(l, client.Rules.Storage, 0, diskPaths, s.dirUsage.get)

		return stats.ClientStats{
			Name:                      client.Name,
			MaxActiveDownloadsAllowed: client.Rules.Torrents.MaxActiveDownloads,
			Status:                    stats.ClientStatusNotReady,
			Reasons:                   append([]stats.NotReadyReason{stats.ReasonClientUnreachable}, storageReasons...),
			StorageHeadroom:           headroom,
		}
	}

	l.Trace().Msg("check disk per path for client")

	reserved := reservedBytes(ctx, l, client, activeDownloads)
	storageReasons, headroom := checkStorage(l, client.Rules.Storage, reserved, diskPaths, s.dirUsage.get)

	// a client is at capacity once it reaches its max active downloads, but
	// we still allow it through if one of those downloads is nearly finished.
	downloadsOK := len(activeDownloads) < client.Rules.Torrents.MaxActiveDownloads
	if !downloadsOK && len(activeDownloads) == client.Rules.Torrents.MaxActiveDownloads {
		l.Debug().Msgf("max active downloads (%d) reached, checking individual torrents...", client.Rules.Torrents.MaxActiveDownloads)

		for _, torrent := range activeDownloads {
			// if progress is above 75% and ETA is less than 60 seconds, treat it as a free slot
			if torrent.Progress >= 0.75 && torrent.ETA <= 60 {
				downloadsOK = true
				break
			}
		}
	}

	var reasons []stats.NotReadyReason
	if !downloadsOK {
		reasons = append(reasons, stats.ReasonMaxDownloadsReached)
	}
	reasons = append(reasons, storageReasons...)

	status := stats.ClientStatusReady
	if len(reasons) > 0 {
		status = stats.ClientStatusNotReady
	}

	l.Trace().Msgf("found %d active torrents for client", len(activeDownloads))

	ct := stats.ClientStats{
		Name:                      client.Name,
		MaxActiveDownloadsAllowed: client.Rules.Torrents.MaxActiveDownloads,
		ActiveDownloadsCount:      len(activeDownloads),
		ActiveDownloads:           activeDownloads,
		Status:                    status,
		Reasons:                   reasons,
		StorageHeadroom:           headroom,
	}

	l.Trace().Msgf("[%d/%d] active downloads, status: %s", len(activeDownloads), client.Rules.Torrents.MaxActiveDownloads, ct.Status)
	l.Debug().Msgf("client status: %s", ct.Status)

	return ct
}

// reservedBytes is the space active downloads will still write: the sum of
// their remaining bytes. When qBittorrent preallocates files that space is
// already taken on disk, so nothing is reserved. If the preference cannot be
// read we reserve anyway, since overestimating is the safe side.
func reservedBytes(ctx context.Context, l zerolog.Logger, client *QbitClient, activeDownloads []qbittorrent.Torrent) uint64 {
	if len(activeDownloads) == 0 || len(client.Rules.Storage) == 0 {
		return 0
	}

	prefs, err := client.Client.GetAppPreferencesCtx(ctx)
	if err != nil {
		l.Warn().Err(err).Msg("could not read client preferences, assuming files are not preallocated")
	} else if prefs.PreallocateAll {
		return 0
	}

	return sumAmountLeft(activeDownloads)
}

func sumAmountLeft(torrents []qbittorrent.Torrent) uint64 {
	var total uint64
	for _, t := range torrents {
		if t.AmountLeft > 0 {
			total += uint64(t.AmountLeft)
		}
	}

	return total
}

// checkStorage checks every storage rule against its path and records the disk
// stats in diskPaths. reserved is space already promised to active downloads:
// it is taken off free space and added to used space before the rules are
// checked. It returns ReasonDiskFull when a readable path breaks its
// minFree / maxUsage rule, and ReasonStorageUnavailable when a path cannot be
// read at all (missing mount, typo), so the two are not confused. dirUsage
// returns the space used under a path and is only called for maxUsage rules;
// until a path has been measured the client reports ReasonStorageMeasuring,
// since unknown usage must not count as room.
//
// The returned headroom is the smallest room left across all rules, 0 when any
// rule fails, and nil when there are no rules.
func checkStorage(l zerolog.Logger, rules []StorageRule, reserved uint64, diskPaths map[string]*linux.Disk, dirUsage func(path string) (uint64, error)) ([]stats.NotReadyReason, *uint64) {
	if len(rules) == 0 {
		return nil, nil
	}

	diskFull := false
	unavailable := false
	measuring := false
	headroom := uint64(math.MaxUint64)

	for _, storage := range rules {
		l.Trace().Msgf("check disk for path %q", storage.Path)

		disk, err := stats.GetDiskInfoByPath(storage.Path)
		if err != nil {
			unavailable = true
			l.Error().Err(err).Msgf("storage path %q is unavailable", storage.Path)
			continue
		}

		diskPaths[storage.Path] = disk

		var used uint64
		if storage.MaxUsage != "" {
			used, err = dirUsage(storage.Path)
			if errors.Is(err, errDirUsagePending) {
				measuring = true
				l.Debug().Msgf("usage of storage path %q is still being measured", storage.Path)
				continue
			}
			if err != nil {
				unavailable = true
				l.Error().Err(err).Msgf("could not measure usage of storage path %q", storage.Path)
				continue
			}
		}

		free := disk.Free - min(disk.Free, reserved)
		used += reserved

		room, ok, err := storage.headroom(free, used)
		if err != nil {
			// bad threshold config: log and treat the rule as non-binding
			l.Warn().Err(err).Msgf("ignoring storage rule for path %q", storage.Path)
		}

		if !ok {
			diskFull = true
			l.Debug().Msgf("storage path %q does not satisfy rule (minFree=%q maxUsage=%q free=%d used=%d reserved=%d bytes)", storage.Path, storage.MinFree, storage.MaxUsage, disk.Free, used-reserved, reserved)
		}

		headroom = min(headroom, room)
	}

	var reasons []stats.NotReadyReason
	if diskFull {
		reasons = append(reasons, stats.ReasonDiskFull)
	}
	if unavailable {
		reasons = append(reasons, stats.ReasonStorageUnavailable)
		headroom = 0
	}
	if measuring {
		reasons = append(reasons, stats.ReasonStorageMeasuring)
		headroom = 0
	}

	return reasons, &headroom
}

// dirUsageInterval is how often the storage paths with a maxUsage rule are
// walked. Walking a large torrent directory is slow, so it runs in the
// background and /stats only reads the last result.
const dirUsageInterval = time.Minute

// errDirUsagePending means a path has not been measured yet, because the first
// background walk is still running.
var errDirUsagePending = errors.New("dir usage not measured yet")

type dirUsageEntry struct {
	used uint64
	err  error
}

// dirUsageCache holds the last measured stats.GetDirUsage per path. run keeps it
// fresh in the background; get never walks. The zero value is ready to use.
type dirUsageCache struct {
	m       sync.RWMutex
	entries map[string]dirUsageEntry
}

// get returns the last measured usage of path, the error from its last walk,
// or errDirUsagePending when it has not been measured yet.
func (c *dirUsageCache) get(path string) (uint64, error) {
	c.m.RLock()
	e, ok := c.entries[path]
	c.m.RUnlock()

	if !ok {
		return 0, errDirUsagePending
	}

	return e.used, e.err
}

// refresh walks path and stores the result.
func (c *dirUsageCache) refresh(path string) {
	used, err := stats.GetDirUsage(path)

	c.m.Lock()
	if c.entries == nil {
		c.entries = map[string]dirUsageEntry{}
	}
	c.entries[path] = dirUsageEntry{used: used, err: err}
	c.m.Unlock()
}

// run measures every path right away, then again every interval, until ctx is done.
func (c *dirUsageCache) run(ctx context.Context, paths []string, interval time.Duration) {
	if len(paths) == 0 {
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		for _, path := range paths {
			if ctx.Err() != nil {
				return
			}

			start := time.Now()
			c.refresh(path)
			log.Trace().Msgf("measured dir usage of %q in %s", path, time.Since(start))
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// dirUsagePaths returns the storage paths that have a maxUsage rule, which are
// the only ones that need their dir usage measured.
func (s *Service) dirUsagePaths() []string {
	var paths []string
	for _, client := range s.clients {
		for _, storage := range client.Rules.Storage {
			if storage.MaxUsage != "" && !slices.Contains(paths, storage.Path) {
				paths = append(paths, storage.Path)
			}
		}
	}

	return paths
}

func (s *Service) GetLabels() map[string]string {
	return s.cfg.Agent.Labels
}
