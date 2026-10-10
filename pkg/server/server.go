package server

import (
	"context"
	"fmt"
	"maps"
	"net"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/autobrr/distribrr/pkg/logger"
	"github.com/autobrr/distribrr/pkg/node"
	"github.com/autobrr/distribrr/pkg/scheduler"
	"github.com/autobrr/distribrr/pkg/task"

	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/sync/errgroup"
)

// shutdownTimeout bounds how long in-flight requests get to finish on shutdown.
const shutdownTimeout = 30 * time.Second

// Defaults for the Service timing fields.
const (
	defaultHealthCheckInterval = 10 * time.Second
	defaultTaskTimeout         = 75 * time.Second
	defaultWebhookTimeout      = 100 * time.Second
)

type Service struct {
	// HealthCheckInterval is how often Serve health checks the nodes. Set it
	// before calling Serve.
	HealthCheckInterval time.Duration
	// TaskTimeout is how long SendWork waits for one agent to accept a task.
	// It must be longer than the agent's own TaskTimeout, so the agent gives
	// up and answers first instead of adding the torrent after the server
	// stopped waiting.
	TaskTimeout time.Duration
	// WebhookTimeout bounds a whole SendWork, node selection included. Keep it
	// below autobrr's webhook timeout (120s).
	WebhookTimeout time.Duration

	cfg         *Config
	workerNodes []*node.Node
	m           sync.RWMutex

	// roundRobin is shared by every task so the turn carries over.
	roundRobin *scheduler.RoundRobin

	log zerolog.Logger
}

func NewService(cfg *Config) *Service {
	s := &Service{
		HealthCheckInterval: defaultHealthCheckInterval,
		TaskTimeout:         defaultTaskTimeout,
		WebhookTimeout:      defaultWebhookTimeout,

		cfg:         cfg,
		workerNodes: make([]*node.Node, 0),
		roundRobin:  &scheduler.RoundRobin{},
		log:         log.Logger.With().Str("module", "server").Logger(),
		m:           sync.RWMutex{},
	}

	s.m.Lock()
	for _, w := range cfg.Nodes {
		if w != nil {
			s.workerNodes = append(s.workerNodes, newWorkerNode(w))
		}
	}
	s.m.Unlock()

	return s
}

// Run serves on the configured host and port until SIGINT, SIGQUIT or SIGTERM,
// then shuts down gracefully. SIGHUP reloads the config.
// newWorkerNode builds the node for a config entry, with the labels it was
// saved with.
func newWorkerNode(entry *AgentNode) *node.Node {
	n := node.NewNode(entry.Name, entry.Addr, entry.Token, "worker")
	if entry.Labels != nil {
		n.SetLabels(entry.Labels)
	}

	return n
}

func (s *Service) Run() error {
	addr := net.JoinHostPort(s.cfg.Http.Host, s.cfg.Http.Port)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return errors.Wrapf(err, "could not open listener on: %s", addr)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM)
	defer stop()

	// SIGHUP reloads the config instead of shutting down
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

// Serve runs the server on ln, along with the node health checks, until ctx
// is done or the http server fails. A value on reload reloads the config; a
// failed reload is logged and the current config kept. reload may be nil.
// When ctx is done in-flight requests get up to shutdownTimeout to finish and
// Serve returns nil. ln is closed when Serve returns.
func (s *Service) Serve(ctx context.Context, ln net.Listener, reload <-chan struct{}) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	srv := NewAPIServer(s.cfg, s)

	errorChannel := make(chan error, 1)
	go func() {
		errorChannel <- srv.Serve(ln)
	}()

	var wg sync.WaitGroup
	wg.Go(func() {
		s.HealthChecks(ctx)
	})

	defer func() {
		cancel()
		wg.Wait()
	}()

	for running := true; running; {
		select {
		case err := <-errorChannel:
			return errors.Wrap(err, "http server stopped")
		case <-reload:
			log.Info().Msg("reloading config")
			if err := s.Reload(); err != nil {
				log.Error().Err(err).Msg("could not reload config, keeping current config")
			}
		case <-ctx.Done():
			log.Info().Msg("shutting down server")
			running = false
		}
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancelShutdown()

	shutdownErr := srv.Shutdown(shutdownCtx)

	// Serve returns right after Shutdown starts; wait for it so ln is closed
	if err := <-errorChannel; err != nil {
		return errors.Wrap(err, "http server stopped")
	}

	if shutdownErr != nil {
		return errors.Wrap(shutdownErr, "could not shut down http server")
	}

	return nil
}

