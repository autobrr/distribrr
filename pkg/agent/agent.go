package agent

import (
	"context"
	"fmt"
	"maps"
	"math"
	"net"
	"os"
	"os/signal"
	"reflect"
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
)

// shutdownTimeout bounds how long deregistering and in-flight requests get on shutdown.
const shutdownTimeout = 30 * time.Second

// Defaults for the Service timing fields.
const (
	defaultRegisterInterval      = 10 * time.Second
	defaultDirUsageInterval      = time.Minute
	defaultReannounceInterval    = 7
	defaultReannounceMaxAttempts = 50
	defaultTaskTimeout           = 60 * time.Second
)

type Service struct {
	// Timings. NewService sets the defaults; change them before calling Serve.

	// RegisterInterval is how often Register retries joining the server, how
	// long a single join attempt may take, and how often a joined agent checks
	// that the server still knows it.
	RegisterInterval time.Duration
	// DirUsageInterval is how often the storage paths with a maxUsage rule are
	// walked. Walking a large torrent directory is slow, so it runs in the
	// background and /stats only reads the last result.
	DirUsageInterval time.Duration
	// ReannounceInterval is the number of seconds between re-announce attempts
	// after a torrent is added.
	ReannounceInterval int
	// ReannounceMaxAttempts is how many times a torrent is re-announced at most.
	ReannounceMaxAttempts int
	// TaskTimeout bounds StartTask: checking the clients, fetching the
	// .torrent and adding it. The server waits longer than this for the
	// agent's answer, so a slow tracker fails here first.
	TaskTimeout time.Duration

	// m guards cfg, clients, serverClient, controlNode and serveCtx. reload swaps cfg and
	// clients for new values instead of changing them in place, so readers can
	// keep using what they got after releasing the lock.
	m   sync.RWMutex
	cfg *Config

	controlNode *controlNode
	clients     map[string]*QbitClient
	dirUsage    dirUsageCache

	serverClient *serverclient.Client

	// serveCtx is the ctx of the running Serve, see baseContext.
	serveCtx context.Context

	// reloadM serializes reloads and guards the dir usage loop fields.
	reloadM          sync.Mutex
	dirUsageCancel   context.CancelFunc
	dirUsageWatching []string
}

type controlNode struct {
	Addr  string
	Token string
	// NodeName is the name the agent joined with, the hostname when
	// agent.nodeName is empty.
	NodeName string
}

func NewService(cfg *Config) *Service {
	s := &Service{
		RegisterInterval:      defaultRegisterInterval,
		DirUsageInterval:      defaultDirUsageInterval,
		ReannounceInterval:    defaultReannounceInterval,
		ReannounceMaxAttempts: defaultReannounceMaxAttempts,
		TaskTimeout:           defaultTaskTimeout,

		cfg:     cfg,
		clients: buildClients(cfg),
	}

	if s.cfg.Manager.Addr != "" && s.cfg.Manager.Token != "" {
		s.serverClient = serverclient.NewClient(s.cfg.Manager.Addr, s.cfg.Manager.Token)
	}

	return s
}

// Run serves on the configured host and port until SIGINT, SIGQUIT or SIGTERM,
// then deregisters and shuts down gracefully. SIGHUP reloads the config.
func (s *Service) Run() error {
	cfg := s.config()
	addr := net.JoinHostPort(cfg.Http.Host, cfg.Http.Port)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return errors.Wrapf(err, "could not open listener on: %s", addr)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM)
	defer stop()

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)

	reload := make(chan struct{})
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-hup:
				log.Info().Msg("got SIGHUP")
				select {
				case reload <- struct{}{}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return s.Serve(ctx, ln, reload)
}

