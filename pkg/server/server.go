package server

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"sync"
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

type Service struct {
	cfg         *Config
	workerNodes []*node.Node
	m           sync.RWMutex

	log zerolog.Logger
}

func NewService(cfg *Config) *Service {
	s := &Service{
		cfg:         cfg,
		workerNodes: make([]*node.Node, 0),
		log:         log.Logger.With().Str("module", "server").Logger(),
		m:           sync.RWMutex{},
	}

	s.m.Lock()
	for _, w := range cfg.Nodes {
		if w != nil {
			s.workerNodes = append(s.workerNodes, node.NewNode(w.Name, w.Addr, w.Token, "worker"))
		}
	}
	s.m.Unlock()

	return s
}

func (s *Service) Run() {
	srv := NewAPIServer(s.cfg, s)

	errorChannel := make(chan error)
	go func() {
		errorChannel <- srv.Open()
	}()

	//go s.HealthChecks()
	go s.HealthChecks()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGKILL, syscall.SIGTERM)

	for sig := range sigCh {
		log.Info().Msgf("got signal %q, shutting down server", sig)
		os.Exit(0)
	}
}

func (s *Service) OnRegister(ctx context.Context, req RegisterRequest) error {
	l := log.Ctx(ctx)

	l.Debug().Msgf("register node: %s", req.NodeName)

	// validate token
	if s.cfg.Http.Token != req.ServerToken {
		return errors.New("could not register node: bad token")
	}

	newNode := node.NewNode(req.NodeName, req.ClientAddr, req.AgentToken, "worker")
	newNode.Labels = req.Labels

	if err := newNode.VerifyToken(ctx); err != nil {
		s.log.Error().Err(err).Msgf("could not verify agent token")
		return err
	}

	// check s.workerNodes if it includes item by req.NodeName
	exists := slices.ContainsFunc(s.workerNodes, func(n *node.Node) bool {
		return n.Name == req.NodeName && n.Addr == req.ClientAddr
	})

	if exists {
		l.Debug().Msgf("node already exists in config: %s", req.NodeName)

		for _, workerNode := range s.workerNodes {
			if workerNode.Name == req.NodeName {
				// update labels
				workerNode.Labels = req.Labels
				workerNode.Status = node.StatusReady
			}
		}

		return nil
	}

	s.workerNodes = append(s.workerNodes, newNode)

	if err := s.appendNodeToConfig(ctx, req.NodeName, req.ClientAddr, req.AgentToken); err != nil {
		l.Error().Err(err).Msgf("could not write node to config")
		return err
	}

	l.Info().Msgf("on register: new node %s %s", req.NodeName, req.ClientAddr)

	return nil
}

func (s *Service) appendNodeToConfig(_ context.Context, nodeName string, clientAddr string, token string) error {
	log.Debug().Msgf("append node to config: %s %s", nodeName, clientAddr)

	a := AgentNode{
		Name:  nodeName,
		Addr:  clientAddr,
		Token: token,
	}

	s.cfg.Nodes = append(s.cfg.Nodes, &a)

	if err := s.cfg.WriteToFile(); err != nil {
		log.Error().Err(err).Msgf("could not write node to config")
		return errors.Wrap(err, "could not write node to config")
	}

	return nil
}

func (s *Service) Deregister(ctx context.Context, req DeregisterRequest) error {
	log.Info().Msgf("deregister: node %s", req.NodeName)

	for _, workerNode := range s.workerNodes {
		if workerNode.Name == req.NodeName {
			//s.workerNodes = append(s.workerNodes[:i], s.workerNodes[i+1:]...)
			workerNode.Status = node.StatusRemoved
			break
		}
	}

	//slices.DeleteFunc(s.workerNodes, func(node *node.Node) bool {
	//	return node.Name == req.NodeName
	//})

	//if err := s.removeNodeFromConfig(ctx, req.NodeName); err != nil {
	//	return err
	//}

	return nil
}

func (s *Service) removeNodeFromConfig(ctx context.Context, nodeName string) error {
	log.Debug().Msgf("remove node from config: node %s", nodeName)

	// remove from config slice
	slices.DeleteFunc(s.cfg.Nodes, func(agentNode *AgentNode) bool {
		return agentNode.Name == nodeName
	})

	l := log.Ctx(ctx)

	if len(s.cfg.Nodes) == 0 {
		s.cfg.Nodes = []*AgentNode{}
	}

	// remove from config file
	if err := s.cfg.WriteToFile(); err != nil {
		l.Error().Err(err).Msgf("could not write node to config")
		return err
	}

	log.Info().Msgf("deregister: node %s", nodeName)

	return nil
}