// Reload reads the config file again and applies the nodes in it. Nodes new
// to the file are added, nodes whose addr or token changed are updated and
// nodes no longer in the file are marked REMOVED. Statuses are otherwise left
// to the health checks, and a REMOVED node stays REMOVED until it registers.
// The http settings need a restart and are not applied. On error the running
// config is left unchanged.
func (s *Service) Reload() error {
	s.m.Lock()
	defer s.m.Unlock()

	if s.cfg.configFile == "" {
		s.log.Info().Msg("reload: no config file set, nothing to reload")
		return nil
	}

	// start from the running http values, so keys missing from the file
	// (set by flags) do not read as changes
	next := NewConfig()
	next.Http = s.cfg.Http

	if err := next.LoadFromFile(s.cfg.configFile); err != nil {
		return errors.Wrap(err, "could not load config")
	}

	if next.Http != s.cfg.Http {
		s.log.Warn().Msg("reload: http settings changed, restart required to apply them; keeping current http settings")
		next.Http = s.cfg.Http
	}

	var added, updated, removed int

	inFile := make(map[string]struct{}, len(next.Nodes))
	for _, entry := range next.Nodes {
		if entry == nil {
			continue
		}

		if _, ok := inFile[entry.Name]; ok {
			s.log.Warn().Msgf("reload: duplicate node %s in config, ignoring", entry.Name)
			continue
		}
		inFile[entry.Name] = struct{}{}

		idx := slices.IndexFunc(s.workerNodes, func(n *node.Node) bool {
			return n.Name == entry.Name
		})

		if idx < 0 {
			s.workerNodes = append(s.workerNodes, newWorkerNode(entry))
			added++

			s.log.Debug().Msgf("reload: added node %s %s", entry.Name, entry.Addr)

			continue
		}

		workerNode := s.workerNodes[idx]
		// an entry without labels keeps the ones the node already has
		labels := entry.Labels
		if labels == nil {
			labels = workerNode.Labels()
		}

		if workerNode.Addr() != entry.Addr || workerNode.Token() != entry.Token || !maps.Equal(workerNode.Labels(), labels) {
			workerNode.Update(entry.Addr, entry.Token, labels)
			updated++

			s.log.Debug().Msgf("reload: updated node %s %s", entry.Name, entry.Addr)
		}
	}

	for _, workerNode := range s.workerNodes {
		if _, ok := inFile[workerNode.Name]; ok || workerNode.Status() == node.StatusRemoved {
			continue
		}

		workerNode.SetStatus(node.StatusRemoved)
		removed++

		s.log.Debug().Msgf("reload: removed node %s", workerNode.Name)
	}

	s.cfg.Nodes = next.Nodes

	s.log.Info().Msgf("reloaded config: %d nodes added, %d updated, %d removed", added, updated, removed)

	return nil
}

// ErrNodeNotFound is returned when a request names a node the server does not know.
var ErrNodeNotFound = errors.New("node not found")

// OnRegister adds a node, or refreshes the address, token and labels of the
// node with the same name. Node names are unique; a re-register never adds a
// duplicate, and also brings a REMOVED node back.
func (s *Service) OnRegister(ctx context.Context, req RegisterRequest) error {
	l := log.Ctx(ctx)

	l.Debug().Msgf("register node: %s", req.NodeName)

	// validate token
	if s.cfg.Http.Token != req.ServerToken {
		return errors.New("could not register node: bad token")
	}

	newNode := node.NewNode(req.NodeName, req.ClientAddr, req.AgentToken, "worker")
	newNode.SetLabels(req.Labels)

	if err := newNode.VerifyToken(ctx); err != nil {
		s.log.Error().Err(err).Msgf("could not verify agent token")
		return err
	}

	s.m.Lock()
	defer s.m.Unlock()

	idx := slices.IndexFunc(s.workerNodes, func(n *node.Node) bool {
		return n.Name == req.NodeName
	})

	// persist first so memory never holds a node the config file does not
	if err := s.saveNodeToConfig(req.NodeName, req.ClientAddr, req.AgentToken, req.Labels); err != nil {
		l.Error().Err(err).Msgf("could not write node to config")
		return err
	}

	if idx >= 0 {
		workerNode := s.workerNodes[idx]
		workerNode.Update(req.ClientAddr, req.AgentToken, req.Labels)
		workerNode.SetStatus(node.StatusReady)

		l.Info().Msgf("on register: updated node %s %s", req.NodeName, req.ClientAddr)

		return nil
	}

	s.workerNodes = append(s.workerNodes, newNode)

	l.Info().Msgf("on register: new node %s %s", req.NodeName, req.ClientAddr)

	return nil
}

