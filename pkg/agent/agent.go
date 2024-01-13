package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/autobrr/distribrr/pkg/diskusage"
	"github.com/autobrr/distribrr/pkg/stats"
	"github.com/autobrr/distribrr/pkg/task"

	"github.com/autobrr/go-qbittorrent"
	"github.com/dustin/go-humanize"
	"github.com/rs/zerolog/log"
	"golang.org/x/sync/errgroup"
)

type Service struct {
	cfg *Config

	controlNode *controlNode
	clients     map[string]*QbitClient
	stats       *stats.Stats
	taskCount   int
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

		s.Deregister()

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
	if err := s.Join(ctx, s.cfg.Manager.Addr, s.cfg.Manager.Token, s.cfg.Agent.NodeName, s.cfg.Agent.ClientAddr); err != nil {
		//log.Error().Err(err).Msgf("could not register agent and join server: %s", s.cfg.Manager.Addr)
		return err
	}

	return nil
}

func (s *Service) Deregister() {
	log.Info().Msgf("deregister")
}

// Join worker to manager
func (s *Service) Join(ctx context.Context, addr string, token string, name string, clientAddr string) error {
	log.Info().Msgf("sending join request to: %s", addr)

	nodeName := name
	if nodeName == "" {
		h, err := os.Hostname()
		if err != nil {
			log.Error().Err(err).Msg("could not get hostname")
		}

		if h != "" {
			nodeName = h
		}
	}

	joinReq := JoinRequest{
		NodeName:   nodeName,
		ClientAddr: clientAddr,
	}

	if err := joinRequest(ctx, addr, token, joinReq); err != nil {
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
	return nil
}

func (s *Service) GetFreeSpace(ctx context.Context, dir string) error {
	return nil
}

//func (s *Service) CanDownload(ctx context.Context) ([]*QbitClient, error) {
//	fetcher := errgroup.Group{}
//
//	readyClients := []*QbitClient{}
//
//	for _, c := range s.clients {
//		c := c
//
//		fetcher.Go(func() error {
//			// get active downloads
//			fmt.Println(c.Name)
//
//			hasSpace, err := checkFreeSpace(c.Rules.FreeSpace)
//			if err != nil {
//				log.Error().Err(err).Msg("error checking free space")
//				return err
//			}
//
//			if !hasSpace {
//				return nil
//			}
//
//			activeDownloads, err := c.Client.GetTorrentsActiveDownloadsCtx(ctx)
//			if err != nil {
//				log.Error().Err(err).Msgf("error could not get torrents from qbit: %s", c.Name)
//				return err
//			}
//
//			if len(activeDownloads) <= c.Rules.MaxActiveDownloads {
//				readyClients = append(readyClients, c)
//			}
//
//			return nil
//		})
//	}
//
//	if err := fetcher.Wait(); err != nil {
//		log.Error().Err(err).Msg("error could not get torrents")
//	}
//
//	return readyClients, nil
//}

func checkFreeSpace(paths []string) (bool, error) {
	for _, path := range paths {
		splits := strings.Split(path, "=")

		if len(splits) >= 2 {
			minFreeBytes, err := humanize.ParseBytes(splits[1])
			if err != nil {
				return false, err
			}

			usage := diskusage.NewDiskUsage(splits[0])
			available := usage.Available()

			if available <= minFreeBytes {
				log.Debug().Msgf("less free space than wanted. got: %s wanted: %s", humanize.Bytes(available), humanize.Bytes(minFreeBytes))
				return false, nil
			}
		}
	}

	return true, nil
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

	for _, c := range s.clients {
		c := c
		//fmt.Println(c.Name)
		//if downloads > req.MaxDownloads {
		//	break
		//}

		sender.Go(func() error {
			log.Debug().Msgf("add torrent %s to %s", t.Name, c.Name)

			// send downloads
			if err := c.Client.AddTorrentFromUrlCtx(ctx, t.DownloadURL, opts); err != nil {
				log.Error().Err(err).Msgf("error adding torrent from file %s to qbit: %s", t.Name, c.Name)
				return err
			}

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
			//		if err := c.Client.ReannounceTorrentWithRetry(context.Background(), req.InfoHash, &options); err != nil {
			//			log.Error().Err(err).Msgf("error re-announcing torrent %s on qbit: %s", req.InfoHash, c.Name)
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

func (s *Service) GetStatsFull() *stats.Stats {
	s.stats = stats.GetStats()
	s.GetClientStats()
	return s.stats
}

func (s *Service) GetStats() *stats.Stats {
	log.Trace().Msg("collecting stats")
	s.stats = stats.GetStats()

	// TODO use errgroup
	//for _, client := range s.clients {
	//	l := log.With().Str("client", client.Name).Logger()
	//
	//
	//	l.Trace().Msg("check disk per path for client")
	//
	//	for _, storage := range client.Rules.Storage {
	//		l.Trace().Msgf("check disk for path %q", storage.Path)
	//
	//		s.stats.DiskPathStats[storage.Path] = stats.GetDiskInfoByPath(storage.Path)
	//	}
	//
	//	l.Trace().Msg("get active torrents for client")
	//
	//	t, err := client.Client.GetTorrentsActiveDownloadsCtx(context.Background())
	//	if err != nil {
	//		l.Error().Err(err).Msgf("could not load active torrents for client: %q", client.Name)
	//		continue
	//	}
	//
	//	l.Trace().Msgf("found %d active torrents for client", len(t))
	//
	//	ct := stats.ClientStats{
	//		ClientActiveDownloads: len(t),
	//		ClientReady:           len(t) < client.Rules.Torrents.MaxActiveDownloads,
	//	}
	//
	//	l.Trace().Msgf("client ready: %t", ct.ClientReady)
	//
	//	s.stats.ClientStats[client.Name] = ct
	//}
	//
	//s.taskCount = s.stats.TaskCount

	return s.stats
}

func (s *Service) GetClientStats() *stats.Stats {
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

		t, err := client.Client.GetTorrentsActiveDownloadsCtx(context.Background())
		if err != nil {
			l.Error().Err(err).Msgf("could not load active torrents for client")
			continue
		}

		l.Trace().Msgf("found %d active torrents for client", len(t))

		ct := stats.ClientStats{
			ClientActiveDownloads: len(t),
			ClientReady:           len(t) < client.Rules.Torrents.MaxActiveDownloads,
		}

		l.Trace().Msgf("client ready: %t", ct.ClientReady)

		s.stats.ClientStats[name] = ct
	}

	s.taskCount = s.stats.TaskCount

	return s.stats
}

type JoinRequest struct {
	NodeName   string `json:"node_name"`
	ClientAddr string `json:"client_addr"`
}

type JoinResponse struct {
	NodeName string `json:"node_name"`
}

func joinRequest(ctx context.Context, addr string, token string, joinReq JoinRequest) error {
	reqUrl := buildUrl(addr, "/node/register", nil)

	log.Trace().Msgf("join url: %s", reqUrl)

	body, err := json.Marshal(joinReq)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqUrl, bytes.NewBuffer(body))
	if err != nil {
		return err
	}

	//req.Header.Add("Authorization", token)
	setHeaders(ctx, req, token)

	client := &http.Client{
		Timeout: DefaultTimeout,
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("bad status: %d\n", resp.StatusCode)
	}

	return nil
}

func setHeaders(ctx context.Context, req *http.Request, token string) {
	req.Header.Add("Authorization", token)
	req.Header.Add("User-Agent", "distribrr-client")

	if ctx != nil {
		if cid := ctx.Value("correlation_id").(string); cid != "" {
			req.Header.Add("X-Correlation-ID", cid)
		}
	}
}

func buildUrl(addr string, endpoint string, params map[string]string) string {
	apiBase := "/api/v1/"

	// add query params
	queryParams := url.Values{}
	for key, value := range params {
		queryParams.Add(key, value)
	}

	joinedUrl, _ := url.JoinPath(addr, apiBase, endpoint)
	parsedUrl, _ := url.Parse(joinedUrl)
	parsedUrl.RawQuery = queryParams.Encode()

	// make into new string and return
	return parsedUrl.String()
}