// Serve runs the agent on ln until ctx is done or the http server fails. It
// registers with the server in the background and keeps the dir usage of
// maxUsage paths measured. A value on reload reloads the config; a failed
// reload is logged and the current config kept. reload may be nil.
// When ctx is done the agent deregisters first, so the server stops sending
// tasks, then in-flight requests get the rest of shutdownTimeout to finish.
// Serve returns nil on a clean shutdown. ln is closed when Serve returns.
func (s *Service) Serve(ctx context.Context, ln net.Listener, reload <-chan struct{}) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	s.m.Lock()
	s.serveCtx = ctx
	s.m.Unlock()

	srv := NewAPIServer(s.config(), s)

	errorChannel := make(chan error, 1)
	go func() {
		errorChannel <- srv.Serve(ln)
	}()

	// register agent with server
	go s.Register(ctx)
	go s.watchRegistration(ctx)

	// keep dir usage for maxUsage rules measured in the background
	s.reloadM.Lock()
	s.watchDirUsage(ctx, s.dirUsagePaths())
	s.reloadM.Unlock()

	for ctx.Err() == nil {
		select {
		case err := <-errorChannel:
			return errors.Wrap(err, "http server stopped")
		case <-reload:
			log.Info().Msg("reloading config")
			if err := s.reload(ctx); err != nil {
				log.Error().Err(err).Msg("could not reload config, keeping the current config")
			}
		case <-ctx.Done():
		}
	}

	log.Info().Msg("shutting down agent")

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancelShutdown()

	// deregister first so the server stops sending tasks while in-flight ones finish
	deregisterErr := s.Deregister(shutdownCtx)

	shutdownErr := srv.Shutdown(shutdownCtx)

	// Serve returns right after Shutdown starts; wait for it so ln is closed
	if err := <-errorChannel; err != nil {
		return errors.Wrap(err, "http server stopped")
	}

	if shutdownErr != nil {
		return errors.Wrap(shutdownErr, "could not shut down http server")
	}

	if deregisterErr != nil {
		return errors.Wrap(deregisterErr, "could not deregister agent")
	}

	return nil
}

// baseContext returns the ctx of the running Serve, or context.Background()
// when Serve has not been called. Work that outlives a request, like
// re-announcing, stops when it is done.
func (s *Service) baseContext() context.Context {
	s.m.RLock()
	defer s.m.RUnlock()

	if s.serveCtx == nil {
		return context.Background()
	}

	return s.serveCtx
}

// config returns the current config. It is replaced, never changed, on reload,
// so callers must not modify it.
func (s *Service) config() *Config {
	s.m.RLock()
	defer s.m.RUnlock()

	return s.cfg
}

// getClients returns the current qbit clients. The map is replaced, never
// changed, on reload, so callers must not modify it.
func (s *Service) getClients() map[string]*QbitClient {
	s.m.RLock()
	defer s.m.RUnlock()

	return s.clients
}

func (s *Service) joined() bool {
	s.m.RLock()
	defer s.m.RUnlock()

	return s.controlNode != nil
}

// buildClients creates a qbit client for every client in cfg.
func buildClients(cfg *Config) map[string]*QbitClient {
	clients := make(map[string]*QbitClient, len(cfg.Clients))

	for name, c := range cfg.Clients {
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
		clients[name] = c
	}

	return clients
}

// reload reads the config file again and applies it: qbit clients and their
// rules, the dir usage loop, and the agent and manager sections, re-joining the
// server when those change. The http section needs a restart and is kept. On
// any error the current config stays in place.
func (s *Service) reload(ctx context.Context) error {
	s.reloadM.Lock()
	defer s.reloadM.Unlock()

	old := s.config()
	if old.path == "" {
		return errors.New("agent was started without a config file, nothing to reload")
	}

	cfg := NewConfig()
	// start from the running http values so ones set by flags and absent from
	// the file do not look changed
	cfg.Http = old.Http

	if err := cfg.LoadFromFile(old.path); err != nil {
		return errors.Wrap(err, "could not load config")
	}

	if err := cfg.Validate(); err != nil {
		return errors.Wrap(err, "invalid config")
	}

	if cfg.Http != old.Http {
		log.Warn().Msg("http config changed, restart the agent to apply it")
		cfg.Http = old.Http
	}

	clients := buildClients(cfg)

	managerChanged := cfg.Manager != old.Manager

	s.m.Lock()
	oldServerClient := s.serverClient
	rejoin := s.controlNode != nil && !sameRegistration(cfg, old)

	s.cfg = cfg
	s.clients = clients
	if managerChanged {
		// Join creates a client for the new manager
		s.serverClient = nil
	}
	if rejoin {
		s.controlNode = nil
	}
	s.m.Unlock()

	if paths := s.dirUsagePaths(); !sameElements(paths, s.dirUsageWatching) {
		s.watchDirUsage(ctx, paths)
	}

	log.Info().
		Int("clients", len(clients)).
		Bool("labels_changed", !maps.Equal(cfg.Agent.Labels, old.Agent.Labels)).
		Bool("rejoin", rejoin).
		Msg("reloaded config")

	if rejoin {
		// the server knows this agent by node name: when the name or the
		// manager changes, the old registration would otherwise linger
		if managerChanged || cfg.Agent.NodeName != old.Agent.NodeName {
			if err := deregister(ctx, oldServerClient, old.Agent); err != nil {
				log.Warn().Err(err).Msg("could not deregister previous registration")
			}
		}

		go s.Register(ctx)
	}

	return nil
}

