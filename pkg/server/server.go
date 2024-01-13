package server

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/autobrr/distribrr/pkg/agent"
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

	log zerolog.Logger
}

func NewService(cfg *Config) *Service {
	s := &Service{
		cfg:         cfg,
		workerNodes: make([]*node.Node, 0),
		log:         log.Logger.With().Str("module", "server").Logger(),
	}

	//if cfg.Workers != nil {
	//	//s.workerNodes = cfg.Workers
	//	for _, w := range cfg.Workers {
	//		n := node.Node{
	//			Name:   w.Name,
	//			Addr:   w.Addr,
	//			Client: agent.NewClient(w.Addr, w.Name, w.Token),
	//		}
	//
	//		s.workerNodes = append(s.workerNodes, &n)
	//	}
	//}

	return s
}

func (s *Service) Run() {
	srv := NewAPIServer(s.cfg, s)

	errorChannel := make(chan error)
	go func() {
		errorChannel <- srv.Open()
	}()

	go s.HealthChecks()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGKILL, syscall.SIGTERM)

	for sig := range sigCh {
		log.Info().Msgf("got signal %q, shutting down server", sig)
		os.Exit(0)
	}
}

func (s *Service) OnRegister(ctx context.Context, req RegisterRequest) error {
	if s.cfg.Http.Token != req.Token {
		return errors.New("could not register node: bad token")
	}

	n := node.Node{
		Name:        req.NodeName,
		Addr:        req.ClientAddr,
		Client:      agent.NewClient(req.ClientAddr, req.NodeName, req.Token),
		DateCreated: time.Now().UTC(),
	}

	s.workerNodes = append(s.workerNodes, &n)

	l := log.Ctx(ctx)

	l.Info().Msgf("on register: new node %s %s", req.NodeName, req.ClientAddr)

	return nil
}

func (s *Service) Deregister(req DeregisterRequest) error {
	slices.DeleteFunc(s.workerNodes, func(node *node.Node) bool {
		return node.Name == req.NodeName
	})

	// TODO remove from config file

	log.Info().Msgf("deregister: node %s", req.NodeName)

	return nil
}

func (s *Service) HealthChecks() {
	for {
		fetcher := errgroup.Group{}

		for _, n := range s.workerNodes {
			n := n

			fetcher.Go(func() error {
				//log.Debug().Msgf("healthcheck: %s", n.Name)

				if err := n.HealthCheck(context.Background()); err != nil {
					log.Error().Err(err).Msgf("agent healthcheck failed: %s", n.Name)
					return err
				}

				return nil
			})
		}

		if err := fetcher.Wait(); err != nil {
			log.Error().Err(err).Msg("health checks failed for node(s)")
		}

		time.Sleep(15 * time.Second)
	}
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

	l.Trace().Msg("selecting agents")

	// select workers
	//nodes := s.workerNodes
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

	l.Trace().Msgf("task max replicas %d", te.Task.MaxDownloads)

	// check maxDownloads and add to X nodes
	if len(nodes) > te.Task.MaxDownloads {
		nodes = nodes[:te.Task.MaxDownloads]

		l.Trace().Msgf("preparing to send task to %d node(s)", len(nodes))
	}

	fetcher := errgroup.Group{}

	nodesOK := 0

	// post to worker nodes
	for _, n := range nodes {
		n := n

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
	candidates := sc.SelectCandidateNodes(t, s.workerNodes)
	if len(candidates) == 0 {
		return nil, nil
	}

	// score
	scores := sc.Score(ctx, t, candidates)
	if len(scores) == 0 {
		return nil, nil
	}

	// pick
	nodes := sc.Pick(scores, candidates)

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

type RegisterRequest struct {
	NodeName   string `json:"node_name"`
	RemoteAddr string `json:"remote_addr,omitempty"`
	ClientAddr string `json:"client_addr"`
	Token      string `json:"api_key"`
}

type DeregisterRequest struct {
	NodeName string `json:"node_name"`
	//RemoteAddr string `json:"remote_addr,omitempty"`
	//ClientAddr string `json:"client_addr"`
	//Token string `json:"api_key"`
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
