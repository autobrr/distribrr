package agent

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/autobrr/distribrr/pkg/server/client"
	"github.com/autobrr/distribrr/pkg/stats"
	"github.com/autobrr/distribrr/pkg/task"

	"github.com/autobrr/go-qbittorrent"
	"github.com/rs/zerolog/log"
	"golang.org/x/sync/errgroup"
)

type Service struct {
	cfg *Config

	controlNode *controlNode
	clients     map[string]*QbitClient
	stats       *stats.Stats
	taskCount   int

	serverClient *serverclient.Client
}

type controlNode struct {
	Addr  string
	Token string
}

func NewService(cfg *Config) *Service {
	s := &Service{
		cfg:       cfg,
		clients:   map[string]*QbitClient{},
		stats:     &stats.Stats{},
		taskCount: 0,
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
	if err := s.Join(ctx, s.cfg.Manager.Addr, s.cfg.Manager.Token, s.cfg.Agent); err != nil {
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
func (s *Service) Join(ctx context.Context, addr string, token string, agent Agent) error {
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

func (s *Service) GetFreeSpace(ctx context.Context, dir string) error {
	return nil
}

//func checkFreeSpace(paths []string) (bool, error) {
//	for _, path := range paths {
//		splits := strings.Split(path, "=")
//
//		if len(splits) >= 2 {
//			minFreeBytes, err := humanize.ParseBytes(splits[1])
//			if err != nil {
//				return false, err
//			}
//
//			usage := diskusage.NewDiskUsage(splits[0])
//			available := usage.Available()
//
//			if available <= minFreeBytes {
//				log.Debug().Msgf("less free space than wanted. got: %s wanted: %s", humanize.Bytes(available), humanize.Bytes(minFreeBytes))
//				return false, nil
//			}
//		}
//	}
//
//	return true, nil
//}

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

	for _, client := range s.clients {
		sender.Go(func() error {
			log.Debug().Msgf("add torrent %s to %s", t.Name, client.Name)

			// send downloads
			if err := client.Client.AddTorrentFromUrlCtx(ctx, t.DownloadURL, opts); err != nil {
				log.Error().Err(err).Msgf("error adding torrent from file %s to qbit: %s", t.Name, client.Name)
				return err
			}

			// TODO handle reannounce
			//if req.InfoHash != "" {
			//	log.Debug().Msgf("trying to re-announce torrent: %s", req.InfoHash)
			//
			//	go func(req domain.TorrentDownloadRequest) {
			//		// reannounce
			//		options := qbittorrent.ReannounceOptions{
			//			Interval:        7,
			//			MaxAttempts:     50,
			//			DeleteOnFailure: false,
			//		}
			//		if err := client.Client.ReannounceTorrentWithRetry(context.Background(), req.InfoHash, &options); err != nil {
			//			log.Error().Err(err).Msgf("error re-announcing torrent %s on qbit: %s", req.InfoHash, client.Name)
			//		}
			//	}(req)
			//}

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
	s.stats = stats.GetStats()
	s.GetClientStats(ctx)
	return s.stats
}

func (s *Service) GetStats() *stats.Stats {
	log.Trace().Msg("collecting stats")
	s.stats = stats.GetStats()

	return s.stats
}

func (s *Service) GetClientStats(ctx context.Context) *stats.Stats {
	log.Trace().Msg("collecting stats")
	//s.stats = stats.GetStats()

	// TODO use errgroup
	for name, client := range s.clients {
		l := log.With().Str("client", name).Logger()

		l.Trace().Msg("check disk per path for client")

		for _, storage := range client.Rules.Storage {
			l.Trace().Msgf("check disk for path %q", storage.Path)

			s.stats.DiskPathStats[storage.Path] = stats.GetDiskInfoByPath(storage.Path)
		}

		l.Trace().Msg("get active torrents for client")

		activeDownloads, err := client.Client.GetTorrentsActiveDownloadsCtx(ctx)
		if err != nil {
			l.Error().Err(err).Msgf("could not load active torrents for client")
			continue
		}

		l.Trace().Msgf("found %d active torrents for client", len(activeDownloads))

		ct := stats.ClientStats{
			ActiveDownloads: len(activeDownloads),
			Ready:           len(activeDownloads) < client.Rules.Torrents.MaxActiveDownloads,
		}

		l.Trace().Msgf("[%d/%d] active downloads, status ready: %t", len(activeDownloads), client.Rules.Torrents.MaxActiveDownloads, ct.Ready)
		l.Debug().Msgf("client ready: %t", ct.Ready)

		s.stats.ClientStats[name] = ct
	}

	s.taskCount = s.stats.TaskCount

	return s.stats
}

func (s *Service) GetLabels() map[string]string {
	return s.cfg.Agent.Labels
}