// watchDirUsage (re)starts the background dir usage loop for paths, stopping
// the previous one. Callers must hold reloadM.
func (s *Service) watchDirUsage(ctx context.Context, paths []string) {
	if s.dirUsageCancel != nil {
		s.dirUsageCancel()
	}

	ctx, cancel := context.WithCancel(ctx)
	s.dirUsageCancel = cancel
	s.dirUsageWatching = paths

	go s.dirUsage.run(ctx, paths, s.DirUsageInterval)
}

func sameElements(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)

	return slices.Equal(a, b)
}

// Register joins the server, retrying until it succeeds or ctx is done. Every
// attempt reads the current config, so a reload applies to the next attempt.
func (s *Service) Register(ctx context.Context) {
	if err := s.registerAgentWithServer(ctx, s.RegisterInterval); err != nil {
		log.Error().Err(err).Msgf("could not register agent and join server: %s", s.config().Manager.Addr)
	}

	ticker := time.NewTicker(s.RegisterInterval)
	defer ticker.Stop()

	for !s.joined() {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.registerAgentWithServer(ctx, s.RegisterInterval); err != nil {
				log.Error().Err(err).Msgf("could not register agent and join server: %s", s.config().Manager.Addr)
			}
		}
	}
}

func (s *Service) registerAgentWithServer(ctx context.Context, tickerDuration time.Duration) error {
	cfg := s.config()

	log.Debug().Msgf("preparing to register agent and join server: %s", cfg.Manager.Addr)

	// Create a new context that will be done after tickerDuration
	ctx, cancel := context.WithTimeout(ctx, tickerDuration)
	defer cancel()
	if err := s.Join(ctx, cfg.Manager.Addr, cfg.Manager.Token, cfg.Agent, cfg.Http.Token); err != nil {
		return err
	}

	// a reload during the join may have changed what the server should know
	if !sameRegistration(cfg, s.config()) {
		s.m.Lock()
		s.controlNode = nil
		s.m.Unlock()

		return errors.New("config reloaded while joining, joining again")
	}

	return nil
}

// watchRegistration checks every RegisterInterval that the server still
// knows the joined agent, and joins again when it does not, e.g. after the
// server restarted from a config file without the node. A node the server
// keeps as REMOVED, because it was taken out of the server config, stays
// removed.
func (s *Service) watchRegistration(ctx context.Context) {
	ticker := time.NewTicker(s.RegisterInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.lostRegistration(ctx) {
				go s.Register(ctx)
			}
		}
	}
}

// lostRegistration reports whether the server no longer knows the joined
// agent, and if so marks the agent as not joined. It is false when the agent
// has not joined, or the server could not be asked.
func (s *Service) lostRegistration(ctx context.Context) bool {
	s.m.RLock()
	cn := s.controlNode
	sc := s.serverClient
	s.m.RUnlock()

	// not joined yet, Register is still trying
	if cn == nil || sc == nil {
		return false
	}

	ctx, cancel := context.WithTimeout(ctx, s.RegisterInterval)
	defer cancel()

	err := sc.CheckNode(ctx, cn.NodeName)
	if err == nil {
		return false
	}

	if !errors.Is(err, serverclient.ErrNodeNotFound) {
		// the server being down is the health checks' business, not a reason to join again
		log.Debug().Err(err).Msg("could not check registration with server")
		return false
	}

	s.m.Lock()
	defer s.m.Unlock()

	// a reload may have started a new join meanwhile
	if s.controlNode != cn {
		return false
	}

	s.controlNode = nil

	log.Warn().Msgf("server does not know node %s anymore, joining again", cn.NodeName)

	return true
}

// sameRegistration reports whether a and b register the agent with the server
// the same way.
func sameRegistration(a, b *Config) bool {
	return a.Manager == b.Manager && reflect.DeepEqual(a.Agent, b.Agent)
}