// saveNodeToConfig adds or updates the config entry for nodeName and writes
// the config file. Nothing is written when the entry is already up to date.
// s.cfg.Nodes is only changed after a successful write. Callers hold s.m.
func (s *Service) saveNodeToConfig(nodeName string, clientAddr string, token string, labels map[string]string) error {
	idx := slices.IndexFunc(s.cfg.Nodes, func(n *AgentNode) bool {
		return n != nil && n.Name == nodeName
	})

	if idx >= 0 && s.cfg.Nodes[idx].Addr == clientAddr && s.cfg.Nodes[idx].Token == token && maps.Equal(s.cfg.Nodes[idx].Labels, labels) {
		return nil
	}

	log.Debug().Msgf("save node to config: %s %s", nodeName, clientAddr)

	entry := &AgentNode{
		Name:   nodeName,
		Addr:   clientAddr,
		Token:  token,
		Labels: labels,
	}

	nodes := slices.Clone(s.cfg.Nodes)
	if idx >= 0 {
		nodes[idx] = entry
	} else {
		nodes = append(nodes, entry)
	}

	if err := s.cfg.writeNodesToFile(nodes); err != nil {
		return errors.Wrapf(err, "could not write node to config: %s", nodeName)
	}

	return nil
}

// Deregister marks the node REMOVED. It stays in memory and in the config
// file, and becomes active again when the agent registers.
func (s *Service) Deregister(ctx context.Context, req DeregisterRequest) error {
	log.Info().Msgf("deregister: node %s", req.NodeName)

	s.m.Lock()
	defer s.m.Unlock()

	for _, workerNode := range s.workerNodes {
		if workerNode.Name == req.NodeName {
			workerNode.SetStatus(node.StatusRemoved)
			return nil
		}
	}

	return errors.Wrapf(ErrNodeNotFound, "could not deregister node: %s", req.NodeName)
}

func (s *Service) GetNodes() []*node.Node {
	s.m.RLock()
	defer s.m.RUnlock()
	return s.workerNodes
}

// HealthChecks checks every node each HealthCheckInterval until ctx is done.
func (s *Service) HealthChecks(ctx context.Context) {
	ticker := time.NewTicker(s.HealthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.healthChecks(ctx); err != nil {
				s.log.Error().Err(err).Msg("health checks failed")
			}
		}
	}
}

func (s *Service) healthChecks(ctx context.Context) error {
	fetcher := errgroup.Group{}

	workerNodes := s.GetNodes()

	for _, n := range workerNodes {
		if n.Status() == node.StatusRemoved {
			s.log.Trace().Msgf("healthcheck: %s Status: %s ignored", n.Name, node.StatusRemoved)
			continue
		}

		fetcher.Go(func() error {
			//log.Trace().Msgf("healthcheck: %s", n.Name)

			if err := n.HealthCheck(ctx); err != nil {
				log.Error().Err(err).Msgf("agent healthcheck failed: %s", n.Name)

				n.SetHealth(node.StatusUnknown)

				log.Warn().Msgf("healthcheck: %s Status: %s", n.Name, n.Status())

				return err
			}

			n.SetHealth(node.StatusReady)

			log.Trace().Msgf("healthcheck: %s Status: %s", n.Name, n.Status())

			return nil
		})
	}

	if err := fetcher.Wait(); err != nil {
		log.Error().Err(err).Msg("health checks failed for node(s)")
		return errors.Wrap(err, "health checks failed for node(s)")
	}

	return nil
}

func (s *Service) ProcessTasks() {
	ticker := time.NewTicker(10 * time.Second)
	done := make(chan bool)

	go func() {
		for {
			select {
			case <-done:
				return
			case t := <-ticker.C:
				fmt.Println("Tick at", t)
				log.Debug().Msgf("Processing task queue")

				// FIXME change to read from queue
				s.SendWork(context.Background(), task.NewEvent())
				log.Debug().Msgf("sleeping for 10 seconds...")
			}
		}
	}()
}