func (s *Service) GetNodes() []*node.Node {
	s.m.RLock()
	defer s.m.RUnlock()
	return s.workerNodes
}

func (s *Service) HealthChecks() {
	tickerDuration := time.Second * 10

	ticker := time.NewTicker(tickerDuration)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ctx := context.Background()

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
		if n.Status == node.StatusRemoved {
			s.log.Trace().Msgf("healthcheck: %s Status: %s ignored", n.Name, n.Status)
			continue
		}

		fetcher.Go(func() error {
			//log.Trace().Msgf("healthcheck: %s", n.Name)

			if err := n.HealthCheck(ctx); err != nil {
				log.Error().Err(err).Msgf("agent healthcheck failed: %s", n.Name)

				n.Status = node.StatusUnknown

				log.Warn().Msgf("healthcheck: %s Status: %s", n.Name, n.Status)

				return err
			}

			n.Status = node.StatusReady

			log.Trace().Msgf("healthcheck: %s Status: %s", n.Name, n.Status)

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

func (s *Service) SendWork(ctx context.Context, te task.Event) {
	//l := logger.Get()
	l := logger.GetWithCtx(ctx)

	l.Debug().Msgf("recieved task: %+v", te.Task)

	// TODO get tasks
	//te := task.NewEvent()

	l.Trace().Msg("selecting workers")

	// select workers
	nodes, err := s.selectWorkers(ctx, te.Task)
	if err != nil {
		l.Error().Err(err).Msg("error selecting nodes")
		return
	}

	if len(nodes) == 0 {
		l.Info().Msg("found no nodes to send work to")
		return
	}

	l.Debug().Msgf("selected %d nodes", len(nodes))

	// proxy download to only download once

	fetcher := errgroup.Group{}

	nodesOK := 0

	// post to worker nodes
	for _, n := range nodes {
		subLogger := l.With().Str("node", n.Name).Logger()

		fetcher.Go(func() error {
			subLogger.Debug().Msgf("sending task to: %s", n.Name)

			if err := n.StartTask(ctx, &te); err != nil {
				subLogger.Error().Err(err).Msgf("error could not send task to node: %s", n.Name)
				return err
			}

			subLogger.Info().Msgf("successfully sent task to %s", n.Name)

			nodesOK++

			return nil
		})
	}

	if err := fetcher.Wait(); err != nil {
		l.Error().Err(err).Msg("error sending tasks to nodes")
		return
	}

	if nodesOK == 0 {
		l.Warn().Msg("found 0 ready nodes")
		return
	}

	l.Info().Msgf("successfully scheduled download on %d nodes", nodesOK)
}

func (s *Service) selectWorkers(ctx context.Context, t task.Task) ([]*node.Node, error) {
	// hardcoded scheduler for now
	var sc scheduler.LeastActive

	// get candidates
	candidates := sc.SelectCandidateNodes(ctx, t, s.workerNodes)
	if len(candidates) == 0 {
		return nil, nil
	}

	// score
	scores := sc.Score(ctx, t, candidates)
	if len(scores) == 0 {
		return nil, nil
	}

	// pick
	nodes := sc.PickN(scores, candidates, t.MaxAllowedReplicas)

	s.log.Trace().Msgf("task max replicas %d", t.MaxAllowedReplicas)

	return nodes, nil
}

//func (s *Service) SelectWorkers(t task.Task) ([]*node.Node, error) {
//	var scdl scheduler.Scheduler
//	switch t.SchedulerType {
//	case "greedy":
//		scdl = &scheduler.Greedy{Name: "greedy"}
//	case "roundrobin":
//		scdl = &scheduler.RoundRobin{Name: "roundrobin"}
//	default:
//		scdl = &scheduler.Epvm{Name: "epvm"}
//	}
//
//	// select candidates
//	candidates := scdl.SelectCandidateNodes(t, s.workerNodes)
//	if candidates == nil {
//		return nil, errors.Errorf("no available candidates match resource request for task: %v", t)
//	}
//
//	// score
//	scores := scdl.Score(t, candidates)
//	if scores == nil {
//		return nil, errors.Errorf("no scores to task: %v", t)
//	}
//
//	// pick
//	nodes := scdl.Pick(scores, candidates)
//
//	return nodes, nil
//}

func (s *Service) AddTask(ctx context.Context, te task.Event) {
	// TODO add to queue

	s.SendWork(ctx, te)
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