func (s *Service) Deregister(ctx context.Context) error {
	s.m.RLock()
	joined := s.controlNode != nil
	sc := s.serverClient
	agent := s.cfg.Agent
	s.m.RUnlock()

	// never joined, nothing to deregister
	if !joined {
		return nil
	}

	return deregister(ctx, sc, agent)
}

func deregister(ctx context.Context, sc *serverclient.Client, agent Agent) error {
	if sc == nil {
		return nil
	}

	log.Info().Msgf("deregister node with server")

	req := serverclient.DeregisterRequest{
		NodeName:   agent.NodeName,
		ClientAddr: agent.ClientAddr,
	}

	if err := sc.DeregisterRequest(ctx, req); err != nil {
		log.Error().Err(err).Msg("could not deregister node")
		return err
	}

	return nil
}

// Join worker to manager
func (s *Service) Join(ctx context.Context, addr string, token string, agent Agent, agentToken string) error {
	log.Info().Msgf("sending join request to: %s", addr)

	s.m.Lock()
	if s.serverClient == nil {
		s.serverClient = serverclient.NewClient(addr, token)
	}
	sc := s.serverClient
	s.m.Unlock()

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

	joinReq := serverclient.JoinRequest{
		NodeName:   nodeName,
		ClientAddr: agent.ClientAddr,
		Labels:     agent.Labels,
		Token:      agentToken,
	}

	if err := sc.JoinRequest(ctx, joinReq); err != nil {
		return err
	}

	log.Info().Msgf("successfully joined manager: %s", addr)

	// set controlNode
	s.m.Lock()
	s.controlNode = &controlNode{
		Addr:     addr,
		Token:    token,
		NodeName: nodeName,
	}
	s.m.Unlock()

	return nil
}

func (s *Service) Healthcheck(ctx context.Context) error {
	// TODO check client(s) and report status
	return nil
}