func (s *Service) SendWork(ctx context.Context, te task.Event) error {
	ctx, cancel := context.WithTimeout(ctx, s.WebhookTimeout)
	defer cancel()

	l := logger.GetWithCtx(ctx)

	l.Debug().Msgf("received task: %+v", te.Task)

	l.Trace().Msg("selecting workers")

	// select workers
	nodes, rejected, err := s.selectWorkers(ctx, te.Task)
	if err != nil {
		l.Error().Err(err).Msg("error selecting nodes")
		return errors.Wrap(err, "could not select nodes for task")
	}

	if len(nodes) == 0 {
		summary := summarizeRejections(rejected)
		l.Info().Msgf("found no nodes to send work to: %s", summary)
		return errors.Errorf("no ready nodes available to handle the task: %s", summary)
	}

	l.Debug().Msgf("selected %d nodes", len(nodes))

	// TODO proxy download to only download once

	fetcher := errgroup.Group{}

	var succeeded atomic.Int64

	// post to worker nodes
	for _, n := range nodes {
		subLogger := l.With().Str("node", n.Name).Logger()

		fetcher.Go(func() error {
			subLogger.Debug().Msgf("sending task to: %s", n.Name)

			taskCtx, cancel := context.WithTimeout(ctx, s.TaskTimeout)
			defer cancel()

			if err := n.StartTask(taskCtx, &te); err != nil {
				subLogger.Error().Err(err).Msgf("error could not send task to node: %s", n.Name)
				return err
			}

			subLogger.Info().Msgf("successfully sent task to %s", n.Name)

			succeeded.Add(1)

			return nil
		})
	}

	// wait for every node, then apply best-effort semantics: the task is
	// considered scheduled as long as at least one node accepted it.
	err = fetcher.Wait()

	ok := succeeded.Load()
	if ok == 0 {
		l.Error().Err(err).Msg("error sending task: all nodes failed")
		return errors.Wrap(err, "failed to send task to any node")
	}

	if err != nil {
		l.Warn().Err(err).Msgf("scheduled download on %d/%d nodes; some nodes failed", ok, len(nodes))
	} else {
		l.Info().Msgf("successfully scheduled download on %d nodes", ok)
	}

	return nil
}

// schedulerFor returns the scheduler a task asks for, least active if it asks
// for none.
func (s *Service) schedulerFor(schedulerType string) (scheduler.Scheduler, error) {
	switch schedulerType {
	case "", scheduler.TypeLeastActive:
		return &scheduler.LeastActive{}, nil
	case scheduler.TypeRoundRobin:
		return s.roundRobin, nil
	}

	return nil, errors.Wrapf(scheduler.ErrUnknownType, "'%s', want %s or %s", schedulerType, scheduler.TypeLeastActive, scheduler.TypeRoundRobin)
}

func (s *Service) selectWorkers(ctx context.Context, t task.Task) ([]*node.Node, []scheduler.NodeRejection, error) {
	sc, err := s.schedulerFor(t.SchedulerType)
	if err != nil {
		return nil, nil, err
	}

	// select candidates
	candidates, rejected := sc.SelectCandidateNodes(ctx, t, s.GetNodes())
	if len(candidates) == 0 {
		return nil, rejected, nil
	}

	// score
	scores := sc.Score(ctx, t, candidates)
	if len(scores) == 0 {
		return nil, rejected, nil
	}

	// pick
	nodes := sc.PickN(scores, candidates, t.MaxAllowedReplicas)

	s.log.Trace().Msgf("task max replicas %d", t.MaxAllowedReplicas)

	return nodes, rejected, nil
}

// summarizeRejections renders why no candidate nodes were available, for the
// caller-facing error and logs (e.g. "node1 (client qbit: disk_full); node2 (node not ready (UNKNOWN))").
func summarizeRejections(rejections []scheduler.NodeRejection) string {
	if len(rejections) == 0 {
		return "no worker nodes registered"
	}

	parts := make([]string, 0, len(rejections))
	for _, r := range rejections {
		parts = append(parts, fmt.Sprintf("%s (%s)", r.Node, strings.Join(r.Reasons, "; ")))
	}

	return strings.Join(parts, "; ")
}

func (s *Service) AddTask(ctx context.Context, te task.Event) error {
	// TODO add to queue

	return s.SendWork(ctx, te)
}

func (s *Service) QueueTask(ctx context.Context, te task.Event) {
	// TODO add to queue

	s.SendWork(ctx, te)
}

type RegisterRequest struct {
	NodeName    string            `json:"node_name"`
	RemoteAddr  string            `json:"remote_addr,omitempty"`
	ClientAddr  string            `json:"client_addr"`
	AgentToken  string            `json:"token"`
	Labels      map[string]string `json:"labels"`
	ServerToken string            `json:"-"`
}

type DeregisterRequest struct {
	NodeName string `json:"node_name"`
	//RemoteAddr string `json:"remote_addr,omitempty"`
	//ClientAddr string `json:"client_addr"`
	//AgentToken string `json:"api_key"`
}

type ScheduleDownloadRequest struct {
	DownloadUrl  string `json:"download_url"`
	Filename     string `json:"filename"`
	InfoHash     string `json:"info_hash"`
	Category     string `json:"category"`
	Tags         string `json:"tags"`
	MaxDownloads int    `json:"max_downloads"`
	Mode         string `json:"mode"`
	Opts         map[string]string
}

type TorrentDownloadRequest struct {
	DownloadUrl  string            `json:"download_url"`
	Filename     string            `json:"filename"`
	InfoHash     string            `json:"info_hash"`
	Category     string            `json:"category"`
	Tags         string            `json:"tags"`
	MaxDownloads int               `json:"max_downloads"`
	Mode         string            `json:"mode"`
	Opts         map[string]string `json:"opts"`
}