// StartTask adds the torrent to one client: the best ready client with room by
// stats.CompareClients, the same ranking the scheduler scores this node by. If
// the add fails it tries the next client, and fails only when every client
// failed. It gives up when ctx is done, e.g. the server stopped waiting, or
// after TaskTimeout.
func (s *Service) StartTask(ctx context.Context, t task.Task) error {
	ctx, cancel := context.WithTimeout(ctx, s.TaskTimeout)
	defer cancel()

	// the request logger carries the server's correlation id
	l := log.Ctx(ctx)

	opts := map[string]string{}

	if t.Category != "" {
		opts["category"] = t.Category
	}

	if t.Tags != "" {
		opts["tags"] = t.Tags
	}

	// reject before fetching the .torrent if no client is ready, or none has
	// room for the size the caller sent
	if _, err := s.readyClients(ctx, t.Size); err != nil {
		return err
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

	clients, err := s.readyClients(ctx, size)
	if err != nil {
		return err
	}

	slices.SortFunc(clients, func(a, b readyClient) int {
		return stats.CompareClients(a.stats, b.stats)
	})

	var failed []string

	for _, c := range clients {
		l.Debug().Msgf("add torrent %s to client %s", t.Name, c.client.Name)

		// add the file we already fetched: a URL add only queues the fetch in
		// qbit, so a failed second fetch would go unnoticed. go-qbittorrent
		// writes to the options map, so every attempt gets its own copy
		if _, err := c.client.Client.AddTorrentFromMemoryCtx(ctx, rel.File, maps.Clone(opts)); err != nil {
			l.Error().Err(err).Msgf("error adding torrent from file %s to qbit: %s", t.Name, c.client.Name)

			// a timed out add may still have gone through, so another client
			// could end up with the torrent too
			if ctx.Err() != nil {
				return errors.Wrapf(err, "could not add torrent to client %s", c.client.Name)
			}

			failed = append(failed, fmt.Sprintf("%s: %v", c.client.Name, err))
			continue
		}

		l.Debug().Msgf("successfully added torrent %s to client %s", t.Name, c.client.Name)

		if rel.Hash != "" {
			go s.reannounce(l, c.client, rel.Hash, rel.Name)
		}

		return nil
	}

	l.Error().Msg("error adding torrent: all clients failed")

	return errors.Errorf("could not add torrent to any client (%s)", strings.Join(failed, ", "))
}

// reannounce re-announces a torrent just added to client until a tracker
// answers. It outlives the task, so it runs on the Serve ctx.
func (s *Service) reannounce(l *zerolog.Logger, client *QbitClient, hash string, name string) {
	l.Debug().Msgf("trying to re-announce torrent: %s %s", hash, name)

	options := qbittorrent.ReannounceOptions{
		Interval:        s.ReannounceInterval,
		MaxAttempts:     s.ReannounceMaxAttempts,
		DeleteOnFailure: false,
	}

	if err := client.Client.ReannounceTorrentWithRetry(s.baseContext(), hash, &options); err != nil {
		l.Error().Err(err).Msgf("error re-announcing torrent %s on qbit: %s", hash, client.Name)
		return
	}

	l.Debug().Msgf("successfully re-announced torrent: %s %s", hash, name)
}

// ErrInsufficientStorage is returned by StartTask when no ready client has
// room for the torrent.
var ErrInsufficientStorage = errors.New("insufficient storage")

// ErrNoReadyClients is returned by StartTask when no client is ready, e.g. all
// are at their max active downloads, out of disk or unreachable.
var ErrNoReadyClients = errors.New("no ready clients")

// readyClient is a client that can take a torrent, with the stats that say so.
type readyClient struct {
	client *QbitClient
	stats  stats.ClientStats
}

// readyClients returns the clients that are READY and, for a known size, whose
// storage rules have room for a torrent of size bytes. It returns
// ErrNoReadyClients when no client is ready and ErrInsufficientStorage when no
// ready client has room.
func (s *Service) readyClients(ctx context.Context, size uint64) ([]readyClient, error) {
	var ready []readyClient
	var notReady []string
	var noRoom []string

	for _, client := range s.getClients() {
		ct := s.clientStats(ctx, client, map[string]*linux.Disk{})

		if ct.Status != stats.ClientStatusReady {
			notReady = append(notReady, fmt.Sprintf("%s: %s", client.Name, joinReasons(ct.Reasons)))
			continue
		}

		if !ct.HasRoomFor(size) {
			log.Ctx(ctx).Debug().Msgf("client %s has no room for %s (headroom %s)", client.Name, humanize.Bytes(size), humanize.Bytes(*ct.StorageHeadroom))
			noRoom = append(noRoom, fmt.Sprintf("%s: %s room", client.Name, humanize.Bytes(*ct.StorageHeadroom)))
			continue
		}

		ready = append(ready, readyClient{client: client, stats: ct})
	}

	if len(ready) > 0 {
		return ready, nil
	}

	if len(noRoom) > 0 {
		return nil, errors.Wrapf(ErrInsufficientStorage, "no client has room for %s (%s)", humanize.Bytes(size), strings.Join(append(noRoom, notReady...), ", "))
	}

	return nil, errors.Wrapf(ErrNoReadyClients, "no client is ready (%s)", strings.Join(notReady, ", "))
}

func joinReasons(reasons []stats.NotReadyReason) string {
	if len(reasons) == 0 {
		return "not ready"
	}

	parts := make([]string, len(reasons))
	for i, reason := range reasons {
		parts[i] = string(reason)
	}

	return strings.Join(parts, ", ")
}

func (s *Service) GetStatsFull(ctx context.Context) *stats.Stats {
	st := stats.GetStats()
	s.collectClientStats(ctx, st)
	return st
}

// collectClientStats fills st with per-client torrent and storage status. It
// operates only on the passed-in *stats.Stats (never on shared Service state),
// so concurrent /stats requests cannot race on the same maps.
func (s *Service) collectClientStats(ctx context.Context, st *stats.Stats) {
	log.Trace().Msg("collecting client stats")

	// TODO use errgroup
	for name, client := range s.getClients() {
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
	// A limit of 0 means unlimited.
	maxActive := client.Rules.Torrents.MaxActiveDownloads
	downloadsOK := maxActive == 0 || len(activeDownloads) < maxActive
	if !downloadsOK && len(activeDownloads) == maxActive {
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
	for _, client := range s.getClients() {
		for _, storage := range client.Rules.Storage {
			if storage.MaxUsage != "" && !slices.Contains(paths, storage.Path) {
				paths = append(paths, storage.Path)
			}
		}
	}

	return paths
}

func (s *Service) GetLabels() map[string]string {
	return s.config().Agent.Labels
}
